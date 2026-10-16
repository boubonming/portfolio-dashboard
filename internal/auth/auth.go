package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName = "portfolio_session"
	CSRFHeaderName    = "X-CSRF-Token"
	maxLoginAttempts  = 5
	loginWindow       = time.Minute
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrInvalidConfig   = errors.New("invalid authentication configuration")
)

type Config struct {
	Username      string
	PasswordHash  string
	SessionSecret string
	Machine       MachineConfig
	PublicOrigin  string
	Environment   string
	CookieSecure  bool
	SessionTTL    time.Duration
	Now           func() time.Time
}

func (c Config) Validate() error {
	if err := c.Machine.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Username) == "" || len(c.Username) > 256 {
		return fmt.Errorf("%w: username is required", ErrInvalidConfig)
	}
	if cost, err := bcrypt.Cost([]byte(c.PasswordHash)); err != nil || cost < bcrypt.MinCost+6 || cost > bcrypt.MaxCost {
		return fmt.Errorf("%w: password hash is invalid or too weak", ErrInvalidConfig)
	}
	if _, err := decodeSecret(c.SessionSecret); err != nil {
		return fmt.Errorf("%w: session secret must be base64-encoded random bytes of at least 32 bytes", ErrInvalidConfig)
	}
	if c.SessionTTL < time.Second || c.SessionTTL > 24*time.Hour {
		return fmt.Errorf("%w: session lifetime must be between 1 second and 24 hours", ErrInvalidConfig)
	}
	origin, err := parseOrigin(c.PublicOrigin)
	if err != nil {
		return fmt.Errorf("%w: public origin: %v", ErrInvalidConfig, err)
	}
	if strings.EqualFold(origin.Scheme, "http") && !strings.EqualFold(c.Environment, "development") && !strings.EqualFold(c.Environment, "test") {
		return fmt.Errorf("%w: HTTP public origins require development or test environment", ErrInvalidConfig)
	}
	return nil
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type SessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	CSRFToken     string `json:"csrf_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

type session struct {
	ID        string
	CSRFToken string
	ExpiresAt time.Time
}

type tokenPayload struct {
	ID        string `json:"id"`
	CSRFToken string `json:"csrf"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type loginAttempt struct {
	WindowStart time.Time
	Failures    int
	InFlight    int
}

// Authenticator owns the short-lived in-memory session registry. Keeping the
// registry in memory makes logout and rotation effective immediately and avoids
// putting session state or CSRF values in SQLite.
type Authenticator struct {
	config   Config
	secret   []byte
	mu       sync.Mutex
	sessions map[string]session
	attempts map[string]loginAttempt
}

func New(c Config) (*Authenticator, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	secret, _ := decodeSecret(c.SessionSecret)
	return &Authenticator{config: c, secret: secret, sessions: make(map[string]session), attempts: make(map[string]loginAttempt)}, nil
}

func (a *Authenticator) now() time.Time { return a.config.Now().UTC() }

// ActivityCursorSecret derives a purpose-specific key from the required
// session secret. The raw session secret is never exposed to the store.
func (a *Authenticator) ActivityCursorSecret() []byte {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte("portfolio-dashboard/activity-cursor/v1"))
	return mac.Sum(nil)
}

// ReviewCursorSecret derives a separate key for immutable review cursors. The
// session secret remains the only configured high-entropy secret, while domain
// separation prevents cursor tokens from being valid in another namespace.
func (a *Authenticator) ReviewCursorSecret() []byte {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte("portfolio-dashboard/review-cursor/v1"))
	return mac.Sum(nil)
}

func (a *Authenticator) MachinePrincipal() MachinePrincipal {
	return a.config.Machine.Principal()
}

func (a *Authenticator) MachineTokenValid(token string) bool {
	return a.config.Machine.MachineTokenValid(token)
}

func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request, request LoginRequest) (SessionResponse, bool) {
	if !a.reserveAttempt() {
		return SessionResponse{}, false
	}
	passwordOK := bcrypt.CompareHashAndPassword([]byte(a.config.PasswordHash), []byte(request.Password)) == nil
	usernameOK := subtle.ConstantTimeCompare([]byte(request.Username), []byte(a.config.Username)) == 1
	if !passwordOK || !usernameOK {
		a.finishAttempt(false)
		return SessionResponse{}, false
	}
	a.finishAttempt(true)
	id, err := randomToken(32)
	if err != nil {
		return SessionResponse{}, false
	}
	csrf, err := randomToken(32)
	if err != nil {
		return SessionResponse{}, false
	}
	now := a.now()
	s := session{ID: id, CSRFToken: csrf, ExpiresAt: now.Add(a.config.SessionTTL)}
	a.mu.Lock()
	for key := range a.sessions {
		delete(a.sessions, key)
	}
	a.sessions[hashID(id)] = s
	a.mu.Unlock()
	// Keep sub-second precision in the signed token. Unix-second truncation
	// makes a one-second session valid for less than its configured lifetime.
	token, err := a.sign(tokenPayload{ID: id, CSRFToken: csrf, IssuedAt: now.UnixNano(), ExpiresAt: s.ExpiresAt.UnixNano()})
	if err != nil {
		return SessionResponse{}, false
	}
	a.setCookie(w, token, s.ExpiresAt)
	return SessionResponse{Authenticated: true, CSRFToken: csrf, ExpiresAt: s.ExpiresAt.Format(time.RFC3339)}, true
}

func (a *Authenticator) Session(r *http.Request) (session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return session{}, ErrUnauthenticated
	}
	var payload tokenPayload
	if err := a.verify(cookie.Value, &payload); err != nil {
		return session{}, ErrUnauthenticated
	}
	now := a.now()
	if payload.ExpiresAt <= now.UnixNano() || payload.IssuedAt > now.Add(time.Minute).UnixNano() {
		return session{}, ErrUnauthenticated
	}
	a.mu.Lock()
	stored, ok := a.sessions[hashID(payload.ID)]
	a.mu.Unlock()
	if !ok || stored.ID != payload.ID || stored.CSRFToken != payload.CSRFToken || !stored.ExpiresAt.After(now) {
		return session{}, ErrUnauthenticated
	}
	return stored, nil
}

func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) error {
	cookie, err := r.Cookie(SessionCookieName)
	if err == nil {
		var payload tokenPayload
		if a.verify(cookie.Value, &payload) == nil {
			a.mu.Lock()
			delete(a.sessions, hashID(payload.ID))
			a.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.config.CookieSecure, SameSite: http.SameSiteStrictMode})
	return nil
}

func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Session(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, s)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Authenticator) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		s, ok := sessionFromContext(r.Context())
		if !ok || !a.sameOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeaderName)), []byte(s.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) sameOrigin(r *http.Request) bool {
	seen := false
	for _, header := range []string{"Origin", "Referer"} {
		values := r.Header.Values(header)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] == "" {
			return false
		}
		value := values[0]
		seen = true
		if header == "Origin" && !a.canonicalOriginMatches(value) {
			return false
		}
		if header == "Referer" && !a.canonicalRefererMatches(value) {
			return false
		}
	}
	return seen
}

// SameOrigin validates the browser origin headers for callers that perform
// authentication before a request has a session context (for example login).
func (a *Authenticator) SameOrigin(r *http.Request) bool { return a.sameOrigin(r) }

func (a *Authenticator) CanonicalOriginMatches(value string) bool {
	return a.canonicalOriginMatches(value)
}

// CanonicalRefererMatches compares the origin derived from a full Referer URL.
func (a *Authenticator) CanonicalRefererMatches(value string) bool {
	return a.canonicalRefererMatches(value)
}

func (a *Authenticator) sign(payload tokenPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *Authenticator) verify(token string, payload *tokenPayload) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[1]) < 20 {
		return errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(parts[0]))
	supplied, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(supplied, mac.Sum(nil)) {
		return errors.New("invalid token")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(encoded, payload) != nil {
		return errors.New("invalid token")
	}
	return nil
}

func (a *Authenticator) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", Expires: expires, MaxAge: int(a.config.SessionTTL.Seconds()), HttpOnly: true, Secure: a.config.CookieSecure, SameSite: http.SameSiteStrictMode})
}

// reserveAttempt admits work under the mutex, before bcrypt starts. The
// single configured account uses one bounded global bucket rather than an
// attacker-controlled username/IP map, so username churn cannot bypass it.
func (a *Authenticator) reserveAttempt() bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts["global"]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) >= loginWindow {
		attempt = loginAttempt{WindowStart: now}
	}
	if attempt.Failures >= maxLoginAttempts || attempt.InFlight >= 1 {
		a.attempts["global"] = attempt
		return false
	}
	attempt.Failures++
	attempt.InFlight++
	a.attempts["global"] = attempt
	return true
}

func (a *Authenticator) finishAttempt(success bool) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts["global"]
	if attempt.InFlight > 0 {
		attempt.InFlight--
	}
	if success {
		delete(a.attempts, "global")
		return
	}
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) >= loginWindow {
		attempt.WindowStart = now
		attempt.Failures = 1
	}
	a.attempts["global"] = attempt
}

func hashID(id string) string { sum := sha256.Sum256([]byte(id)); return hex.EncodeToString(sum[:]) }
func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

type sessionContextKey struct{}

func sessionFromContext(ctx context.Context) (session, bool) {
	s, ok := ctx.Value(sessionContextKey{}).(session)
	return s, ok
}

func decodeSecret(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("empty secret")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(decoded) < 32 {
		return nil, errors.New("secret is not sufficiently long")
	}
	var nonZero, distinct int
	seen := make(map[byte]struct{}, len(decoded))
	for _, value := range decoded {
		if value != 0 {
			nonZero++
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			distinct++
		}
	}
	if nonZero < len(decoded)/2 || distinct < 16 {
		return nil, errors.New("secret has insufficient diversity")
	}
	return decoded, nil
}

func parseOrigin(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, errors.New("origin is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("origin must be scheme plus host[:port] with no path, query, fragment, or userinfo")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return nil, errors.New("origin scheme must be http or https")
	}
	if strings.EqualFold(u.Scheme, "http") {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("http origin is allowed only for loopback development")
		}
	}
	if _, err := effectivePort(u); err != nil {
		return nil, err
	}
	return u, nil
}

func (a *Authenticator) canonicalOriginMatches(raw string) bool {
	expected, err := parseOrigin(a.config.PublicOrigin)
	if err != nil {
		return false
	}
	actual, err := parseOrigin(raw)
	if err != nil {
		return false
	}
	return canonicalOrigin(expected) == canonicalOrigin(actual)
}

func (a *Authenticator) canonicalRefererMatches(raw string) bool {
	expected, err := parseOrigin(a.config.PublicOrigin)
	if err != nil {
		return false
	}
	actual, err := parseRefererOrigin(raw)
	if err != nil {
		return false
	}
	return canonicalOrigin(expected) == canonicalOrigin(actual)
}

func parseRefererOrigin(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, errors.New("referer is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, errors.New("referer must be an absolute URL")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return nil, errors.New("referer scheme must be http or https")
	}
	if _, err := effectivePort(u); err != nil {
		return nil, err
	}
	return u, nil
}

func canonicalOrigin(u *url.URL) string {
	port, _ := effectivePort(u)
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func effectivePort(u *url.URL) (string, error) {
	host := u.Hostname()
	if host == "" {
		return "", errors.New("host is required")
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return "", errors.New("origin port is empty")
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443", nil
		}
		if strings.EqualFold(u.Scheme, "http") {
			return "80", nil
		}
		return "", errors.New("unsupported scheme")
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return "", errors.New("origin port must be numeric")
		}
	}
	value := 0
	for _, digit := range port {
		value = value*10 + int(digit-'0')
		if value > 65535 {
			return "", errors.New("origin port is out of range")
		}
	}
	if value == 0 {
		return "", errors.New("origin port is out of range")
	}
	return fmt.Sprintf("%d", value), nil
}

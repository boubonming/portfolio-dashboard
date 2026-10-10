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
	PublicOrigin  string
	CookieSecure  bool
	SessionTTL    time.Duration
	Now           func() time.Time
}

func (c Config) Validate() error {
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
	if _, err := parseOrigin(c.PublicOrigin); err != nil {
		return fmt.Errorf("%w: public origin: %v", ErrInvalidConfig, err)
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
	token, err := a.sign(tokenPayload{ID: id, CSRFToken: csrf, IssuedAt: now.Unix(), ExpiresAt: s.ExpiresAt.Unix()})
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
	if payload.ExpiresAt <= now.Unix() || payload.IssuedAt > now.Add(time.Minute).Unix() {
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
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		seen = true
		if !a.canonicalOriginMatches(value) {
			return false
		}
	}
	return seen
}

func (a *Authenticator) CanonicalOriginMatches(value string) bool {
	return a.canonicalOriginMatches(value)
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
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
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
	return strings.EqualFold(expected.Scheme, actual.Scheme) && strings.EqualFold(expected.Host, actual.Host)
}

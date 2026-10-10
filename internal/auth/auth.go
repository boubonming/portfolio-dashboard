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
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName = "portfolio_session"
	CSRFHeaderName    = "X-CSRF-Token"
	maxLoginAttempts  = 4096
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
	if len(c.SessionSecret) < 32 {
		return fmt.Errorf("%w: session secret must contain at least 32 bytes", ErrInvalidConfig)
	}
	if c.SessionTTL <= 0 || c.SessionTTL > 24*time.Hour {
		return fmt.Errorf("%w: session lifetime must be between 1 second and 24 hours", ErrInvalidConfig)
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
}

// Authenticator owns the short-lived in-memory session registry. Keeping the
// registry in memory makes logout and rotation effective immediately and avoids
// putting session state or CSRF values in SQLite.
type Authenticator struct {
	config   Config
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
	return &Authenticator{config: c, sessions: make(map[string]session), attempts: make(map[string]loginAttempt)}, nil
}

func (a *Authenticator) now() time.Time { return a.config.Now().UTC() }

func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request, request LoginRequest) (SessionResponse, bool) {
	key := loginKey(r, request.Username)
	if !a.allowAttempt(key) {
		return SessionResponse{}, false
	}
	passwordOK := bcrypt.CompareHashAndPassword([]byte(a.config.PasswordHash), []byte(request.Password)) == nil
	usernameOK := subtle.ConstantTimeCompare([]byte(request.Username), []byte(a.config.Username)) == 1
	if !passwordOK || !usernameOK {
		a.recordFailure(key)
		return SessionResponse{}, false
	}
	a.clearAttempt(key)
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
		if !ok || !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeaderName)), []byte(s.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	for _, header := range []string{"Origin", "Referer"} {
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		u, err := http.NewRequest(http.MethodGet, value, nil)
		if err != nil || u.URL.Host == "" || !strings.EqualFold(u.URL.Host, r.Host) {
			return false
		}
		expectedScheme := "http"
		if r.TLS != nil {
			expectedScheme = "https"
		}
		if u.URL.Scheme != expectedScheme {
			return false
		}
	}
	return true
}

func (a *Authenticator) sign(payload tokenPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, []byte(a.config.SessionSecret))
	_, _ = mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (a *Authenticator) verify(token string, payload *tokenPayload) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[1]) < 20 {
		return errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, []byte(a.config.SessionSecret))
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

func (a *Authenticator) allowAttempt(key string) bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.attempts[key]
	if now.Sub(attempt.WindowStart) >= time.Minute {
		return true
	}
	return attempt.Failures < 5
}
func (a *Authenticator) recordFailure(key string) {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.attempts[key]; !exists && len(a.attempts) >= maxLoginAttempts {
		for oldest := range a.attempts {
			delete(a.attempts, oldest)
			break
		}
	}
	attempt := a.attempts[key]
	if now.Sub(attempt.WindowStart) >= time.Minute {
		attempt = loginAttempt{WindowStart: now}
	}
	if attempt.WindowStart.IsZero() {
		attempt.WindowStart = now
	}
	attempt.Failures++
	a.attempts[key] = attempt
}
func (a *Authenticator) clearAttempt(key string) { a.mu.Lock(); delete(a.attempts, key); a.mu.Unlock() }

func loginKey(r *http.Request, username string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host + "\x00" + username
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

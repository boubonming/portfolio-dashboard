package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func fixtureAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := New(Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: strings.Repeat("s", 32), CookieSecure: true, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func TestSessionCookieRotationExpiryAndLogout(t *testing.T) {
	authenticator := fixtureAuthenticator(t)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	loginResponse := httptest.NewRecorder()
	result, ok := authenticator.Login(loginResponse, loginRequest, LoginRequest{Username: "fixture-user", Password: "fixture-only-value"})
	if !ok || !result.Authenticated || result.CSRFToken == "" {
		t.Fatalf("login result = %+v, ok=%v", result, ok)
	}
	cookie := loginResponse.Result().Cookies()[0]
	if cookie.Name != SessionCookieName || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie flags = %+v", cookie)
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	sessionRequest.AddCookie(cookie)
	if _, err := authenticator.Session(sessionRequest); err != nil {
		t.Fatal(err)
	}
	cookie.Value += "tampered"
	tamperedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	tamperedRequest.AddCookie(cookie)
	if _, err := authenticator.Session(tamperedRequest); err == nil {
		t.Fatal("tampered cookie was accepted")
	}

	logoutResponse := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(loginResponse.Result().Cookies()[0])
	if err := authenticator.Logout(logoutResponse, logoutRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Session(sessionRequest); err == nil {
		t.Fatal("logged-out session remained valid")
	}
}

func TestCSRFRequiresSessionBoundTokenAndOrigin(t *testing.T) {
	authenticator := fixtureAuthenticator(t)
	loginResponse := httptest.NewRecorder()
	result, ok := authenticator.Login(loginResponse, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), LoginRequest{Username: "fixture-user", Password: "fixture-only-value"})
	if !ok {
		t.Fatal("fixture login failed")
	}
	protected := authenticator.RequireAuth(authenticator.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	request := httptest.NewRequest(http.MethodPost, "http://example.test/private", nil)
	request.Host = "example.test"
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://example.test/private", nil)
	request.Host = "example.test"
	request.Header.Set("Origin", "https://attacker.test")
	request.Header.Set(CSRFHeaderName, result.CSRFToken)
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "http://example.test/private", nil)
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set(CSRFHeaderName, result.CSRFToken)
	request.AddCookie(loginResponse.Result().Cookies()[0])
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid csrf status = %d", response.Code)
	}
}

func TestConfigRejectsWeakHashAndSecret(t *testing.T) {
	if _, err := New(Config{Username: "fixture-user", PasswordHash: "not-a-hash", SessionSecret: strings.Repeat("s", 32), SessionTTL: time.Hour}); err == nil {
		t.Fatal("malformed password hash was accepted")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: strings.Repeat("s", 31), SessionTTL: time.Hour}); err == nil {
		t.Fatal("weak session secret or password hash was accepted")
	}
}

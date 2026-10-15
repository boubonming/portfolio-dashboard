package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	validSecret := base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123"))
	authenticator, err := New(Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: validSecret, PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour})
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
	request = httptest.NewRequest(http.MethodPost, "https://example.test/private", nil)
	request.Host = "example.test"
	request.Header.Set("Origin", "https://example.test")
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

func TestOriginAndRefererCanonicalization(t *testing.T) {
	authenticator := fixtureAuthenticator(t)
	cases := []struct {
		name   string
		header string
		value  string
		want   bool
	}{
		{"origin exact", "Origin", "https://example.test", true},
		{"origin default port", "Origin", "https://example.test:443/", true},
		{"origin non-default port", "Origin", "https://example.test:444", false},
		{"origin query rejected", "Origin", "https://example.test/?x=1", false},
		{"origin path rejected", "Origin", "https://example.test/portfolio", false},
		{"referer path and query", "Referer", "https://example.test/portfolio?x=1", true},
		{"referer cross-origin", "Referer", "https://attacker.test/portfolio?x=1", false},
		{"referer root", "Referer", "https://example.test", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.test/private", nil)
			request.Header.Set(tc.header, tc.value)
			if got := authenticator.SameOrigin(request); got != tc.want {
				t.Fatalf("SameOrigin(%s=%q) = %v, want %v", tc.header, tc.value, got, tc.want)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "https://example.test/private", nil)
	request.Header.Set("Origin", "https://example.test:443")
	request.Header.Set("Referer", "https://example.test/portfolio?x=1")
	if !authenticator.SameOrigin(request) {
		t.Fatal("matching origin and referer were rejected")
	}
	request.Header.Set("Referer", "https://attacker.test/portfolio?x=1")
	if authenticator.SameOrigin(request) {
		t.Fatal("mismatched origin and referer were accepted")
	}
}

func TestSameOriginRequiresAtLeastOneHeader(t *testing.T) {
	authenticator := fixtureAuthenticator(t)
	request := httptest.NewRequest(http.MethodPost, "https://example.test/private", nil)
	if authenticator.SameOrigin(request) {
		t.Fatal("request without origin headers was accepted")
	}
}

func TestSessionExpiryRetainsSubSecondPrecision(t *testing.T) {
	clock := time.Date(2026, time.January, 2, 12, 0, 0, int(500*time.Millisecond), time.UTC)
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := New(Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Second,
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	result, ok := authenticator.Login(response, httptest.NewRequest(http.MethodPost, "/login", nil), LoginRequest{Username: "fixture-user", Password: "fixture-only-value"})
	if !ok || !result.Authenticated {
		t.Fatalf("login result = %+v, ok=%v", result, ok)
	}
	request := httptest.NewRequest(http.MethodGet, "/session", nil)
	request.AddCookie(response.Result().Cookies()[0])
	clock = clock.Add(999 * time.Millisecond)
	if _, err := authenticator.Session(request); err != nil {
		t.Fatalf("session expired early: %v", err)
	}
	clock = clock.Add(time.Millisecond)
	if _, err := authenticator.Session(request); err == nil {
		t.Fatal("session remained valid at its precise expiry")
	}
}

func TestLoginAdmissionIsBoundedAcrossUsernameChurnAndRecoversByClock(t *testing.T) {
	clock := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	authenticator := fixtureAuthenticatorWithClock(t, &clock)
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, _ = authenticator.Login(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil), LoginRequest{Username: "churn-" + string(rune('a'+index)), Password: "wrong"})
		}(index)
	}
	group.Wait()
	authenticator.mu.Lock()
	attempt := authenticator.attempts["global"]
	authenticator.mu.Unlock()
	if attempt.Failures < 1 || attempt.Failures > maxLoginAttempts || attempt.InFlight != 0 {
		t.Fatalf("parallel admission state = %+v", attempt)
	}
	clock = clock.Add(loginWindow)
	_, _ = authenticator.Login(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil), LoginRequest{Username: "after-window", Password: "wrong"})
	authenticator.mu.Lock()
	recovered := authenticator.attempts["global"]
	authenticator.mu.Unlock()
	if recovered.Failures != 1 || recovered.WindowStart != clock {
		t.Fatalf("recovered admission state = %+v, clock=%s", recovered, clock)
	}
}

func fixtureAuthenticatorWithClock(t *testing.T, clock *time.Time) *Authenticator {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := New(Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("fixture-session-secret-with-enough-diversity-123")),
		PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Now: func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func TestMachineConfigValidationAndConstantTimeTokenBoundary(t *testing.T) {
	if err := (MachineConfig{}).Validate(); err != nil {
		t.Fatalf("disabled machine configuration rejected: %v", err)
	}
	if err := (MachineConfig{PortfolioRead: true}).Validate(); err == nil {
		t.Fatal("grant-only machine configuration was accepted")
	}
	config := MachineConfig{PrincipalID: "machine-reader", PortfolioID: "portfolio-api", BearerSecret: "machine-secret-with-enough-diversity-0123456789-ABCDEF", PortfolioRead: true}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if !config.MachineTokenValid(config.BearerSecret) || config.MachineTokenValid(config.BearerSecret+"x") {
		t.Fatal("machine token boundary was not enforced")
	}
	for _, invalid := range []MachineConfig{
		{PrincipalID: "machine", PortfolioID: "portfolio", BearerSecret: "short"},
		{PrincipalID: "machine", PortfolioID: "portfolio", BearerSecret: "ssssssssssssssssssssssssssssssss"},
		{PrincipalID: "machine", PortfolioID: "portfolio", BearerSecret: "machine-secret-with-enough-diversity-0123456789\n-ABCDEF"},
		{PrincipalID: "machine/escape", PortfolioID: "portfolio", BearerSecret: config.BearerSecret},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid machine configuration accepted: %s", invalid)
		}
	}
}

func TestMachineBearerGrammarAndRedaction(t *testing.T) {
	secret := "machine-secret-with-enough-diversity-0123456789-ABCDEF"
	config := MachineConfig{PrincipalID: "machine-reader", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formatted := fmt.Sprintf(format, config)
		if strings.Contains(formatted, secret) || !strings.Contains(formatted, "<redacted>") {
			t.Fatalf("format %s exposed machine secret: %s", format, formatted)
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "bearer_secret") {
		t.Fatalf("JSON exposed machine secret: %s", encoded)
	}

	for _, value := range []string{
		"Bearer " + strings.Repeat("x", MachineSecretMin-1),
		"Bearer " + strings.Repeat("x", MachineSecretMax+1),
		"Bearer " + secret + " ",
		"bearer " + secret,
		"Bearer " + secret[:len(secret)-1] + "	X",
		"Bearer " + secret + "é",
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", value)
		if _, present, wellFormed := BearerToken(request); !present || wellFormed {
			t.Fatalf("BearerToken(%q) accepted malformed value", value)
		}
	}
	multiple := httptest.NewRequest(http.MethodGet, "/", nil)
	multiple.Header.Add("Authorization", "Bearer "+secret)
	multiple.Header.Add("Authorization", "Bearer "+secret)
	if _, present, wellFormed := BearerToken(multiple); !present || wellFormed {
		t.Fatal("multiple Authorization values were accepted")
	}
	if !config.MachineTokenValid(secret) || config.MachineTokenValid(strings.Repeat("x", MachineSecretMax+1)) {
		t.Fatal("bounded token validation failed")
	}
}

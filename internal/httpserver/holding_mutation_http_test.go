package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestMachineCanCreateAndReplayHoldingThroughMutationRoute(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	secret := "holding-machine-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("holding-fixture-session-secret-with-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
		Machine: auth.MachineConfig{PrincipalID: "holding-machine", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, HoldingsWrite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"12.5000","unit_cost":"10.00","cost_basis":"125.0000","currency":"USD"}`
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "machine-create-key-1")
		r.Header.Set("If-Match", `"portfolio-api-0"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	first := request()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := request()
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() {
		t.Fatalf("replay status=%d body=%s first=%s", second.Code, second.Body.String(), first.Body.String())
	}
	if first.Header().Get("Location") == "" || first.Header().Get("ETag") != `"portfolio-api-1"` {
		t.Fatalf("headers location=%q etag=%q", first.Header().Get("Location"), first.Header().Get("ETag"))
	}
}

func TestBrowserHoldingMutationRequiresExactOriginAndSessionCSRF(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("browser-holding-session-secret-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("session body=%s", loginResponse.Body.String())
	}
	body := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"1.00","unit_cost":"2.00","cost_basis":"2.00","currency":"USD"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", strings.NewReader(body))
	request.AddCookie(loginResponse.Result().Cookies()[0])
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Idempotency-Key", "browser-create-key-1")
	request.Header.Set("If-Match", `"portfolio-api-0"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Idempotency-Key", "browser-create-key-2")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", response.Code, response.Body.String())
	}
}

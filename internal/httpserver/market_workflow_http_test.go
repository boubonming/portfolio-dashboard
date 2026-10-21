package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	appconfig "github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func marketHTTPFixture(t *testing.T) (http.Handler, *store.Store, *http.Cookie, string) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "market-http.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-01-01T00:00:00.000000000Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"portfolio-market-http", "Market HTTP", "MYR", now, now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"instrument-market-http", "HTTP", "HTTP fixture", "USD", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"portfolio-market-http", "instrument-market-http", now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, []any{"lot-market-http", "portfolio-market-http", "instrument-market-http", "1", "USD", now, now}},
	} {
		if _, err := database.DB.Exec(statement.query, statement.args...); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	configuration := appconfig.Config{ProviderKeys: appconfig.ProviderKeys{Finnhub: "server-only-secret"}, Auth: auth.Config{
		Username: "fixture-user", PasswordHash: string(passwordHash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("market-http-session-secret-with-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	}}
	handler, err := NewHandlerWithConfig(database, configuration)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		database.Close()
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var payload struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &payload); err != nil || payload.CSRFToken == "" {
		database.Close()
		t.Fatalf("login payload=%s err=%v", loginResponse.Body.String(), err)
	}
	return handler, database, loginResponse.Result().Cookies()[0], payload.CSRFToken
}

func marketRequest(handler http.Handler, cookie *http.Cookie, csrf, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.test"+target, strings.NewReader(body))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://example.test")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set(auth.CSRFHeaderName, csrf)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestMarketWorkflowHTTPAuthETagAndAtomicBatch(t *testing.T) {
	handler, database, cookie, csrf := marketHTTPFixture(t)
	defer database.Close()
	unauthenticated := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}
	setupResponse := marketRequest(handler, cookie, csrf, http.MethodGet, "/api/v1/portfolios/portfolio-market-http/market-data", "")
	if setupResponse.Code != http.StatusOK || setupResponse.Header().Get("ETag") == "" {
		t.Fatalf("setup status=%d etag=%q body=%s", setupResponse.Code, setupResponse.Header().Get("ETag"), setupResponse.Body.String())
	}
	var setup struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	batch := `{"mappings":[{"instrument_id":"instrument-market-http","provider":"finnhub","provider_symbol":"HTTP","quote_currency":"USD","provenance":"verified listing","confirm_unverified":true}]}`
	stale := marketRequest(handler, cookie, csrf, http.MethodPost, "/api/v1/portfolios/portfolio-market-http/market-data/mappings", batch)
	// The helper intentionally does not set If-Match; this must never be treated
	// as an implicit current revision.
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("missing If-Match status=%d body=%s", stale.Code, stale.Body.String())
	}
	validRequest := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/mappings", bytes.NewBufferString(batch))
	validRequest.AddCookie(cookie)
	validRequest.Header.Set("Origin", "https://example.test")
	validRequest.Header.Set("Content-Type", "application/json")
	validRequest.Header.Set(auth.CSRFHeaderName, csrf)
	validRequest.Header.Set("If-Match", setup.ETag)
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid mapping status=%d body=%s", validResponse.Code, validResponse.Body.String())
	}
	var count int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("mapping count=%d err=%v", count, err)
	}
}

func TestMarketWorkflowHTTPRejectsOriginCSRFAndIdempotencyConflict(t *testing.T) {
	handler, database, cookie, csrf := marketHTTPFixture(t)
	defer database.Close()
	batch := `{"mappings":[{"instrument_id":"instrument-market-http","provider":"finnhub","provider_symbol":"HTTP","quote_currency":"USD","provenance":"verified listing","confirm_unverified":true}]}`
	crossOriginRequest := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/mappings", strings.NewReader(batch))
	crossOriginRequest.AddCookie(cookie)
	crossOriginRequest.Header.Set("Origin", "https://evil.example")
	crossOriginRequest.Header.Set("Content-Type", "application/json")
	crossOriginRequest.Header.Set(auth.CSRFHeaderName, csrf)
	crossOrigin := httptest.NewRecorder()
	handler.ServeHTTP(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}
	missingCSRF := marketRequest(handler, cookie, "", http.MethodPost, "/api/v1/portfolios/portfolio-market-http/market-data/mappings", batch)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}
	refresh := func(currency string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/refresh", strings.NewReader(`{"reporting_currency":"`+currency+`"}`))
		request.AddCookie(cookie)
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.CSRFHeaderName, csrf)
		request.Header.Set("Idempotency-Key", "market-refresh-key-01")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	first := refresh("MYR")
	if first.Code != http.StatusOK {
		t.Fatalf("first refresh status=%d body=%s", first.Code, first.Body.String())
	}
	replay := refresh("MYR")
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay status=%d body=%s first=%s", replay.Code, replay.Body.String(), first.Body.String())
	}
	conflict := refresh("USD")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("fingerprint conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if strings.Contains(first.Body.String(), "server-only-secret") {
		t.Fatal("provider secret was serialized in refresh response")
	}
}

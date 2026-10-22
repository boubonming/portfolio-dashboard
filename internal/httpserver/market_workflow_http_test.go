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
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func marketHTTPFixture(t *testing.T) (http.Handler, *store.Store, *http.Cookie, string) {
	return marketHTTPFixtureWithClients(t, nil)
}

func marketHTTPFixtureWithClients(t *testing.T, clients map[marketdata.Provider]marketdata.ProviderClient) (http.Handler, *store.Store, *http.Cookie, string) {
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
	providerKeys := appconfig.ProviderKeys{Finnhub: "server-only-secret"}
	if clients != nil {
		providerKeys.OpenExchangeRates = "fixture-only-provider-key"
	}
	configuration := appconfig.Config{ProviderKeys: providerKeys, Auth: auth.Config{
		Username: "fixture-user", PasswordHash: string(passwordHash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("market-http-session-secret-with-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	}}
	var handler http.Handler
	if clients == nil {
		handler, err = NewHandlerWithConfig(database, configuration)
	} else {
		handler, err = NewHandlerWithConfigAndMarketClients(database, configuration, clients)
	}
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

type fakeMarketProvider struct {
	provider  marketdata.Provider
	quoteCall int
	fxCall    int
}

func (p *fakeMarketProvider) Name() marketdata.Provider { return p.provider }
func (p *fakeMarketProvider) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	p.quoteCall++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return domain.Quote{ID: "fake-quote-" + request.InstrumentID, InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "10", Currency: request.Currency, Source: p.provider.String(), MarketAt: now, FetchedAt: now, Basis: "fake fixture", Provenance: "test provider"}, nil
}
func (p *fakeMarketProvider) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	p.fxCall++
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return domain.FXRate{ID: "fake-fx-" + request.BaseCurrency + "-" + request.QuoteCurrency, BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "1.5", Source: p.provider.String(), MarketAt: now, FetchedAt: now, Basis: "fake fixture", Provenance: "test provider"}, nil
}

type countingDiscoveryProvider struct {
	fakeMarketProvider
	searchCalls int
}

func (p *countingDiscoveryProvider) Search(context.Context, string) ([]marketdata.DiscoveryCandidate, error) {
	p.searchCalls++
	return []marketdata.DiscoveryCandidate{{Identifier: "fixture-id", Symbol: "HTTP", Name: "Fixture"}}, nil
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
	if !strings.Contains(setupResponse.Body.String(), `"mappings":[]`) || strings.Contains(setupResponse.Body.String(), `"mappings":null`) {
		t.Fatalf("setup mappings were not encoded as an empty array: %s", setupResponse.Body.String())
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

func TestMarketDiscoveryRejectsInvalidEmptyObjectContractBeforeProviderOrStoreWork(t *testing.T) {
	coingecko := &countingDiscoveryProvider{fakeMarketProvider: fakeMarketProvider{provider: marketdata.ProviderCoinGecko}}
	finnhub := &countingDiscoveryProvider{fakeMarketProvider: fakeMarketProvider{provider: marketdata.ProviderFinnhub}}
	handler, database, cookie, csrf := marketHTTPFixtureWithClients(t, map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderCoinGecko: coingecko,
		marketdata.ProviderFinnhub:   finnhub,
	})
	defer database.Close()
	var before int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `null`, `[]`, `1`, `{"unexpected":1}`, `{} {}`, `{"unexpected":1,"unexpected":2}`} {
		response := marketRequest(handler, cookie, csrf, http.MethodPost, "/api/v1/portfolios/portfolio-market-http/market-data/discover", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d response=%s", body, response.Code, response.Body.String())
		}
		var after int
		if err := database.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("invalid body %q changed mapping count from %d to %d", body, before, after)
		}
	}
	if coingecko.searchCalls != 0 || finnhub.searchCalls != 0 {
		t.Fatalf("invalid bodies reached providers: coingecko=%d finnhub=%d", coingecko.searchCalls, finnhub.searchCalls)
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
	var actor string
	if err := database.DB.QueryRow(`SELECT actor_id FROM market_refresh_idempotency WHERE idempotency_key=?`, "market-refresh-key-01").Scan(&actor); err != nil || actor != "fixture-user" {
		t.Fatalf("refresh actor=%q err=%v", actor, err)
	}
}

func TestMarketWorkflowHTTPRejectsEmptyRequiredRefreshBody(t *testing.T) {
	handler, database, cookie, csrf := marketHTTPFixture(t)
	defer database.Close()
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/refresh", strings.NewReader(""))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.CSRFHeaderName, csrf)
	request.Header.Set("Idempotency-Key", "empty-body-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_refresh_request") {
		t.Fatalf("empty body status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMarketWorkflowHTTPRejectsNullAndNonObjectRefreshBodiesWithoutEffects(t *testing.T) {
	handler, database, cookie, csrf := marketHTTPFixture(t)
	defer database.Close()
	for index, body := range []string{`null`, `[]`, `true`, `42`, `{"unknown":"field"}`, `{"reporting_currency":"MYR"} {}`} {
		request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/refresh", strings.NewReader(body))
		request.AddCookie(cookie)
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.CSRFHeaderName, csrf)
		request.Header.Set("Idempotency-Key", "invalid-body-key-"+string(rune('a'+index)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_refresh_request") {
			t.Fatalf("body=%q status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	var receipts, observations, snapshots int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_idempotency`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM quotes`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || observations != 0 || snapshots != 0 {
		t.Fatalf("invalid bodies caused effects: receipts=%d observations=%d snapshots=%d", receipts, observations, snapshots)
	}
}

func TestMarketWorkflowHTTPFakeProviderEndToEndAndRestartReplay(t *testing.T) {
	quoteProvider := &fakeMarketProvider{provider: marketdata.ProviderCoinGecko}
	fxProvider := &fakeMarketProvider{provider: marketdata.ProviderOpenExchangeRates}
	handler, database, cookie, csrf := marketHTTPFixtureWithClients(t, map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderCoinGecko: quoteProvider, marketdata.ProviderOpenExchangeRates: fxProvider})
	var setup struct {
		ETag string `json:"etag"`
	}
	setupResponse := marketRequest(handler, cookie, csrf, http.MethodGet, "/api/v1/portfolios/portfolio-market-http/market-data", "")
	if setupResponse.Code != http.StatusOK || json.Unmarshal(setupResponse.Body.Bytes(), &setup) != nil || setup.ETag == "" {
		database.Close()
		t.Fatalf("setup status=%d body=%s", setupResponse.Code, setupResponse.Body.String())
	}
	batch := `{"mappings":[{"instrument_id":"instrument-market-http","provider":"coingecko","provider_symbol":"fixture-coin","quote_currency":"USD","provenance":"fixture listing","confirm_unverified":true}]}`
	mappingRequest := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/mappings", strings.NewReader(batch))
	mappingRequest.AddCookie(cookie)
	mappingRequest.Header.Set("Origin", "https://example.test")
	mappingRequest.Header.Set("Content-Type", "application/json")
	mappingRequest.Header.Set(auth.CSRFHeaderName, csrf)
	mappingRequest.Header.Set("If-Match", setup.ETag)
	mappingResponse := httptest.NewRecorder()
	handler.ServeHTTP(mappingResponse, mappingRequest)
	if mappingResponse.Code != http.StatusOK {
		database.Close()
		t.Fatalf("mapping status=%d body=%s", mappingResponse.Code, mappingResponse.Body.String())
	}
	refresh := func(current http.Handler, currentCookie *http.Cookie, currentCSRF string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/portfolios/portfolio-market-http/market-data/refresh", strings.NewReader(`{"reporting_currency":"MYR"}`))
		request.AddCookie(currentCookie)
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(auth.CSRFHeaderName, currentCSRF)
		request.Header.Set("Idempotency-Key", "fake-provider-restart-key")
		response := httptest.NewRecorder()
		current.ServeHTTP(response, request)
		return response
	}
	first := refresh(handler, cookie, csrf)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"complete":true`) || quoteProvider.quoteCall != 1 || fxProvider.fxCall != 1 {
		database.Close()
		t.Fatalf("first refresh status=%d calls quote=%d fx=%d body=%s", first.Code, quoteProvider.quoteCall, fxProvider.fxCall, first.Body.String())
	}
	var databasePath string
	if err := database.DB.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &databasePath); err != nil || databasePath == "" {
		database.Close()
		t.Fatalf("database path err=%v path=%q", err, databasePath)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	configuration := appconfig.Config{ProviderKeys: appconfig.ProviderKeys{OpenExchangeRates: "fixture-only-provider-key"}, Auth: auth.Config{Username: "fixture-user", PasswordHash: string(passwordHash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("market-http-session-secret-with-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour}}
	restartedHandler, err := NewHandlerWithConfigAndMarketClients(restarted, configuration, map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderCoinGecko: quoteProvider, marketdata.ProviderOpenExchangeRates: fxProvider})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	restartedHandler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("restart login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginPayload struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginPayload); err != nil || loginPayload.CSRFToken == "" {
		t.Fatalf("restart login payload=%s err=%v", loginResponse.Body.String(), err)
	}
	replay := refresh(restartedHandler, loginResponse.Result().Cookies()[0], loginPayload.CSRFToken)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() || quoteProvider.quoteCall != 1 || fxProvider.fxCall != 1 {
		t.Fatalf("restart replay status=%d calls quote=%d fx=%d body=%s first=%s", replay.Code, quoteProvider.quoteCall, fxProvider.fxCall, replay.Body.String(), first.Body.String())
	}
}

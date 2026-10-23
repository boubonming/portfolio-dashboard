package httpserver

import (
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
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func marketMachineSecretValue() string {
	return "market-machine-" + strings.Repeat("0123456789", 4) + "-ABCdef"
}

func marketMachineFixture(t *testing.T, clients map[marketdata.Provider]marketdata.ProviderClient) (http.Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "market-machine.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-01-01T00:00:00.000000000Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"market-machine-portfolio", "Machine Market", "MYR", now, now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-machine-instrument", "MACHINE", "Machine fixture", "USD", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-machine-portfolio", "market-machine-instrument", now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-machine-lot", "market-machine-portfolio", "market-machine-instrument", "1", "USD", "active", now, now}},
	} {
		if _, err := database.DB.Exec(statement.query, statement.args...); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	configuration := appconfig.Config{
		ProviderKeys: appconfig.ProviderKeys{Finnhub: "fixture-provider-key", OpenExchangeRates: "fixture-provider-key"},
		Auth: auth.Config{
			Username: "fixture-user", PasswordHash: string(hash),
			SessionSecret: base64.StdEncoding.EncodeToString([]byte("market-machine-session-secret-with-diversity-123")),
			PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
			Machine: auth.MachineConfig{PrincipalID: "market-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true, HoldingsWrite: true},
		},
	}
	handler, err := NewHandlerWithConfigAndMarketClients(database, configuration, clients)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.GrantMachineHoldings(context.Background(), "market-machine", "market-machine-portfolio"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return handler, database
}

func machineMarketRequest(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+marketMachineSecretValue())
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestMachineBearerCanUseScopedMarketWorkflowAndPreservesActor(t *testing.T) {
	coingecko := &fakeMarketProvider{provider: marketdata.ProviderCoinGecko}
	fx := &fakeMarketProvider{provider: marketdata.ProviderOpenExchangeRates}
	handler, database := marketMachineFixture(t, map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderCoinGecko:         coingecko,
		marketdata.ProviderOpenExchangeRates: fx,
	})
	defer database.Close()

	setupResponse := machineMarketRequest(handler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data", "", nil)
	if setupResponse.Code != http.StatusOK || setupResponse.Header().Get("ETag") == "" {
		t.Fatalf("setup status=%d etag=%q body=%s", setupResponse.Code, setupResponse.Header().Get("ETag"), setupResponse.Body.String())
	}
	var setup struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}

	discovery := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/discover", `{}`, map[string]string{"If-Match": setup.ETag})
	if discovery.Code != http.StatusOK {
		t.Fatalf("discovery status=%d body=%s", discovery.Code, discovery.Body.String())
	}

	batch := `{"mappings":[{"instrument_id":"market-machine-instrument","provider":"coingecko","provider_symbol":"machine-coin","quote_currency":"USD","provenance":"machine verified listing","confirm_unverified":true}]}`
	mapping := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/mappings", batch, map[string]string{"If-Match": setup.ETag})
	if mapping.Code != http.StatusOK {
		t.Fatalf("mapping status=%d body=%s", mapping.Code, mapping.Body.String())
	}
	var mappingActor string
	if err := database.DB.QueryRow(`SELECT actor FROM audit_events WHERE action='market.mapping.batch_saved'`).Scan(&mappingActor); err != nil {
		t.Fatal(err)
	}
	if mappingActor != "market-machine" {
		t.Fatalf("mapping actor=%q", mappingActor)
	}

	refresh := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, map[string]string{"Idempotency-Key": "machine-market-refresh-01"})
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var actor, portfolio string
	if err := database.DB.QueryRow(`SELECT actor_id,portfolio_id FROM market_refresh_idempotency WHERE idempotency_key=?`, "machine-market-refresh-01").Scan(&actor, &portfolio); err != nil {
		t.Fatal(err)
	}
	if actor != "market-machine" || portfolio != "market-machine-portfolio" {
		t.Fatalf("refresh binding actor=%q portfolio=%q", actor, portfolio)
	}
}

func TestMachineMarketWorkflowFailsClosedForCSRFAndRevokedGrant(t *testing.T) {
	handler, database := marketMachineFixture(t, nil)
	defer database.Close()
	withCSRF := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/discover", `{}`, map[string]string{"If-Match": `"stale"`, auth.CSRFHeaderName: "unexpected"})
	if withCSRF.Code != http.StatusForbidden {
		t.Fatalf("machine CSRF status=%d body=%s", withCSRF.Code, withCSRF.Body.String())
	}
	if _, err := database.RevokeMachineHoldings(context.Background(), "market-machine", "market-machine-portfolio"); err != nil {
		t.Fatal(err)
	}
	setup := machineMarketRequest(handler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data", "", nil)
	if setup.Code != http.StatusForbidden {
		t.Fatalf("revoked grant status=%d body=%s", setup.Code, setup.Body.String())
	}
}

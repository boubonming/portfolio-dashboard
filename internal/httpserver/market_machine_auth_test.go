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
	return marketMachineFixtureWithMachine(t, clients, auth.MachineConfig{
		PrincipalID: "market-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true, HoldingsWrite: true,
	}, true)
}

func marketMachineFixtureWithMachine(t *testing.T, clients map[marketdata.Provider]marketdata.ProviderClient, machine auth.MachineConfig, grant bool) (http.Handler, *store.Store) {
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
			Machine: machine,
		},
	}
	handler, err := NewHandlerWithConfigAndMarketClients(database, configuration, clients)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if grant {
		if _, err := database.GrantMachineHoldings(context.Background(), machine.PrincipalID, machine.PortfolioID); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	return handler, database
}

func marketMachineEffects(t *testing.T, database *store.Store) [7]int {
	t.Helper()
	var effects [7]int
	for index, table := range []string{"instrument_provider_mappings", "snapshots", "audit_events", "activity_events", "market_refresh_idempotency", "quotes", "fx_rates"} {
		if err := database.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&effects[index]); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
	}
	return effects
}

func marketMachineProviderCalls(clients map[marketdata.Provider]marketdata.ProviderClient) int {
	total := 0
	for _, client := range clients {
		switch provider := client.(type) {
		case *fakeMarketProvider:
			total += provider.quoteCall + provider.fxCall
		case *countingDiscoveryProvider:
			total += provider.quoteCall + provider.fxCall + provider.searchCalls
		}
	}
	return total
}

func marketMachineRequestWithAuthorization(handler http.Handler, method, path, body string, authorization []string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	for _, value := range authorization {
		request.Header.Add("Authorization", value)
	}
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

func marketMachineWorkflowRequests(handler http.Handler, setupETag string, authorization []string) []func() *httptest.ResponseRecorder {
	return []func() *httptest.ResponseRecorder{
		func() *httptest.ResponseRecorder {
			return marketMachineRequestWithAuthorization(handler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data", "", authorization, nil)
		},
		func() *httptest.ResponseRecorder {
			return marketMachineRequestWithAuthorization(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/discover", `{}`, authorization, map[string]string{"If-Match": setupETag})
		},
		func() *httptest.ResponseRecorder {
			return marketMachineRequestWithAuthorization(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/mappings", `{"mappings":[{"instrument_id":"market-machine-instrument","provider":"coingecko","provider_symbol":"machine-coin","quote_currency":"USD","provenance":"fixture listing","confirm_unverified":true}]}`, authorization, map[string]string{"If-Match": setupETag})
		},
		func() *httptest.ResponseRecorder {
			return marketMachineRequestWithAuthorization(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, authorization, map[string]string{"Idempotency-Key": "machine-auth-matrix-key"})
		},
	}
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

func TestMachineMarketRefreshUsesPortfolioReadWithoutDedicatedGrant(t *testing.T) {
	clients := map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderCoinGecko:         &fakeMarketProvider{provider: marketdata.ProviderCoinGecko},
		marketdata.ProviderOpenExchangeRates: &fakeMarketProvider{provider: marketdata.ProviderOpenExchangeRates},
	}
	refreshOnly, refreshDB := marketMachineFixtureWithMachine(t, clients, auth.MachineConfig{
		PrincipalID: "market-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true,
	}, false)
	defer refreshDB.Close()
	response := machineMarketRequest(refreshOnly, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, map[string]string{"Idempotency-Key": "refresh-only-key"})
	if response.Code != http.StatusOK {
		t.Fatalf("portfolio-read refresh status=%d body=%s", response.Code, response.Body.String())
	}
	var permission int
	if err := refreshDB.DB.QueryRow(`SELECT permission_market_data_refresh FROM market_refresh_idempotency WHERE idempotency_key=?`, "refresh-only-key").Scan(&permission); err != nil {
		t.Fatal(err)
	}
	if permission != 0 {
		t.Fatalf("refresh receipt legacy permission=%d, want inert 0", permission)
	}
	var payload string
	if err := refreshDB.DB.QueryRow(`SELECT payload FROM audit_events WHERE action='market.refresh.completed'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"portfolio:read"`) || strings.Contains(payload, `"market_data:refresh"`) {
		t.Fatalf("refresh audit permissions=%s", payload)
	}
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

func TestMachineMarketRuntimeFlagsIndependentlyFailClosedOnEveryRoute(t *testing.T) {
	for _, tc := range []struct {
		name          string
		portfolioRead bool
		holdingsWrite bool
	}{
		{name: "portfolio read disabled", portfolioRead: false, holdingsWrite: true},
		{name: "holdings write disabled", portfolioRead: true, holdingsWrite: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clients := map[marketdata.Provider]marketdata.ProviderClient{
				marketdata.ProviderCoinGecko:         &countingDiscoveryProvider{fakeMarketProvider: fakeMarketProvider{provider: marketdata.ProviderCoinGecko}},
				marketdata.ProviderFinnhub:           &countingDiscoveryProvider{fakeMarketProvider: fakeMarketProvider{provider: marketdata.ProviderFinnhub}},
				marketdata.ProviderOpenExchangeRates: &fakeMarketProvider{provider: marketdata.ProviderOpenExchangeRates},
			}
			handler, database := marketMachineFixtureWithMachine(t, clients, auth.MachineConfig{PrincipalID: "market-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: tc.portfolioRead, HoldingsWrite: tc.holdingsWrite}, false)
			defer database.Close()
			before := marketMachineEffects(t, database)
			requests := marketMachineWorkflowRequests(handler, `"not-used"`, []string{"Bearer " + marketMachineSecretValue()})
			requestNames := []string{"setup", "discovery", "mapping", "refresh"}
			for index, request := range requests {
				wantStatus := http.StatusForbidden
				if tc.portfolioRead && !tc.holdingsWrite && index == len(requests)-1 {
					wantStatus = http.StatusOK
				}
				var beforeDeniedEffects [7]int
				var beforeDeniedProviderCalls int
				deniedWorkflowRequest := tc.portfolioRead && !tc.holdingsWrite && index < 3
				if deniedWorkflowRequest {
					beforeDeniedEffects = marketMachineEffects(t, database)
					beforeDeniedProviderCalls = marketMachineProviderCalls(clients)
				}
				response := request()
				if response.Code != wantStatus {
					t.Fatalf("%s request status=%d body=%s", requestNames[index], response.Code, response.Body.String())
				}
				if deniedWorkflowRequest {
					if got := marketMachineEffects(t, database); got != beforeDeniedEffects {
						t.Fatalf("%s request in %q caused effects: before=%v after=%v", requestNames[index], tc.name, beforeDeniedEffects, got)
					}
					if got := marketMachineProviderCalls(clients); got != beforeDeniedProviderCalls {
						t.Fatalf("%s request in %q reached providers: before=%d after=%d", requestNames[index], tc.name, beforeDeniedProviderCalls, got)
					}
				}
			}
			if !tc.portfolioRead {
				got := marketMachineEffects(t, database)
				if got != before {
					t.Fatalf("runtime-disabled request caused effects: before=%v after=%v", before, got)
				}
			}
			if !tc.portfolioRead {
				got := marketMachineProviderCalls(clients)
				if got != 0 {
					t.Fatalf("runtime-disabled request reached providers: %d calls", got)
				}
			}
		})
	}
}

func TestMachineMarketAuthorizationMatrixRejectsBindingsAndDuplicateCredentials(t *testing.T) {
	clients := map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderCoinGecko: &countingDiscoveryProvider{fakeMarketProvider: fakeMarketProvider{provider: marketdata.ProviderCoinGecko}},
	}
	handler, database := marketMachineFixture(t, clients)
	defer database.Close()
	valid := []string{"Bearer " + marketMachineSecretValue()}
	tests := []struct {
		name          string
		authorization []string
		headers       map[string]string
		path          string
		status        int
	}{
		{name: "empty bearer", authorization: []string{"Bearer "}, status: http.StatusUnauthorized},
		{name: "malformed bearer", authorization: []string{"Basic " + marketMachineSecretValue()}, status: http.StatusUnauthorized},
		{name: "oversized bearer", authorization: []string{"Bearer " + strings.Repeat("x", auth.MachineSecretMax+1)}, status: http.StatusUnauthorized},
		{name: "valid then invalid duplicate", authorization: []string{"Bearer " + marketMachineSecretValue(), "Bearer wrong"}, status: http.StatusUnauthorized},
		{name: "invalid then valid duplicate", authorization: []string{"Bearer wrong", "Bearer " + marketMachineSecretValue()}, status: http.StatusUnauthorized},
		{name: "mixed session and bearer", authorization: []string{"Bearer " + marketMachineSecretValue()}, headers: map[string]string{"Cookie": auth.SessionCookieName + "=malformed-session"}, status: http.StatusUnauthorized},
		{name: "cross portfolio", authorization: []string{"Bearer " + marketMachineSecretValue()}, path: "/api/v1/portfolios/another-portfolio/market-data/refresh", status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = "/api/v1/portfolios/market-machine-portfolio/market-data/refresh"
			}
			before := marketMachineEffects(t, database)
			beforeCalls := marketMachineProviderCalls(clients)
			headers := map[string]string{"Idempotency-Key": "rejected-auth-matrix-key"}
			for key, value := range tc.headers {
				headers[key] = value
			}
			response := marketMachineRequestWithAuthorization(handler, http.MethodPost, path, `{"reporting_currency":"MYR"}`, tc.authorization, headers)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if got := marketMachineEffects(t, database); got != before {
				t.Fatalf("rejected credentials caused effects: before=%v after=%v", before, got)
			}
			if got := marketMachineProviderCalls(clients); got != beforeCalls {
				t.Fatalf("rejected credentials reached providers: before=%d after=%d", beforeCalls, got)
			}
		})
	}

	wrongPrincipalHandler, wrongPrincipalDB := marketMachineFixtureWithMachine(t, clients, auth.MachineConfig{PrincipalID: "wrong-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true, HoldingsWrite: true}, false)
	defer wrongPrincipalDB.Close()
	if _, err := wrongPrincipalDB.DB.Exec(`UPDATE machine_principals SET active=0 WHERE id=?`, "wrong-machine"); err != nil {
		t.Fatal(err)
	}
	wrongPrincipalBefore := marketMachineEffects(t, wrongPrincipalDB)
	wrongPrincipalCalls := marketMachineProviderCalls(clients)
	response := marketMachineRequestWithAuthorization(wrongPrincipalHandler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, valid, map[string]string{"Idempotency-Key": "wrong-principal-key"})
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong configured principal status=%d body=%s", response.Code, response.Body.String())
	}
	if got := marketMachineEffects(t, wrongPrincipalDB); got != wrongPrincipalBefore {
		t.Fatalf("wrong configured principal caused effects: before=%v after=%v", wrongPrincipalBefore, got)
	}
	if got := marketMachineProviderCalls(clients); got != wrongPrincipalCalls {
		t.Fatalf("wrong configured principal reached providers: before=%d after=%d", wrongPrincipalCalls, got)
	}

	wrongPortfolioHandler, wrongPortfolioDB := marketMachineFixtureWithMachine(t, clients, auth.MachineConfig{PrincipalID: "market-machine", PortfolioID: "wrong-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true, HoldingsWrite: true}, false)
	defer wrongPortfolioDB.Close()
	wrongPortfolioBefore := marketMachineEffects(t, wrongPortfolioDB)
	wrongPortfolioCalls := marketMachineProviderCalls(clients)
	response = marketMachineRequestWithAuthorization(wrongPortfolioHandler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, valid, map[string]string{"Idempotency-Key": "wrong-portfolio-key"})
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong configured portfolio status=%d body=%s", response.Code, response.Body.String())
	}
	if got := marketMachineEffects(t, wrongPortfolioDB); got != wrongPortfolioBefore {
		t.Fatalf("wrong configured portfolio caused effects: before=%v after=%v", wrongPortfolioBefore, got)
	}
	if got := marketMachineProviderCalls(clients); got != wrongPortfolioCalls {
		t.Fatalf("wrong configured portfolio reached providers: before=%d after=%d", wrongPortfolioCalls, got)
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

func TestMachineMarketRoutePersistsExactActorCategoriesIDsAndReplayEquivalence(t *testing.T) {
	coingecko := &fakeMarketProvider{provider: marketdata.ProviderCoinGecko}
	fx := &fakeMarketProvider{provider: marketdata.ProviderOpenExchangeRates}
	clients := map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderCoinGecko: coingecko, marketdata.ProviderOpenExchangeRates: fx}
	handler, database := marketMachineFixture(t, clients)
	defer database.Close()

	setup := machineMarketRequest(handler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data", "", nil)
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	var setupPayload struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &setupPayload); err != nil || setupPayload.ETag == "" {
		t.Fatalf("setup payload=%s err=%v", setup.Body.String(), err)
	}
	batch := `{"mappings":[{"instrument_id":"market-machine-instrument","provider":"coingecko","provider_symbol":"machine-coin","quote_currency":"USD","provenance":"machine verified listing","confirm_unverified":true}]}`
	mapping := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/mappings", batch, map[string]string{"If-Match": setupPayload.ETag})
	if mapping.Code != http.StatusOK {
		t.Fatalf("mapping status=%d body=%s", mapping.Code, mapping.Body.String())
	}
	var mappingPayload struct {
		Outcomes []struct {
			Mapping struct {
				ID string `json:"id"`
			} `json:"mapping"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(mapping.Body.Bytes(), &mappingPayload); err != nil || len(mappingPayload.Outcomes) != 1 || mappingPayload.Outcomes[0].Mapping.ID == "" {
		t.Fatalf("mapping payload=%s err=%v", mapping.Body.String(), err)
	}
	mappingID := mappingPayload.Outcomes[0].Mapping.ID
	var auditActor, auditAction, auditType, auditEntity string
	if err := database.DB.QueryRow(`SELECT actor,action,entity_type,entity_id FROM audit_events WHERE action='market.mapping.batch_saved' AND entity_id=?`, mappingID).Scan(&auditActor, &auditAction, &auditType, &auditEntity); err != nil {
		t.Fatal(err)
	}
	if auditActor != "market-machine" || auditAction != "market.mapping.batch_saved" || auditType != "provider_mapping" || auditEntity != mappingID {
		t.Fatalf("mapping audit actor=%q action=%q type=%q entity=%q", auditActor, auditAction, auditType, auditEntity)
	}
	var activityActor, activityAction, activityCategory, activityType, activityEntity, activityAudit string
	if err := database.DB.QueryRow(`SELECT actor_category,action,category,entity_type,entity_id,source_audit_event_id FROM activity_events WHERE action='market.mapping.batch_saved' AND entity_id=?`, mappingID).Scan(&activityActor, &activityAction, &activityCategory, &activityType, &activityEntity, &activityAudit); err != nil {
		t.Fatal(err)
	}
	if activityActor != "machine" || activityAction != "market.mapping.batch_saved" || activityCategory != "market" || activityType != "provider_mapping" || activityEntity != mappingID || activityAudit == "" {
		t.Fatalf("mapping activity actor=%q action=%q category=%q type=%q entity=%q audit=%q", activityActor, activityAction, activityCategory, activityType, activityEntity, activityAudit)
	}

	refresh := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, map[string]string{"Idempotency-Key": "machine-replay-key"})
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refresh.Code, refresh.Body.String())
	}
	var refreshPayload struct {
		Refresh struct {
			RefreshID string `json:"refresh_id"`
		} `json:"refresh"`
		Snapshot struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(refresh.Body.Bytes(), &refreshPayload); err != nil || refreshPayload.Refresh.RefreshID == "" || refreshPayload.Snapshot.SnapshotID == "" {
		t.Fatalf("refresh payload=%s err=%v", refresh.Body.String(), err)
	}
	refreshID, snapshotID := refreshPayload.Refresh.RefreshID, refreshPayload.Snapshot.SnapshotID
	for _, tc := range []struct{ name, entity, action, wantType string }{
		{"refresh audit", refreshID, "market.refresh.completed", "refresh_run"},
		{"snapshot audit", snapshotID, "portfolio.snapshot.created", "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actor, action, entityType, entityID string
			if err := database.DB.QueryRow(`SELECT actor,action,entity_type,entity_id FROM audit_events WHERE action=? AND entity_id=?`, tc.action, tc.entity).Scan(&actor, &action, &entityType, &entityID); err != nil {
				t.Fatal(err)
			}
			if actor != "market-machine" || action != tc.action || entityType != tc.wantType || entityID != tc.entity {
				t.Fatalf("actor=%q action=%q type=%q entity=%q", actor, action, entityType, entityID)
			}
		})
	}
	for _, tc := range []struct{ name, entity, action, wantCategory, wantType string }{
		{"refresh activity", refreshID, "market.refresh.completed", "market", "refresh_run"},
		{"snapshot activity", snapshotID, "portfolio.snapshot.created", "portfolio", "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var actor, action, category, entityType, entityID, sourceAudit string
			if err := database.DB.QueryRow(`SELECT actor_category,action,category,entity_type,entity_id,source_audit_event_id FROM activity_events WHERE action=? AND entity_id=?`, tc.action, tc.entity).Scan(&actor, &action, &category, &entityType, &entityID, &sourceAudit); err != nil {
				t.Fatal(err)
			}
			if actor != "machine" || action != tc.action || category != tc.wantCategory || entityType != tc.wantType || entityID != tc.entity || sourceAudit == "" {
				t.Fatalf("actor=%q action=%q category=%q type=%q entity=%q audit=%q", actor, action, category, entityType, entityID, sourceAudit)
			}
		})
	}
	var actorCategory, actorID, portfolioID, state, storedRefreshID, storedSnapshotID, auditID, activityID string
	if err := database.DB.QueryRow(`SELECT actor_category,actor_id,portfolio_id,state,refresh_id,snapshot_id,audit_event_id,activity_event_id FROM market_refresh_idempotency WHERE idempotency_key=?`, "machine-replay-key").Scan(&actorCategory, &actorID, &portfolioID, &state, &storedRefreshID, &storedSnapshotID, &auditID, &activityID); err != nil {
		t.Fatal(err)
	}
	if actorCategory != "machine" || actorID != "market-machine" || portfolioID != "market-machine-portfolio" || state != "committed" || storedRefreshID != refreshID || storedSnapshotID != snapshotID || auditID == "" || activityID == "" {
		t.Fatalf("idempotency actor=%q id=%q portfolio=%q state=%q refresh=%q snapshot=%q audit=%q activity=%q", actorCategory, actorID, portfolioID, state, storedRefreshID, storedSnapshotID, auditID, activityID)
	}

	beforeReplay := marketMachineEffects(t, database)
	quoteCalls, fxCalls := coingecko.quoteCall, fx.fxCall
	replay := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/refresh", `{"reporting_currency":"MYR"}`, map[string]string{"Idempotency-Key": "machine-replay-key"})
	if replay.Code != http.StatusOK || replay.Body.String() != refresh.Body.String() {
		t.Fatalf("replay status=%d body=%s first=%s", replay.Code, replay.Body.String(), refresh.Body.String())
	}
	if got := marketMachineEffects(t, database); got != beforeReplay {
		t.Fatalf("replay changed effect set: before=%v after=%v", beforeReplay, got)
	}
	if coingecko.quoteCall != quoteCalls || fx.fxCall != fxCalls {
		t.Fatalf("replay repeated provider effects: quote=%d/%d fx=%d/%d", coingecko.quoteCall, quoteCalls, fx.fxCall, fxCalls)
	}
}

func TestMachineMarketAuthorizedUnavailableProviderDiscoveryDoesNotWrite(t *testing.T) {
	handler, database := marketMachineFixture(t, nil)
	defer database.Close()
	setup := machineMarketRequest(handler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data", "", nil)
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	var setupPayload struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &setupPayload); err != nil {
		t.Fatal(err)
	}
	before := marketMachineEffects(t, database)
	discovery := machineMarketRequest(handler, http.MethodPost, "/api/v1/portfolios/market-machine-portfolio/market-data/discover", `{}`, map[string]string{"If-Match": setupPayload.ETag})
	if discovery.Code != http.StatusOK || !strings.Contains(discovery.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("unavailable discovery status=%d body=%s", discovery.Code, discovery.Body.String())
	}
	if got := marketMachineEffects(t, database); got != before {
		t.Fatalf("unavailable provider caused effects: before=%v after=%v", before, got)
	}
}

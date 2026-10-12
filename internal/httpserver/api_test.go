package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func apiFixture(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-api", "API Fixture", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-api", "FIX", "Fixture instrument", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,source_row,source_ref,source_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "lot-api", "portfolio-api", "instrument-api", "0.125", "1.20", "0.15", "USD", 1, "fixture#row=1", "fixture-hash", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddQuote(context.Background(), store.QuoteInput{InstrumentID: "instrument-api", Price: "123.4500", Currency: "USD", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddFXRate(context.Background(), store.FXInput{BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.5", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	return database
}

func authenticatedAPIHandler(t *testing.T, database *store.Store) http.Handler {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("api-fixture-session-secret-with-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"username": "fixture-user", "password": "fixture-only-value"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", bytes.NewReader(payload))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("fixture login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		handler.ServeHTTP(w, r)
	})
}

func TestPortfolioOverviewAPIIsScopedAndKeepsDecimalsAsStrings(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	when := time.Now().UTC()
	if _, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "api.test.v1", AsOf: when, MaxAge: 2 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	handler := authenticatedAPIHandler(t, database)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview?currency=MYR", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		ReportingCurrency string `json:"reporting_currency"`
		Snapshot          struct {
			State          string `json:"state"`
			ReportingTotal string `json:"reporting_total"`
		} `json:"snapshot"`
		Holdings []struct {
			Quantity       string  `json:"quantity"`
			LatestPrice    *string `json:"latest_price"`
			ReportingValue *string `json:"reporting_value"`
		} `json:"holdings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReportingCurrency != "MYR" || body.Snapshot.State != "complete" || body.Snapshot.ReportingTotal != "69.440625" {
		t.Fatalf("body=%+v", body)
	}
	if len(body.Holdings) != 1 || body.Holdings[0].Quantity != "0.125" || body.Holdings[0].LatestPrice == nil || *body.Holdings[0].LatestPrice != "123.45" || body.Holdings[0].ReportingValue == nil || *body.Holdings[0].ReportingValue != "69.440625" {
		t.Fatalf("holding=%+v", body.Holdings)
	}
	if !contains(response.Body.String(), `"description":"Fixture instrument"`) || contains(response.Body.String(), `"name":"FIX"`) {
		t.Fatalf("instrument description/name rendering = %s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview?currency=USD", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !contains(response.Body.String(), `"reporting_currency":"USD"`) || !contains(response.Body.String(), `"reporting_total":"15.43125"`) {
		t.Fatalf("alternate response=%d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/unknown/overview", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("unknown portfolio status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview?currency=US", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("invalid currency status=%d", response.Code)
	}
}

func TestPortfolioOverviewAPINoSnapshotIsExplicit(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	handler := authenticatedAPIHandler(t, database)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", nil))
	if response.Code != 200 || !contains(response.Body.String(), `"state":"no_snapshot"`) || contains(response.Body.String(), `"reporting_total"`) {
		t.Fatalf("no snapshot response=%d %s", response.Code, response.Body.String())
	}
	var overview domain.PortfolioOverview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Allocation.State != "unavailable" || overview.Allocation.Coverage.TotalLots != 1 || overview.Allocation.Coverage.ValuedLots != 0 || overview.Allocation.Coverage.MissingDependencies != 0 || overview.Allocation.Coverage.StaleDependencies != 0 || overview.Allocation.Coverage.InvalidDependencies != 0 {
		t.Fatalf("no snapshot allocation = %+v", overview.Allocation)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	var allocationWire map[string]json.RawMessage
	if err := json.Unmarshal(wire["allocation"], &allocationWire); err != nil {
		t.Fatal(err)
	}
	if _, present := allocationWire["by_instrument"]; present {
		t.Fatalf("no snapshot unexpectedly emitted by_instrument: %s", response.Body.String())
	}
	if _, present := allocationWire["by_source_currency"]; present {
		t.Fatalf("no snapshot unexpectedly emitted by_source_currency: %s", response.Body.String())
	}
}

func TestPortfolioOverviewAPIReadsAllLotsWithoutCrossPortfolioLeakage(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, portfolio := range []string{"portfolio-33", "portfolio-other"} {
		if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "MYR", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO accounts(id,portfolio_id,broker,created_at) VALUES(?,?,?,?)`, "account-other", "portfolio-other", "Foreign Broker", now); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 31; index++ {
		instrumentID := fmt.Sprintf("instrument-33-%02d", index)
		if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, instrumentID, fmt.Sprintf("SYM%02d", index), "Synthetic instrument", "MYR", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-leak", "LEAK", "Foreign instrument", "MYR", now); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 33; index++ {
		instrumentIndex := index
		if index == 32 {
			instrumentIndex = 1
		}
		if index == 33 {
			instrumentIndex = 2
		}
		accountID := any(nil)
		if index == 1 {
			accountID = "account-other"
		}
		if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,currency,source_row,source_ref,source_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("lot-33-%02d", index), "portfolio-33", fmt.Sprintf("instrument-33-%02d", instrumentIndex), accountID, "1", "MYR", index, fmt.Sprintf("portfolio-33#row=%d", index), fmt.Sprintf("portfolio-33-hash-%02d", index), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,currency,source_row,source_ref,source_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, "lot-other", "portfolio-other", "instrument-leak", "account-other", "99", "MYR", 1, "portfolio-other#row=1", "portfolio-other-hash", now); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 31; index++ {
		price := "1000"
		if index == 3 {
			price = "8000"
		}
		if _, err := database.AddQuote(context.Background(), store.QuoteInput{InstrumentID: fmt.Sprintf("instrument-33-%02d", index), Price: price, Currency: "MYR", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "allocation fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.AddQuote(context.Background(), store.QuoteInput{InstrumentID: "instrument-leak", Price: "999999", Currency: "MYR", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "foreign allocation fixture"}); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC()
	firstSnapshot, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-33", ReportingCurrency: "MYR", AsOf: when, MaxAge: 2 * time.Hour})
	if err != nil || !firstSnapshot.Complete {
		t.Fatalf("first snapshot=%+v err=%v", firstSnapshot, err)
	}
	secondSnapshot, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-other", ReportingCurrency: "MYR", AsOf: when, MaxAge: 2 * time.Hour})
	if err != nil || !secondSnapshot.Complete {
		t.Fatalf("second snapshot=%+v err=%v", secondSnapshot, err)
	}

	handler := authenticatedAPIHandler(t, database)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-33/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var overview domain.PortfolioOverview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Holdings) != 33 {
		t.Fatalf("holdings=%d, want 33", len(overview.Holdings))
	}
	symbols := map[string]int{}
	for _, holding := range overview.Holdings {
		symbols[holding.Symbol]++
		if holding.Symbol == "LEAK" || holding.Broker != nil || holding.LotID == "lot-other" {
			t.Fatalf("cross-portfolio data leaked into holding: %+v", holding)
		}
	}
	if len(symbols) != 31 || symbols["SYM01"] != 2 || symbols["SYM02"] != 2 {
		t.Fatalf("symbols=%d duplicate counts SYM01=%d SYM02=%d", len(symbols), symbols["SYM01"], symbols["SYM02"])
	}
	if overview.Snapshot.State != "complete" || !overview.Snapshot.Complete || overview.Snapshot.ReportingTotal != "40000" {
		t.Fatalf("snapshot=%+v", overview.Snapshot)
	}
	if overview.Allocation.State != "complete" || overview.Allocation.Coverage.TotalLots != 33 || overview.Allocation.Coverage.ValuedLots != 33 || len(overview.Allocation.ByInstrument) != 31 || len(overview.Allocation.BySourceCurrency) != 1 {
		t.Fatalf("allocation coverage=%+v instruments=%d currencies=%d", overview.Allocation.Coverage, len(overview.Allocation.ByInstrument), len(overview.Allocation.BySourceCurrency))
	}
	instrumentTotal := domain.ZeroDecimal()
	instrumentPercentageTotal := domain.ZeroDecimal()
	for _, item := range overview.Allocation.ByInstrument {
		instrumentTotal = instrumentTotal.Add(domain.MustDecimal(item.Value))
		instrumentPercentageTotal = instrumentPercentageTotal.Add(domain.MustDecimal(item.Percentage))
	}
	currencyTotal := domain.ZeroDecimal()
	currencyPercentageTotal := domain.ZeroDecimal()
	for _, item := range overview.Allocation.BySourceCurrency {
		currencyTotal = currencyTotal.Add(domain.MustDecimal(item.Value))
		currencyPercentageTotal = currencyPercentageTotal.Add(domain.MustDecimal(item.Percentage))
	}
	wantTotal := domain.MustDecimal("40000")
	if instrumentTotal.Rat().Cmp(wantTotal.Rat()) != 0 || currencyTotal.Rat().Cmp(wantTotal.Rat()) != 0 || instrumentPercentageTotal.Rat().Cmp(domain.MustDecimal("100.00").Rat()) != 0 || currencyPercentageTotal.Rat().Cmp(domain.MustDecimal("100.00").Rat()) != 0 {
		t.Fatalf("allocation reconciliation instruments=%s/%s percentages=%s currencies=%s/%s percentages=%s", instrumentTotal.String(), wantTotal.String(), instrumentPercentageTotal.String(), currencyTotal.String(), wantTotal.String(), currencyPercentageTotal.String())
	}
	futureMarketAt := when.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if _, err := database.DB.Exec(`INSERT INTO quotes(id,instrument_id,price,currency,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?)`, "future-invalid-instrument-33-01", "instrument-33-01", "1000", "MYR", "fixture", futureMarketAt, when.Format(time.RFC3339Nano), "close", "partial allocation fixture"); err != nil {
		t.Fatal(err)
	}
	partialSnapshot, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-33", ReportingCurrency: "MYR", AsOf: when, MaxAge: 2 * time.Hour})
	if err != nil || partialSnapshot.Complete {
		t.Fatalf("partial snapshot=%+v err=%v", partialSnapshot, err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-33/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("partial status=%d body=%s", response.Code, response.Body.String())
	}
	var partialOverview domain.PortfolioOverview
	if err := json.Unmarshal(response.Body.Bytes(), &partialOverview); err != nil {
		t.Fatal(err)
	}
	if partialOverview.Snapshot.State != "incomplete" || partialOverview.Snapshot.Complete || partialOverview.Allocation.State != "partial" || partialOverview.Allocation.Coverage.TotalLots != 33 || partialOverview.Allocation.Coverage.ValuedLots != 31 || partialOverview.Allocation.Coverage.InvalidDependencies != 2 || len(partialOverview.Allocation.ByInstrument) != 0 || len(partialOverview.Allocation.BySourceCurrency) != 0 {
		t.Fatalf("partial overview=%+v", partialOverview)
	}
	var partialWire map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &partialWire); err != nil {
		t.Fatal(err)
	}
	var partialAllocationWire map[string]json.RawMessage
	if err := json.Unmarshal(partialWire["allocation"], &partialAllocationWire); err != nil {
		t.Fatal(err)
	}
	if _, present := partialAllocationWire["by_instrument"]; present {
		t.Fatalf("partial response unexpectedly emitted by_instrument: %s", response.Body.String())
	}
	if _, present := partialAllocationWire["by_source_currency"]; present {
		t.Fatalf("partial response unexpectedly emitted by_source_currency: %s", response.Body.String())
	}
	secondView, err := database.PortfolioOverview(context.Background(), "portfolio-other", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if secondView.Allocation.State != "complete" || len(secondView.Allocation.ByInstrument) != 1 || len(secondView.Allocation.BySourceCurrency) != 1 || len(secondView.Holdings) != 1 || secondView.Holdings[0].Symbol != "LEAK" || secondView.Holdings[0].Broker == nil || *secondView.Holdings[0].Broker != "Foreign Broker" {
		t.Fatalf("second portfolio view=%+v", secondView)
	}
}

func TestPortfolioActivityAPIIsAuthenticatedScopedAndRedacted(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	ctx := context.Background()
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, "unscoped-fixture", "administrative-quote", "market.quote.added", "quote", "missing", `{"marker":"redaction-marker"}`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("api-fixture-session-secret-with-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity", nil))
	if anonymous.Code != http.StatusUnauthorized || anonymous.Header().Get("Cache-Control") != "no-store" || anonymous.Header().Get("Vary") != "Cookie" {
		t.Fatalf("anonymous=%d cache=%q vary=%q", anonymous.Code, anonymous.Header().Get("Cache-Control"), anonymous.Header().Get("Vary"))
	}
	authenticated := authenticatedAPIHandler(t, database)
	response := httptest.NewRecorder()
	authenticated.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?limit=1", nil))
	if response.Code != http.StatusOK || !contains(response.Body.String(), `"has_more":true`) || contains(response.Body.String(), "redaction-marker") || contains(response.Body.String(), "provenance") {
		t.Fatalf("activity=%d %s", response.Code, response.Body.String())
	}
	var firstPage struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &firstPage); err != nil || firstPage.NextCursor == "" {
		t.Fatalf("missing activity cursor: %s", response.Body.String())
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-api-other", "Other API Fixture", "MYR", time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	wrongPortfolio := httptest.NewRecorder()
	authenticated.ServeHTTP(wrongPortfolio, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api-other/activity?cursor="+firstPage.NextCursor, nil))
	if wrongPortfolio.Code != http.StatusBadRequest {
		t.Fatalf("wrong portfolio cursor=%d %s", wrongPortfolio.Code, wrongPortfolio.Body.String())
	}
	for _, malformed := range []string{"not-base64", firstPage.NextCursor[:len(firstPage.NextCursor)-1] + "A", strings.Repeat("A", 513)} {
		bad := httptest.NewRecorder()
		authenticated.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?cursor="+malformed, nil))
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("malformed cursor=%q status=%d", malformed, bad.Code)
		}
	}
	isolated := httptest.NewRecorder()
	authenticated.ServeHTTP(isolated, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api-other/activity", nil))
	if isolated.Code != http.StatusOK || !contains(isolated.Body.String(), `"items":[]`) || !contains(isolated.Body.String(), `"has_more":false`) {
		t.Fatalf("cross-portfolio activity=%d %s", isolated.Code, isolated.Body.String())
	}
	invalid := httptest.NewRecorder()
	authenticated.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?limit=101", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid pagination=%d %s", invalid.Code, invalid.Body.String())
	}
	for _, rawQuery := range []string{"cursor=%ZZ", "cursor=cursor;drop", "limit=1&limit=2", "limit=", "offset=1"} {
		badQuery := httptest.NewRecorder()
		authenticated.ServeHTTP(badQuery, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/activity?"+rawQuery, nil))
		if badQuery.Code != http.StatusBadRequest || badQuery.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("malformed raw query %q = %d cache=%q body=%s", rawQuery, badQuery.Code, badQuery.Header().Get("Cache-Control"), badQuery.Body.String())
		}
	}
	unknown := httptest.NewRecorder()
	authenticated.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/unknown/activity", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown=%d %s", unknown.Code, unknown.Body.String())
	}
}

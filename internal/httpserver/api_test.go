package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
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

func TestPortfolioOverviewAPIIsScopedAndKeepsDecimalsAsStrings(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	when := time.Now().UTC()
	if _, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "api.test.v1", AsOf: when, MaxAge: 2 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithStore(database)
	if err != nil {
		t.Fatal(err)
	}
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
	handler, err := NewHandlerWithStore(database)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", nil))
	if response.Code != 200 || !contains(response.Body.String(), `"state":"no_snapshot"`) || contains(response.Body.String(), `"reporting_total"`) {
		t.Fatalf("no snapshot response=%d %s", response.Code, response.Body.String())
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

	handler, err := NewHandlerWithStore(database)
	if err != nil {
		t.Fatal(err)
	}
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
}

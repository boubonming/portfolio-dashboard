package store

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

func e2eNow() time.Time {
	return time.Date(2026, time.January, 2, 12, 0, 0, 123456789, time.UTC)
}

func TestCanonicalHoldingRefreshMappingSnapshotWorkflow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/e2e.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := canonicalSnapshotTime(e2eNow())
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-e2e", "E2E", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO accounts(id,portfolio_id,broker,name,created_at) VALUES(?,?,?,?,?)`, "account-e2e", "portfolio-e2e", "fixture-broker", "Fixture account", now); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, symbol string }{{"instrument-e2e-a", "E2EA"}, {"instrument-e2e-b", "E2EB"}} {
		if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, fixture.id, fixture.symbol, "USD", now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-e2e", fixture.id, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?,?)`, "lot-"+fixture.id, "portfolio-e2e", fixture.id, "account-e2e", "2", "3", "6", "USD", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: fixture.id, Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FIXED-" + fixture.symbol, QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "local fake provider fixture"}); err != nil {
			t.Fatal(err)
		}
	}

	var quoteCalls, fxCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/quote"):
			quoteCalls.Add(1)
			_, _ = w.Write([]byte(`{"c":"10.25","t":1767355200}`))
		case r.URL.Path == "/latest.json":
			fxCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"timestamp":%d,"base":"USD","rates":{"MYR":"4.5"}}`, e2eNow().Add(-time.Hour).Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	when := e2eNow()
	result, err := s.Refresh(ctx, RefreshOptions{
		PortfolioID: "portfolio-e2e", ReportingCurrency: "MYR",
		Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates},
		Clients: map[marketdata.Provider]marketdata.ProviderClient{
			marketdata.ProviderFinnhub: NewFakeFinnhub(server.URL, when),
			marketdata.ProviderOpenExchangeRates: marketdata.NewOpenExchangeRatesClient("fixture-app", marketdata.ClientOptions{BaseURL: server.URL, Now: func() time.Time { return when }}),
		},
		AsOf: when,
	})
	if err != nil || result.Status != "complete" {
		t.Fatalf("refresh result=%+v err=%v", result, err)
	}
	if quoteCalls.Load() == 0 || fxCalls.Load() != 1 {
		t.Fatalf("fake request counts quotes=%d fx=%d", quoteCalls.Load(), fxCalls.Load())
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-e2e", ReportingCurrency: "MYR", AsOf: when, MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Complete || status.ReportingTotal == "" {
		t.Fatalf("snapshot status=%+v", status)
	}
}

type fakeFinnhub struct {
	baseURL string
	now     time.Time
}

func NewFakeFinnhub(baseURL string, now time.Time) marketdata.ProviderClient {
	return &fakeFinnhub{baseURL: baseURL, now: now}
}
func (c *fakeFinnhub) Name() marketdata.Provider { return marketdata.ProviderFinnhub }
func (c *fakeFinnhub) FX(context.Context, marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, marketdata.ErrUnsupportedOperation
}
func (c *fakeFinnhub) Quote(ctx context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	return marketdata.NewFinnhubClient("fixture", marketdata.ClientOptions{BaseURL: c.baseURL, Now: func() time.Time { return c.now }}).Quote(ctx, request)
}

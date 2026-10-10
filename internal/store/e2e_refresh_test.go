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

func e2eNow() time.Time { return time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC) }

func TestFakeProviderImportMappingRefreshSnapshotWorkflow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/e2e.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("z", 64))
	if _, err := s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR"); err != nil {
		t.Fatal(err)
	}
	mappingRows, err := s.DB.QueryContext(ctx, "SELECT id,symbol,currency FROM instruments ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	type mappingFixture struct{ id, symbol, currency string }
	var mappingFixtures []mappingFixture
	for mappingRows.Next() {
		var id, symbol, currency string
		if err := mappingRows.Scan(&id, &symbol, &currency); err != nil {
			mappingRows.Close()
			t.Fatal(err)
		}
		mappingFixtures = append(mappingFixtures, mappingFixture{id, symbol, currency})
	}
	if err := mappingRows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range mappingFixtures {
		if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: fixture.id, Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FIXED-" + fixture.symbol, QuoteCurrency: fixture.currency, ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "local fake provider fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	var quoteCalls, fxCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/quote":
			quoteCalls.Add(1)
			_, _ = w.Write([]byte(`{"c":"10.25","t":1767355200}`))
		case "/latest.json":
			fxCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"timestamp":%d,"base":"USD","rates":{"MYR":"4.5"}}`, e2eNow().Add(-time.Hour).Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	when := e2eNow()
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: defaultPortfolioID, ReportingCurrency: "MYR", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub:           NewFakeFinnhub(server.URL, when),
		marketdata.ProviderOpenExchangeRates: marketdata.NewOpenExchangeRatesClient("fixture-app", marketdata.ClientOptions{BaseURL: server.URL, Now: func() time.Time { return when }}),
	}, AsOf: when})
	if err != nil || result.Status != "complete" {
		t.Fatalf("refresh result=%+v err=%v", result, err)
	}
	if quoteCalls.Load() != 31 || fxCalls.Load() != 1 {
		t.Fatalf("fake request counts quotes=%d fx=%d", quoteCalls.Load(), fxCalls.Load())
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: defaultPortfolioID, ReportingCurrency: "MYR", AsOf: when, MaxAge: 24 * time.Hour})
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

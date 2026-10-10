package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

type fakeRefreshClient struct {
	calls          int
	failInstrument string
}

func (f *fakeRefreshClient) Name() marketdata.Provider { return marketdata.ProviderFinnhub }
func (f *fakeRefreshClient) FX(context.Context, marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, marketdata.ErrUnsupportedOperation
}
func (f *fakeRefreshClient) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	f.calls++
	if request.InstrumentID == f.failInstrument {
		return domain.Quote{}, errors.New("fixture provider failure")
	}
	return domain.Quote{ID: "fixture-" + request.InstrumentID, InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "2.5", Currency: request.Currency, Source: "finnhub", MarketAt: "2026-01-02T10:00:00Z", FetchedAt: "2026-01-02T12:00:00Z", Basis: "close", Provenance: "local fixture"}, nil
}

func openRefreshFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "refresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-01-02T12:00:00Z"
	_, err = s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('refresh-portfolio','Refresh fixture','USD',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-instrument','LOCAL','USD',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,source_ref,source_sha256,created_at) VALUES('refresh-lot','refresh-portfolio','refresh-instrument','1','USD','fixture','fixture',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRefreshRequiresExplicitMappingAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	client := &fakeRefreshClient{}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "incomplete" || result.Providers[0].MissingMapping != 1 || client.calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, client.calls)
	}
	if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "EXACT:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "local mapping fixture"}); err != nil {
		t.Fatal(err)
	}
	result, err = s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "complete" || result.Providers[0].SucceededCount != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_, err = s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var quotes, audits int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM quotes").Scan(&quotes); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='market.quote.added'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if quotes != 1 || audits != 1 {
		t.Fatalf("quotes=%d audits=%d", quotes, audits)
	}
}

func TestRefreshPartialFailureKeepsSuccessfulObservation(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-instrument-2','SECOND','USD',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,source_ref,source_sha256,created_at) VALUES('refresh-lot-2','refresh-portfolio','refresh-instrument-2','1','USD','fixture','fixture-2',?)`, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"refresh-instrument", "refresh-instrument-2"} {
		if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: id, Provider: marketdata.ProviderFinnhub, ProviderSymbol: id, QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "local fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	client := &fakeRefreshClient{failInstrument: "refresh-instrument-2"}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "incomplete" || result.Providers[0].SucceededCount != 1 || result.Providers[0].FailedCount != 1 {
		t.Fatalf("result=%+v", result)
	}
	var quotes int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM quotes").Scan(&quotes); err != nil {
		t.Fatal(err)
	}
	if quotes != 1 {
		t.Fatalf("successful quotes were not retained: %d", quotes)
	}
}

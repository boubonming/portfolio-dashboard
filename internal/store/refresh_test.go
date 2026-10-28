package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

type timestampRefreshClient struct {
	marketAt  string
	fetchedAt string
}

func (f timestampRefreshClient) Name() marketdata.Provider { return marketdata.ProviderFinnhub }
func (f timestampRefreshClient) FX(context.Context, marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, marketdata.ErrUnsupportedOperation
}
func (f timestampRefreshClient) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	return domain.Quote{ID: "timestamp-" + request.InstrumentID, InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "2.5", Currency: request.Currency, Source: marketdata.ProviderFinnhub.String(), MarketAt: f.marketAt, FetchedAt: f.fetchedAt, Basis: "close", Provenance: "timestamp fixture"}, nil
}

type timestampFXClient struct {
	marketAt  string
	fetchedAt string
}

func (f timestampFXClient) Name() marketdata.Provider { return marketdata.ProviderOpenExchangeRates }
func (timestampFXClient) Quote(context.Context, marketdata.QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, marketdata.ErrUnsupportedOperation
}
func (f timestampFXClient) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{ID: "timestamp-fx", BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "2", Source: marketdata.ProviderOpenExchangeRates.String(), MarketAt: f.marketAt, FetchedAt: f.fetchedAt, Basis: "close", Provenance: "timestamp fixture"}, nil
}

type collisionFXClient struct{}

func (collisionFXClient) Name() marketdata.Provider { return marketdata.ProviderOpenExchangeRates }
func (collisionFXClient) Quote(context.Context, marketdata.QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, marketdata.ErrUnsupportedOperation
}
func (collisionFXClient) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{ID: "collision-fx", BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "2", Source: marketdata.ProviderOpenExchangeRates.String(), MarketAt: "2026-01-02T10:00:00Z", FetchedAt: "2026-01-02T12:00:00Z", Basis: "close", Provenance: "local fixture"}, nil
}

type preEpochQuoteClient struct{}

func (preEpochQuoteClient) Name() marketdata.Provider { return marketdata.ProviderFinnhub }
func (preEpochQuoteClient) FX(context.Context, marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, marketdata.ErrUnsupportedOperation
}
func preEpochQuote(request marketdata.QuoteRequest) domain.Quote {
	return domain.Quote{ID: "pre-epoch-" + request.InstrumentID, InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "2", Currency: request.Currency, Source: marketdata.ProviderFinnhub.String(), MarketAt: "1969-12-31T23:59:59Z", FetchedAt: "1969-12-31T23:59:59Z", Basis: "close", Provenance: "pre-epoch fixture"}
}
func (preEpochQuoteClient) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	return preEpochQuote(request), nil
}

type preEpochBatchQuoteClient struct{ preEpochQuoteClient }

func (preEpochBatchQuoteClient) QuoteBatch(_ context.Context, requests []marketdata.QuoteRequest) (map[string]domain.Quote, map[string]error) {
	quotes := make(map[string]domain.Quote, len(requests))
	for _, request := range requests {
		quotes[request.InstrumentID] = preEpochQuote(request)
	}
	return quotes, nil
}

type preEpochFXClient struct{}

func (preEpochFXClient) Name() marketdata.Provider { return marketdata.ProviderOpenExchangeRates }
func (preEpochFXClient) Quote(context.Context, marketdata.QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, marketdata.ErrUnsupportedOperation
}
func preEpochFX(request marketdata.FXRequest) domain.FXRate {
	return domain.FXRate{ID: "pre-epoch-fx", BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "2", Source: marketdata.ProviderOpenExchangeRates.String(), MarketAt: "1969-12-31T23:59:59Z", FetchedAt: "1969-12-31T23:59:59Z", Basis: "close", Provenance: "pre-epoch fixture"}
}
func (preEpochFXClient) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	return preEpochFX(request), nil
}

type preEpochBatchFXClient struct{ preEpochFXClient }

func (preEpochBatchFXClient) FXBatch(_ context.Context, requests []marketdata.FXRequest) (map[string]domain.FXRate, map[string]error) {
	rates := make(map[string]domain.FXRate, len(requests))
	for _, request := range requests {
		rates[strings.ToUpper(request.BaseCurrency)+"/"+strings.ToUpper(request.QuoteCurrency)] = preEpochFX(request)
	}
	return rates, nil
}

type assignedRefreshClient struct {
	provider marketdata.Provider
	calls    []string
	symbols  []string
}

func (f *assignedRefreshClient) Name() marketdata.Provider { return f.provider }
func (f *assignedRefreshClient) FX(context.Context, marketdata.FXRequest) (domain.FXRate, error) {
	return domain.FXRate{}, marketdata.ErrUnsupportedOperation
}
func (f *assignedRefreshClient) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	f.calls = append(f.calls, request.InstrumentID)
	f.symbols = append(f.symbols, request.ProviderSymbol)
	return domain.Quote{ID: "assigned-" + request.InstrumentID, InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "2.5", Currency: request.Currency, Source: f.provider.String(), MarketAt: "2026-01-02T10:00:00Z", FetchedAt: "2026-01-02T12:00:00Z", Basis: "close", Provenance: "local fixture"}, nil
}

func openRefreshFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "refresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-01-02T12:00:00.000000000Z"
	_, err = s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('refresh-portfolio','Refresh fixture','USD',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-instrument','LOCAL','USD',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-instrument',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-lot','refresh-portfolio','refresh-instrument','1','USD',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRefreshRejectsPreEpochScalarAndBatchObservationsWithoutAborting(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name   string
		client marketdata.ProviderClient
	}{
		{name: "scalar quote", client: preEpochQuoteClient{}},
		{name: "batch quote", client: preEpochBatchQuoteClient{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openRefreshFixture(t)
			defer s.Close()
			if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
				t.Fatal(err)
			}
			result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: testCase.client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
			if err != nil || result.Status != "incomplete" || result.Providers[0].SucceededCount != 0 || result.Providers[0].FailedCount != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var quotes int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM quotes`).Scan(&quotes); err != nil {
				t.Fatal(err)
			}
			if quotes != 0 {
				t.Fatalf("pre-epoch quote was persisted: %d", quotes)
			}
		})
	}
	for _, testCase := range []struct {
		name   string
		client marketdata.ProviderClient
	}{
		{name: "scalar FX", client: preEpochFXClient{}},
		{name: "batch FX", client: preEpochBatchFXClient{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openRefreshFixture(t)
			defer s.Close()
			now := "2026-01-02T12:00:00.000000000Z"
			if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-eur','EURREL','EUR',?)`, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-eur',?)`, now); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-eur-lot','refresh-portfolio','refresh-eur','1','EUR',?,?)`, now, now); err != nil {
				t.Fatal(err)
			}
			result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", ReportingCurrency: "USD", Providers: []marketdata.Provider{marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderOpenExchangeRates: testCase.client}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
			if err != nil || result.Status != "incomplete" || result.Providers[0].SucceededCount != 0 || result.Providers[0].FailedCount != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var rates int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM fx_rates`).Scan(&rates); err != nil {
				t.Fatal(err)
			}
			if rates != 0 {
				t.Fatalf("pre-epoch FX rate was persisted: %d", rates)
			}
		})
	}
}

func TestRefreshQuoteCollisionDowngradesOnlyTheConflictingProviderAtomically(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('unrelated-instrument','UNRELATED','USD',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "fixture-refresh-instrument", InstrumentID: "unrelated-instrument", Currency: "USD", Price: "99", Source: "fixture", MarketAt: "2026-01-02T10:00:00Z", FetchedAt: now, Basis: "close", Provenance: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: &fakeRefreshClient{}}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "incomplete" || len(result.ObservationIDs) != 0 || result.Providers[0].SucceededCount != 0 || result.Providers[0].FailedCount != 1 {
		t.Fatalf("collision result=%+v", result)
	}
	var price string
	if err := s.DB.QueryRow(`SELECT price FROM quotes WHERE id='fixture-refresh-instrument'`).Scan(&price); err != nil || price != "99" {
		t.Fatalf("unrelated quote price=%q err=%v", price, err)
	}
	var quoteActivity, refreshActivity int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE portfolio_id='refresh-portfolio' AND action='market.quote.added'`).Scan(&quoteActivity); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE portfolio_id='refresh-portfolio' AND action='market.refresh.completed'`).Scan(&refreshActivity); err != nil {
		t.Fatal(err)
	}
	if quoteActivity != 0 || refreshActivity != 1 {
		t.Fatalf("quote activity=%d refresh activity=%d", quoteActivity, refreshActivity)
	}
}

func TestRefreshFXCollisionDowngradesProviderAndExactReplayRemainsComplete(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-eur','EURREL','EUR',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-eur',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-eur-lot','refresh-portfolio','refresh-eur','1','EUR',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO fx_rates(id,base_currency,quote_currency,rate,source,market_at,fetched_at,basis,provenance) VALUES('collision-fx','EUR','USD','9',?,?,?, ?,?)`, marketdata.ProviderOpenExchangeRates.String(), "2026-01-02T10:00:00Z", now, "close", "unrelated"); err != nil {
		t.Fatal(err)
	}
	options := RefreshOptions{PortfolioID: "refresh-portfolio", ReportingCurrency: "USD", Providers: []marketdata.Provider{marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderOpenExchangeRates: collisionFXClient{}}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)}
	result, err := s.Refresh(ctx, options)
	if err != nil || result.Status != "incomplete" || len(result.ObservationIDs) != 0 || result.Providers[0].SucceededCount != 0 || result.Providers[0].FailedCount != 1 {
		t.Fatalf("FX collision result=%+v err=%v", result, err)
	}
	var rate string
	if err := s.DB.QueryRow(`SELECT rate FROM fx_rates WHERE id='collision-fx'`).Scan(&rate); err != nil || rate != "9" {
		t.Fatalf("unrelated FX rate=%q err=%v", rate, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openRefreshFixture(t)
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-eur','EURREL','EUR',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-eur',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-eur-lot','refresh-portfolio','refresh-eur','1','EUR',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	result, err = s.Refresh(ctx, options)
	if err != nil || result.Status != "complete" || result.Providers[0].SucceededCount != 1 || len(result.ObservationIDs) != 1 {
		t.Fatalf("first coherent FX refresh=%+v err=%v", result, err)
	}
	result, err = s.Refresh(ctx, options)
	if err != nil || result.Status != "complete" || result.Providers[0].SucceededCount != 1 || len(result.ObservationIDs) != 1 {
		t.Fatalf("exact FX replay=%+v err=%v", result, err)
	}
	var fxCount, fxActivity int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM fx_rates WHERE id='collision-fx'`).Scan(&fxCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE portfolio_id='refresh-portfolio' AND action='market.fx.added'`).Scan(&fxActivity); err != nil {
		t.Fatal(err)
	}
	if fxCount != 1 || fxActivity != 1 {
		t.Fatalf("FX replay count=%d activity=%d", fxCount, fxActivity)
	}
}

func TestRefreshPersistsReviewCompatibleCanonicalObservationTimestamps(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Refresh(ctx, RefreshOptions{
		PortfolioID: "refresh-portfolio",
		Providers:   []marketdata.Provider{marketdata.ProviderFinnhub},
		Clients:     map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: &fakeRefreshClient{}},
		AsOf:        time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
	})
	if err != nil || result.Status != "complete" {
		t.Fatalf("refresh=%+v err=%v", result, err)
	}
	var marketAt, fetchedAt string
	if err := s.DB.QueryRow(`SELECT market_at,fetched_at FROM quotes WHERE id=?`, "fixture-refresh-instrument").Scan(&marketAt, &fetchedAt); err != nil {
		t.Fatal(err)
	}
	if marketAt != "2026-01-02T10:00:00.000000000Z" || fetchedAt != "2026-01-02T12:00:00.000000000Z" {
		t.Fatalf("persisted timestamps market_at=%q fetched_at=%q", marketAt, fetchedAt)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "refresh-portfolio", ReportingCurrency: "USD", AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), MaxAge: 3 * time.Hour})
	if err != nil || !status.Complete {
		t.Fatalf("snapshot=%+v err=%v", status, err)
	}
	for _, latest := range []bool{false, true} {
		if _, err := s.ReviewSnapshot(ctx, "refresh-portfolio", status.SnapshotID, latest); err != nil {
			t.Fatalf("review latest=%v: %v", latest, err)
		}
	}
}

func TestRefreshCanonicalizesAcceptedObservationTimestampRepresentations(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, marketAt, fetchedAt, wantMarketAt, wantFetchedAt string
	}{
		{"whole second", "2026-01-02T10:00:00Z", "2026-01-02T12:00:00Z", "2026-01-02T10:00:00.000000000Z", "2026-01-02T12:00:00.000000000Z"},
		{"fractional second", "2026-01-02T10:00:00.123456Z", "2026-01-02T12:00:00.654321Z", "2026-01-02T10:00:00.123456000Z", "2026-01-02T12:00:00.654321000Z"},
		{"canonical nine fractional digits", "2026-01-02T10:00:00.123456789Z", "2026-01-02T12:00:00.987654321Z", "2026-01-02T10:00:00.123456789Z", "2026-01-02T12:00:00.987654321Z"},
		{"offset crosses UTC boundary", "2026-01-02T10:00:00+02:00", "2026-01-02T12:00:00+02:00", "2026-01-02T08:00:00.000000000Z", "2026-01-02T10:00:00.000000000Z"},
		{"epoch boundary", "1970-01-01T00:00:00Z", "1970-01-01T00:00:00Z", "1970-01-01T00:00:00.000000000Z", "1970-01-01T00:00:00.000000000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openRefreshFixture(t)
			defer s.Close()
			if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
				t.Fatal(err)
			}
			result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: timestampRefreshClient{marketAt: tc.marketAt, fetchedAt: tc.fetchedAt}}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
			if err != nil || result.Status != "complete" {
				t.Fatalf("refresh=%+v err=%v", result, err)
			}
			var marketAt, fetchedAt string
			if err := s.DB.QueryRow(`SELECT market_at,fetched_at FROM quotes WHERE id=?`, "timestamp-refresh-instrument").Scan(&marketAt, &fetchedAt); err != nil {
				t.Fatal(err)
			}
			if marketAt != tc.wantMarketAt || fetchedAt != tc.wantFetchedAt {
				t.Fatalf("persisted timestamps market_at=%q fetched_at=%q", marketAt, fetchedAt)
			}
		})
	}
}

func TestRefreshRejectsMalformedObservationTimestampRepresentations(t *testing.T) {
	ctx := context.Background()
	for _, value := range []string{"2026-02-30T10:00:00Z", "2026-01-02T10:00:00", "1969-12-31T23:59:59Z", "not-a-timestamp"} {
		t.Run(value, func(t *testing.T) {
			s := openRefreshFixture(t)
			defer s.Close()
			if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
				t.Fatal(err)
			}
			result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: timestampRefreshClient{marketAt: value, fetchedAt: value}}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
			if err != nil || result.Status != "incomplete" || result.Providers[0].FailedCount != 1 {
				t.Fatalf("refresh=%+v err=%v", result, err)
			}
			var quotes int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM quotes`).Scan(&quotes); err != nil {
				t.Fatal(err)
			}
			if quotes != 0 {
				t.Fatalf("malformed quote persisted: %d", quotes)
			}
		})
	}
}

func TestRefreshCanonicalizesFXObservationTimestampRepresentations(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-eur','EURREL','EUR',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-eur',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-eur-lot','refresh-portfolio','refresh-eur','1','EUR',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", ReportingCurrency: "USD", Providers: []marketdata.Provider{marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderOpenExchangeRates: timestampFXClient{marketAt: "2026-01-02T10:00:00+02:00", fetchedAt: "2026-01-02T12:00:00+02:00"}}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "complete" {
		t.Fatalf("refresh=%+v err=%v", result, err)
	}
	var marketAt, fetchedAt string
	if err := s.DB.QueryRow(`SELECT market_at,fetched_at FROM fx_rates WHERE id=?`, "timestamp-fx").Scan(&marketAt, &fetchedAt); err != nil {
		t.Fatal(err)
	}
	if marketAt != "2026-01-02T08:00:00.000000000Z" || fetchedAt != "2026-01-02T10:00:00.000000000Z" {
		t.Fatalf("persisted FX timestamps market_at=%q fetched_at=%q", marketAt, fetchedAt)
	}
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
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-instrument-2','SECOND','USD',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-instrument-2',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('refresh-lot-2','refresh-portfolio','refresh-instrument-2','1','USD',?,?)`, now, now); err != nil {
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

func TestRefreshDefaultProvidersAssignEachInstrumentOnce(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	for _, id := range []string{"refresh-instrument-alpha", "refresh-instrument-coin"} {
		if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, id, id, "USD", now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "refresh-portfolio", id, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "lot-"+id, "refresh-portfolio", id, "1", "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []ProviderMappingInput{
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument-alpha", Provider: marketdata.ProviderAlphaVantage, ProviderSymbol: "AV:ALPHA", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument-coin", Provider: marketdata.ProviderCoinGecko, ProviderSymbol: "coin", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"},
	} {
		if _, err := s.AddProviderMapping(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	finnhub := &assignedRefreshClient{provider: marketdata.ProviderFinnhub}
	alpha := &assignedRefreshClient{provider: marketdata.ProviderAlphaVantage}
	coin := &assignedRefreshClient{provider: marketdata.ProviderCoinGecko}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub: finnhub, marketdata.ProviderAlphaVantage: alpha, marketdata.ProviderCoinGecko: coin, marketdata.ProviderOpenExchangeRates: marketdata.NoLiveClients{},
	}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "complete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(finnhub.calls) != 1 || finnhub.calls[0] != "refresh-instrument" || len(alpha.calls) != 1 || alpha.calls[0] != "refresh-instrument-alpha" || len(coin.calls) != 1 || coin.calls[0] != "refresh-instrument-coin" {
		t.Fatalf("assigned calls finnhub=%v alpha=%v coin=%v", finnhub.calls, alpha.calls, coin.calls)
	}
	for _, status := range result.Providers {
		if status.MissingMapping != 0 {
			t.Fatalf("unexpected missing mappings: %+v", result.Providers)
		}
	}
}

func TestRefreshDefaultProvidersUseLatestMappingAcrossProviders(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	for _, input := range []ProviderMappingInput{
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderAlphaVantage, ProviderSymbol: "AV:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-02T00:00:00Z", Provenance: "fixture"},
	} {
		if _, err := s.AddProviderMapping(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	finnhub := &assignedRefreshClient{provider: marketdata.ProviderFinnhub}
	alpha := &assignedRefreshClient{provider: marketdata.ProviderAlphaVantage}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub: finnhub, marketdata.ProviderAlphaVantage: alpha, marketdata.ProviderOpenExchangeRates: marketdata.NoLiveClients{},
	}, AsOf: time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "complete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(finnhub.calls) != 0 || len(alpha.calls) != 1 || alpha.calls[0] != "refresh-instrument" {
		t.Fatalf("latest mapping was not selected: finnhub=%v alpha=%v", finnhub.calls, alpha.calls)
	}
}

func TestRefreshRejectsCrossProviderMappingTieWithoutQuoteCall(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	for _, input := range []ProviderMappingInput{
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-02T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderAlphaVantage, ProviderSymbol: "AV:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-02T00:00:00Z", Provenance: "fixture"},
	} {
		if _, err := s.AddProviderMapping(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	finnhub := &assignedRefreshClient{provider: marketdata.ProviderFinnhub}
	alpha := &assignedRefreshClient{provider: marketdata.ProviderAlphaVantage}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub: finnhub, marketdata.ProviderAlphaVantage: alpha,
	}, AsOf: time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "incomplete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(finnhub.calls) != 0 || len(alpha.calls) != 0 {
		t.Fatalf("ambiguous mapping was quoted: finnhub=%v alpha=%v", finnhub.calls, alpha.calls)
	}
	var mappingErrors int
	for _, status := range result.Providers {
		for _, refreshError := range status.Errors {
			if refreshError == "mapping_ambiguous" {
				mappingErrors++
			}
		}
	}
	if mappingErrors != 1 {
		t.Fatalf("expected one explicit mapping error, got %d: %+v", mappingErrors, result.Providers)
	}
}

func TestRefreshExplicitProviderSubsetUsesLatestEligibleMapping(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	for _, input := range []ProviderMappingInput{
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL-NEW", QuoteCurrency: "USD", ActiveFrom: "2026-01-02T00:00:00Z", Provenance: "fixture"},
		{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderAlphaVantage, ProviderSymbol: "AV:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-03T00:00:00Z", Provenance: "fixture"},
	} {
		if _, err := s.AddProviderMapping(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	finnhub := &assignedRefreshClient{provider: marketdata.ProviderFinnhub}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub}, Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub: finnhub,
	}, AsOf: time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "complete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(finnhub.calls) != 1 || finnhub.calls[0] != "refresh-instrument" || len(finnhub.symbols) != 1 || finnhub.symbols[0] != "FH:LOCAL-NEW" {
		t.Fatalf("subset did not use eligible Finnhub mapping: calls=%v symbols=%v", finnhub.calls, finnhub.symbols)
	}
}

func TestRefreshUnmappedInstrumentCountedOnceAndNotRequested(t *testing.T) {
	ctx := context.Background()
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('refresh-unmapped','UNMAPPED','USD',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio','refresh-unmapped',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('lot-unmapped','refresh-portfolio','refresh-unmapped','1','USD',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "refresh-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "FH:LOCAL", QuoteCurrency: "USD", ActiveFrom: "2026-01-01T00:00:00Z", Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	client := &assignedRefreshClient{provider: marketdata.ProviderFinnhub}
	result, err := s.Refresh(ctx, RefreshOptions{PortfolioID: "refresh-portfolio", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderAlphaVantage, marketdata.ProviderCoinGecko}, Clients: map[marketdata.Provider]marketdata.ProviderClient{
		marketdata.ProviderFinnhub: client, marketdata.ProviderAlphaVantage: &assignedRefreshClient{provider: marketdata.ProviderAlphaVantage}, marketdata.ProviderCoinGecko: &assignedRefreshClient{provider: marketdata.ProviderCoinGecko},
	}, AsOf: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)})
	if err != nil || result.Status != "incomplete" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	missing := 0
	for _, status := range result.Providers {
		missing += status.MissingMapping
	}
	if missing != 1 || len(client.calls) != 1 || client.calls[0] != "refresh-instrument" {
		t.Fatalf("missing=%d calls=%v statuses=%+v", missing, client.calls, result.Providers)
	}
}

func TestRefreshTargetsExcludeRemovedOnlyInstruments(t *testing.T) {
	s := openRefreshFixture(t)
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	for _, instrument := range []struct {
		id, symbol string
	}{
		{"archived-only", "ARCHIVED"},
		{"shared-instrument", "SHARED"},
	} {
		if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, instrument.id, instrument.symbol, "USD", now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('refresh-portfolio',?,?)`, instrument.id, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key,lifecycle,removed_at,removed_by_category,removed_by_id) VALUES('archived-only-lot','refresh-portfolio','archived-only','1','USD',?,?, 'removed',?,'browser_user','local-owner')`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key,lifecycle,removed_at,removed_by_category,removed_by_id) VALUES('shared-removed-lot','refresh-portfolio','shared-instrument','1','USD',?,?, 'removed',?,'browser_user','local-owner')`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES('shared-active-lot','refresh-portfolio','shared-instrument','1','USD',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	targets, err := s.refreshTargets(context.Background(), "refresh-portfolio")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].InstrumentID != "refresh-instrument" || targets[1].InstrumentID != "shared-instrument" {
		t.Fatalf("targets=%+v", targets)
	}
}

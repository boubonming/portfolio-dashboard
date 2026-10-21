package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

type durableRefreshFake struct {
	quoteCalls atomic.Int32
	fxCalls    atomic.Int32
}

func (f *durableRefreshFake) Name() marketdata.Provider { return marketdata.ProviderFinnhub }
func (f *durableRefreshFake) Quote(_ context.Context, request marketdata.QuoteRequest) (domain.Quote, error) {
	f.quoteCalls.Add(1)
	now := time.Now().UTC().Add(-time.Minute)
	return domain.Quote{ID: "durable-quote", InstrumentID: request.InstrumentID, Symbol: request.Symbol, Price: "10", Currency: request.Currency, Source: "finnhub", MarketAt: now.Format("2006-01-02T15:04:05.000000000Z"), FetchedAt: now.Format("2006-01-02T15:04:05.000000000Z"), Basis: "close", Provenance: "durable-test"}, nil
}
func (f *durableRefreshFake) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	f.fxCalls.Add(1)
	now := time.Now().UTC().Add(-time.Minute)
	return domain.FXRate{ID: "durable-fx", BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "4", Source: "finnhub", MarketAt: now.Format("2006-01-02T15:04:05.000000000Z"), FetchedAt: now.Format("2006-01-02T15:04:05.000000000Z"), Basis: "close", Provenance: "durable-test"}, nil
}

type durableFXFake struct{ calls atomic.Int32 }

func (f *durableFXFake) Name() marketdata.Provider { return marketdata.ProviderOpenExchangeRates }
func (f *durableFXFake) Quote(context.Context, marketdata.QuoteRequest) (domain.Quote, error) {
	return domain.Quote{}, marketdata.ErrUnsupportedOperation
}
func (f *durableFXFake) FX(_ context.Context, request marketdata.FXRequest) (domain.FXRate, error) {
	f.calls.Add(1)
	now := time.Now().UTC().Add(-time.Minute)
	return domain.FXRate{ID: "durable-fx", BaseCurrency: request.BaseCurrency, QuoteCurrency: request.QuoteCurrency, Rate: "4", Source: "open_exchange_rates", MarketAt: now.Format("2006-01-02T15:04:05.000000000Z"), FetchedAt: now.Format("2006-01-02T15:04:05.000000000Z"), Basis: "close", Provenance: "durable-test"}, nil
}

// TestMarketRefreshCrashHelper is executed in a separate process by
// TestMarketRefreshReopensAfterSubprocessCrash. os.Exit deliberately skips all
// deferred cleanup so the parent proves SQLite recovery from an unclean exit.
func TestMarketRefreshCrashHelper(t *testing.T) {
	if os.Getenv("MARKET_REFRESH_CRASH_HELPER") != "1" {
		return
	}
	database, err := Open(context.Background(), os.Getenv("MARKET_REFRESH_CRASH_DB"))
	if err != nil {
		os.Exit(98)
	}
	phase := os.Getenv("MARKET_REFRESH_CRASH_PHASE")
	database.SetMarketRefreshFailureHook(func(point string) error {
		if point == phase {
			os.Exit(97)
		}
		return nil
	})
	options := MarketRefreshIdempotencyOptions{
		ActorCategory: "browser_user", ActorID: "crash-helper-user", PortfolioID: "crash-portfolio", IdempotencyKey: os.Getenv("MARKET_REFRESH_CRASH_KEY"), RequestFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Refresh:  RefreshOptions{PortfolioID: "crash-portfolio", ReportingCurrency: "MYR", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: &durableRefreshFake{}, marketdata.ProviderOpenExchangeRates: &durableFXFake{}}},
		Snapshot: SnapshotRequest{PortfolioID: "crash-portfolio", ReportingCurrency: "MYR", MaxAge: 24 * time.Hour},
	}
	_, _, _ = database.RefreshMarketDataIdempotent(context.Background(), options)
	os.Exit(99)
}

func TestMarketRefreshComposedWorkflowElectsOneOwnerAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "durable-refresh.sqlite")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")
	if _, err := first.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "durable-portfolio", "Durable", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "durable-instrument", "DUR", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "durable-portfolio", "durable-instrument", now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "durable-lot", "durable-portfolio", "durable-instrument", "1", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "durable-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "DUR", QuoteCurrency: "USD", ActiveFrom: now, Provenance: "durable-test"}); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()

	quoteClient := &durableRefreshFake{}
	fxClient := &durableFXFake{}
	options := func() MarketRefreshIdempotencyOptions {
		return MarketRefreshIdempotencyOptions{
			ActorCategory: "browser_user", ActorID: "durable-user", PortfolioID: "durable-portfolio", IdempotencyKey: "durable-key", RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Refresh:  RefreshOptions{PortfolioID: "durable-portfolio", ReportingCurrency: "MYR", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: quoteClient, marketdata.ProviderOpenExchangeRates: fxClient}},
			Snapshot: SnapshotRequest{PortfolioID: "durable-portfolio", ReportingCurrency: "MYR", MaxAge: 24 * time.Hour},
		}
	}
	results := make(chan struct {
		result domain.MarketRefreshResult
		replay bool
		err    error
	}, 2)
	var group sync.WaitGroup
	for _, database := range []*Store{first, second} {
		group.Add(1)
		go func(database *Store) {
			defer group.Done()
			result, replay, callErr := database.RefreshMarketDataIdempotent(ctx, options())
			results <- struct {
				result domain.MarketRefreshResult
				replay bool
				err    error
			}{result, replay, callErr}
		}(database)
	}
	group.Wait()
	close(results)
	var snapshotID string
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if !item.result.Complete || item.result.Snapshot.SnapshotID == "" {
			t.Fatalf("incomplete composed result: %+v", item.result)
		}
		if snapshotID == "" {
			snapshotID = item.result.Snapshot.SnapshotID
		} else if snapshotID != item.result.Snapshot.SnapshotID {
			t.Fatalf("owners did not converge: %s != %s", snapshotID, item.result.Snapshot.SnapshotID)
		}
	}
	if quoteClient.quoteCalls.Load() != 1 || fxClient.calls.Load() != 1 {
		t.Fatalf("duplicate provider calls: quotes=%d fx=%d", quoteClient.quoteCalls.Load(), fxClient.calls.Load())
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM quotes WHERE id='durable-quote'`,
		`SELECT COUNT(*) FROM fx_rates WHERE id='durable-fx'`,
		`SELECT COUNT(*) FROM snapshots WHERE id=?`,
		`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key='durable-key' AND state='committed'`,
	} {
		var count int
		var scanErr error
		if query == `SELECT COUNT(*) FROM snapshots WHERE id=?` {
			scanErr = first.DB.QueryRow(query, snapshotID).Scan(&count)
		} else {
			scanErr = first.DB.QueryRow(query).Scan(&count)
		}
		if scanErr != nil || count != 1 {
			t.Fatalf("effect count query=%q count=%d err=%v", query, count, scanErr)
		}
	}
}

func TestMarketRefreshReopensAfterSubprocessCrash(t *testing.T) {
	for _, phase := range []string{"after_reservation", "after_observation_commit", "before_snapshot_commit"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "crash.sqlite")
			initial, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			baseTime := time.Now().UTC()
			now := baseTime.Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"crash-portfolio", "Crash", "MYR", now, now}},
				{`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, []any{"crash-instrument", "CRASH", "USD", now}},
				{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"crash-portfolio", "crash-instrument", now}},
				{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"crash-lot", "crash-portfolio", "crash-instrument", "1", "USD", "active", now, now}},
			} {
				if _, err := initial.DB.Exec(statement.query, statement.args...); err != nil {
					initial.Close()
					t.Fatal(err)
				}
			}
			if _, err := initial.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "crash-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "CRASH", QuoteCurrency: "USD", ActiveFrom: now, Provenance: "crash-test"}); err != nil {
				initial.Close()
				t.Fatal(err)
			}
			initial.SetClock(func() time.Time { return baseTime })
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}

			key := "crash-key-" + phase
			command := exec.Command(os.Args[0], "-test.run=^TestMarketRefreshCrashHelper$", "-test.v")
			command.Env = append(os.Environ(), "MARKET_REFRESH_CRASH_HELPER=1", "MARKET_REFRESH_CRASH_DB="+path, "MARKET_REFRESH_CRASH_PHASE="+phase, "MARKET_REFRESH_CRASH_KEY="+key)
			if err := command.Run(); err == nil || command.ProcessState.ExitCode() != 97 {
				t.Fatalf("crash helper result=%v exit=%d", err, command.ProcessState.ExitCode())
			}

			database, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			database.SetClock(func() time.Time { return baseTime.Add(time.Minute) })
			quotes := &durableRefreshFake{}
			fx := &durableFXFake{}
			options := MarketRefreshIdempotencyOptions{
				ActorCategory: "browser_user", ActorID: "crash-helper-user", PortfolioID: "crash-portfolio", IdempotencyKey: key, RequestFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Refresh:  RefreshOptions{PortfolioID: "crash-portfolio", ReportingCurrency: "MYR", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: quotes, marketdata.ProviderOpenExchangeRates: fx}},
				Snapshot: SnapshotRequest{PortfolioID: "crash-portfolio", ReportingCurrency: "MYR", MaxAge: 24 * time.Hour},
			}
			result, replay, err := database.RefreshMarketDataIdempotent(ctx, options)
			if err != nil || replay || !result.Complete {
				t.Fatalf("reopened recovery result=%+v replay=%v err=%v", result, replay, err)
			}
			if phase != "after_reservation" && (quotes.quoteCalls.Load() != 0 || fx.calls.Load() != 0) {
				t.Fatalf("observation commit was repeated after %s: quotes=%d fx=%d", phase, quotes.quoteCalls.Load(), fx.calls.Load())
			}
			var pending, committed, snapshots, audits, activities, receiptSnapshots int
			queries := []struct {
				query string
				args  []any
				out   *int
			}{
				{`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=? AND state='pending'`, []any{key}, &pending},
				{`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=? AND state='committed'`, []any{key}, &committed},
				{`SELECT COUNT(*) FROM snapshots WHERE id=?`, []any{result.Snapshot.SnapshotID}, &snapshots},
				{`SELECT COUNT(*) FROM audit_events WHERE id=?`, []any{deterministicID("audit", "refresh", result.Refresh.RefreshID)}, &audits},
				{`SELECT COUNT(*) FROM activity_events WHERE id=?`, []any{deterministicID("activity", deterministicID("audit", "refresh", result.Refresh.RefreshID), "crash-portfolio")}, &activities},
				{`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=? AND state='committed' AND snapshot_id IN (SELECT id FROM snapshots)`, []any{key}, &receiptSnapshots},
			}
			for _, query := range queries {
				if err := database.DB.QueryRow(query.query, query.args...).Scan(query.out); err != nil {
					t.Fatal(err)
				}
			}
			if pending != 0 || committed != 1 || snapshots != 1 || audits != 1 || activities != 1 || receiptSnapshots != 1 {
				t.Fatalf("crash recovery effects pending=%d committed=%d snapshots=%d audits=%d activities=%d receiptSnapshots=%d", pending, committed, snapshots, audits, activities, receiptSnapshots)
			}
		})
	}
}

func TestMarketRefreshRecoveryAtDurableBoundaries(t *testing.T) {
	for _, failurePoint := range []string{
		"after_reservation",
		"before_observation_commit",
		"before_observation_tx_commit",
		"after_observation_commit",
		"before_snapshot_commit",
		"before_snapshot_tx_commit",
		"after_snapshot_commit",
	} {
		t.Run(failurePoint, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "boundary.sqlite")
			database, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = database.Close() }()
			baseTime := time.Now().UTC()
			database.SetClock(func() time.Time { return baseTime })
			now := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"boundary-portfolio", "Boundary", "MYR", now, now}},
				{`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, []any{"boundary-instrument", "BOUND", "USD", now}},
				{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"boundary-portfolio", "boundary-instrument", now}},
				{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, []any{"boundary-lot", "boundary-portfolio", "boundary-instrument", "1", "USD", now, now}},
			} {
				if _, err := database.DB.Exec(statement.query, statement.args...); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "boundary-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "BOUND", QuoteCurrency: "USD", ActiveFrom: now, Provenance: "boundary-test"}); err != nil {
				t.Fatal(err)
			}
			quotes := &durableRefreshFake{}
			fx := &durableFXFake{}
			didInject := atomic.Bool{}
			commitFailure := failurePoint == "before_observation_tx_commit" || failurePoint == "before_snapshot_tx_commit"
			if commitFailure {
				database.SetMarketRefreshCommitHook(func(point string) {
					if point == failurePoint {
						cancel()
					}
				})
			}
			database.SetMarketRefreshFailureHook(func(point string) error {
				if point == failurePoint && didInject.CompareAndSwap(false, true) {
					return errors.New("injected durable boundary failure")
				}
				return nil
			})
			options := MarketRefreshIdempotencyOptions{
				ActorCategory: "browser_user", ActorID: "boundary-user", PortfolioID: "boundary-portfolio", IdempotencyKey: "boundary-" + failurePoint, RequestFingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Refresh:  RefreshOptions{PortfolioID: "boundary-portfolio", ReportingCurrency: "MYR", Providers: []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderOpenExchangeRates}, Clients: map[marketdata.Provider]marketdata.ProviderClient{marketdata.ProviderFinnhub: quotes, marketdata.ProviderOpenExchangeRates: fx}},
				Snapshot: SnapshotRequest{PortfolioID: "boundary-portfolio", ReportingCurrency: "MYR", MaxAge: 24 * time.Hour},
			}
			if _, _, err := database.RefreshMarketDataIdempotent(ctx, options); err == nil {
				t.Fatal("failure injection unexpectedly succeeded")
			}
			if commitFailure {
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
				database, err = Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				database.SetClock(func() time.Time { return baseTime.Add(time.Minute) })
				ctx = context.Background()
			}
			database.SetMarketRefreshCommitHook(nil)
			database.SetMarketRefreshFailureHook(nil)
			result, replay, err := database.RefreshMarketDataIdempotent(ctx, options)
			if err != nil || !result.Complete {
				t.Fatalf("recovery result=%+v replay=%v err=%v", result, replay, err)
			}
			if failurePoint == "after_observation_commit" || failurePoint == "before_snapshot_commit" || failurePoint == "after_snapshot_commit" {
				if quotes.quoteCalls.Load() != 1 || fx.calls.Load() != 1 {
					t.Fatalf("committed observations were fetched again: quotes=%d fx=%d", quotes.quoteCalls.Load(), fx.calls.Load())
				}
			}
			var pending, committed, snapshots, audits, activities int
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=? AND state='pending'`, options.IdempotencyKey).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=? AND state='committed'`, options.IdempotencyKey).Scan(&committed); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE id=?`, result.Snapshot.SnapshotID).Scan(&snapshots); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE id=?`, deterministicID("audit", "refresh", result.Refresh.RefreshID)).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE id=?`, deterministicID("activity", deterministicID("audit", "refresh", result.Refresh.RefreshID), options.PortfolioID)).Scan(&activities); err != nil {
				t.Fatal(err)
			}
			if pending != 0 || committed != 1 || snapshots != 1 || audits != 1 || activities != 1 {
				t.Fatalf("recovery effects pending=%d committed=%d snapshots=%d audits=%d activities=%d", pending, committed, snapshots, audits, activities)
			}
		})
	}
}

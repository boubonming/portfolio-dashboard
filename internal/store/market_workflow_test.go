package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestMarketDataSetupAndBatchMappingsAreScopedAndAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "market.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "market-portfolio", "Market", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "market-instrument", "AAPL", "Apple Inc.", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "market-portfolio", "market-instrument", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "market-lot", "market-portfolio", "market-instrument", "1", "USD", "active", now, now); err != nil {
		t.Fatal(err)
	}
	setup, err := s.MarketDataSetup(ctx, "market-portfolio", []domain.ProviderReadiness{{Name: "finnhub"}})
	if err != nil || len(setup.Instruments) != 1 || len(setup.Instruments[0].Mappings) != 0 {
		t.Fatalf("setup=%+v err=%v", setup, err)
	}
	result, err := s.AddProviderMappingsBatch(ctx, "market-portfolio", setup.ETag, []domain.MappingBatchItem{{InstrumentID: "market-instrument", Provider: "finnhub", ProviderSymbol: "AAPL", QuoteCurrency: "USD", Provenance: "provider documentation", ConfirmUnverified: true}})
	if err != nil || result.Status != "saved" || len(result.Outcomes) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	updated, err := s.MarketDataSetup(ctx, "market-portfolio", nil)
	if err != nil || len(updated.Instruments[0].Mappings) != 1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err := s.AddProviderMappingsBatch(ctx, "market-portfolio", setup.ETag, []domain.MappingBatchItem{{InstrumentID: "market-instrument", Provider: "finnhub", ProviderSymbol: "MSFT", QuoteCurrency: "USD", Provenance: "stale", ConfirmUnverified: true}}); err == nil {
		t.Fatal("stale mapping batch unexpectedly succeeded")
	}
}

func TestMarketMappingBatchRequiresConfirmationAndRollsBackBeforeCommit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "market-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "market-rollback", "Market", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "market-rollback-instrument", "ROLL", "Rollback", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "market-rollback", "market-rollback-instrument", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "market-rollback-lot", "market-rollback", "market-rollback-instrument", "1", "USD", "active", now, now); err != nil {
		t.Fatal(err)
	}
	setup, err := s.MarketDataSetup(ctx, "market-rollback", nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := domain.MappingBatchItem{InstrumentID: "market-rollback-instrument", Provider: "finnhub", ProviderSymbol: "ROLL", QuoteCurrency: "USD", Provenance: "verified listing", ConfirmUnverified: true}
	if _, err := s.AddProviderMappingsBatch(ctx, "market-rollback", setup.ETag, []domain.MappingBatchItem{{
		InstrumentID: confirmed.InstrumentID, Provider: confirmed.Provider, ProviderSymbol: confirmed.ProviderSymbol, QuoteCurrency: confirmed.QuoteCurrency, Provenance: confirmed.Provenance,
	}}); err == nil {
		t.Fatal("unconfirmed mapping unexpectedly succeeded")
	}
	if _, err := s.AddProviderMappingsBatch(ctx, "market-rollback", setup.ETag, []domain.MappingBatchItem{confirmed, {InstrumentID: "not-in-portfolio", Provider: "finnhub", ProviderSymbol: "OTHER", QuoteCurrency: "USD", Provenance: "verified", ConfirmUnverified: true}}); err == nil {
		t.Fatal("invalid second mapping unexpectedly succeeded")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("atomic batch wrote %d mappings", count)
	}
}

func TestMarketRefreshReceiptIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "receipt.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "receipt-portfolio", "Receipt", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO market_refresh_idempotency(actor_category,actor_id,portfolio_id,operation,idempotency_key,request_fingerprint,state,phase,response_payload,created_at,committed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "browser_user", "receipt-user", "receipt-portfolio", "refresh", "receipt-key", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "committed", "committed", `{}`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`DELETE FROM market_refresh_idempotency WHERE actor_id=?`, "receipt-user"); err == nil {
		t.Fatal("committed receipt delete unexpectedly succeeded")
	}
	if _, err := s.DB.Exec(`UPDATE market_refresh_idempotency SET response_payload=? WHERE actor_id=?`, `{"changed":true}`, "receipt-user"); err == nil {
		t.Fatal("committed receipt update unexpectedly succeeded")
	}
}

func TestMarketRefreshReservationConvergesAcrossSeparateStores(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "separate-stores.sqlite")
	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "separate-store-portfolio", "Separate", "MYR", "2026-01-01T00:00:00.000000000Z", "2026-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	options := MarketRefreshIdempotencyOptions{ActorCategory: "browser_user", ActorID: "separate-store-user", PortfolioID: "separate-store-portfolio", IdempotencyKey: "separate-store-key", RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, database := range []*Store{first, second} {
		group.Add(1)
		go func(database *Store) {
			defer group.Done()
			_, err := database.readOrReserveMarketRefresh(ctx, options)
			results <- err
		}(database)
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("separate store reservation returned raw contention error: %v", err)
		}
	}
	var count int
	if err := first.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_idempotency WHERE idempotency_key=?`, options.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reservation count=%d err=%v", count, err)
	}
}

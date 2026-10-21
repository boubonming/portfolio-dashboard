package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
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

func TestMarketDataSetupUsesStableEmptyMappingsForMixedPortfolioScope(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "market-mixed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"market-mixed", "Mixed", "MYR", now, now}},
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"market-foreign", "Foreign", "MYR", now, now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-mapped", "AAA", "Mapped", "USD", now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-unmapped", "BBB", "Unmapped", "USD", now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-foreign-instrument", "CCC", "Foreign", "USD", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-mixed", "market-mapped", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-mixed", "market-unmapped", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-foreign", "market-foreign-instrument", now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-mapped-lot", "market-mixed", "market-mapped", "1", "USD", "active", now, now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-unmapped-lot", "market-mixed", "market-unmapped", "1", "USD", "active", now, now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-foreign-lot", "market-foreign", "market-foreign-instrument", "1", "USD", "active", now, now}},
		{`INSERT INTO instrument_provider_mappings(id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-mapping", "market-mapped", "coingecko", "aaa", "USD", now, "verified fixture", now}},
	} {
		if _, err := s.DB.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	providers := MarketProviders(map[marketdata.Provider]bool{marketdata.ProviderFinnhub: true})
	setup, err := s.MarketDataSetup(ctx, "market-mixed", providers)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.MarketDataSetup(ctx, "market-mixed", providers)
	if err != nil {
		t.Fatal(err)
	}
	if setup.ETag == "" || setup.ETag != repeated.ETag || len(setup.Instruments) != 2 {
		t.Fatalf("setup is not deterministic or correctly scoped: setup=%+v repeated=%+v", setup, repeated)
	}
	if setup.Instruments[0].InstrumentID != "market-mapped" || len(setup.Instruments[0].Mappings) != 1 || setup.Instruments[1].InstrumentID != "market-unmapped" || setup.Instruments[1].Mappings == nil || len(setup.Instruments[1].Mappings) != 0 {
		t.Fatalf("mixed mapping state=%+v", setup.Instruments)
	}
	var finnhubConfigured, coinGeckoPublic bool
	for _, provider := range setup.Providers {
		if provider.Name == marketdata.ProviderFinnhub.String() {
			finnhubConfigured = provider.Configured
		}
		if provider.Name == marketdata.ProviderCoinGecko.String() {
			coinGeckoPublic = provider.Public && provider.Configured
		}
	}
	if !finnhubConfigured || !coinGeckoPublic || len(setup.Providers) != 4 {
		t.Fatalf("provider readiness changed: %+v", setup.Providers)
	}
	payload, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"mappings":[]`) || strings.Contains(string(payload), `"mappings":null`) || strings.Contains(string(payload), "market-foreign-instrument") {
		t.Fatalf("invalid mixed setup payload: %s", payload)
	}
	if _, err := s.AddProviderMappingsBatch(ctx, "market-mixed", setup.ETag, []domain.MappingBatchItem{{InstrumentID: "market-unmapped", Provider: "finnhub", ProviderSymbol: "BBB", QuoteCurrency: "USD", Provenance: "verified fixture", ConfirmUnverified: true}}); err == nil {
		t.Fatal("partial mixed mapping batch unexpectedly succeeded")
	}
}

func TestMarketMappingBatchRequiresExactActiveInstrumentSet(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "market-multi.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"market-multi", "Market", "MYR", now, now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-multi-a", "AAA", "First", "USD", now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"market-multi-b", "BBB", "Second", "USD", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-multi", "market-multi-a", now}},
		{`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, []any{"market-multi", "market-multi-b", now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-multi-lot-a", "market-multi", "market-multi-a", "1", "USD", "active", now, now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,lifecycle,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, []any{"market-multi-lot-b", "market-multi", "market-multi-b", "1", "USD", "active", now, now}},
	} {
		if _, err := s.DB.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	setup, err := s.MarketDataSetup(ctx, "market-multi", nil)
	if err != nil {
		t.Fatal(err)
	}
	item := func(id, symbol string) domain.MappingBatchItem {
		return domain.MappingBatchItem{InstrumentID: id, Provider: "finnhub", ProviderSymbol: symbol, QuoteCurrency: "USD", Provenance: "verified listing", ConfirmUnverified: true}
	}
	if _, err := s.AddProviderMappingsBatch(ctx, "market-multi", setup.ETag, []domain.MappingBatchItem{item("market-multi-a", "AAA")}); err == nil {
		t.Fatal("partial mapping batch unexpectedly succeeded")
	}
	var mappings, audits, activities int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 || audits != 0 || activities != 0 {
		t.Fatalf("partial mapping batch changed state: mappings=%d audits=%d activities=%d", mappings, audits, activities)
	}
	result, err := s.AddProviderMappingsBatch(ctx, "market-multi", setup.ETag, []domain.MappingBatchItem{item("market-multi-a", "AAA"), item("market-multi-b", "BBB")})
	if err != nil || len(result.Outcomes) != 2 {
		t.Fatalf("complete mapping batch result=%+v err=%v", result, err)
	}
	if result.ETag == setup.ETag {
		t.Fatal("complete mapping batch did not advance setup ETag")
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if mappings != 2 || audits != 2 || activities != 2 {
		t.Fatalf("complete mapping batch effects: mappings=%d audits=%d activities=%d", mappings, audits, activities)
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

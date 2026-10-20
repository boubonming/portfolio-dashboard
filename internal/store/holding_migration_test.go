package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestHoldingMigrationInitializesCanonicalDefaultAndLifecycleSchema(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "holdings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var name, currency string
	var revision, lotCount, instrumentCount int
	if err := database.DB.QueryRow(`SELECT name,reporting_currency,holdings_revision FROM portfolios WHERE id='portfolio-default'`).Scan(&name, &currency, &revision); err != nil {
		t.Fatal(err)
	}
	if name != "Portfolio" || currency != "MYR" || revision != 0 {
		t.Fatalf("default portfolio = %q/%q/%d", name, currency, revision)
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-default'`).Scan(&lotCount); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM portfolio_instruments WHERE portfolio_id='portfolio-default'`).Scan(&instrumentCount); err != nil {
		t.Fatal(err)
	}
	if lotCount != 0 || instrumentCount != 0 {
		t.Fatalf("fresh default rows = lots:%d instruments:%d", lotCount, instrumentCount)
	}

	for _, table := range []string{"portfolio_instruments", "holding_mutation_idempotency"} {
		var count int
		if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing canonical table %s", table)
		}
	}
}

func TestHoldingMigrationRetiresImportSchemaAndRejectsUnassociatedInstrumentWithoutSideEffects(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding-scope.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, table := range []string{"import_" + "runs", "import_" + "items"} {
		var count int
		if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("import-only table %s remains active", table)
		}
	}
	for _, column := range []string{"source_" + "row", "source_" + "ref", "source_" + "sha256"} {
		var count int
		if err := database.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('lots') WHERE name=?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("historical provenance column %s remains on canonical lots", column)
		}
	}
	now := "2026-01-02T12:00:00.000000000Z"
	for _, portfolio := range []string{"scope-a", "scope-b"} {
		if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "scope-instrument", "SCOPE", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "scope-a", "scope-instrument", now); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT COUNT(*) FROM lots`,
		`SELECT COUNT(*) FROM portfolio_instruments`,
		`SELECT holdings_revision FROM portfolios WHERE id='scope-b'`,
		`SELECT COUNT(*) FROM audit_events`,
		`SELECT COUNT(*) FROM activity_events`,
		`SELECT COUNT(*) FROM holding_mutation_idempotency`,
	}
	before := make([]int, len(queries))
	for i, query := range queries {
		if err := database.DB.QueryRow(query).Scan(&before[i]); err != nil {
			t.Fatal(err)
		}
	}
	_, err = database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "scope-cross-lot", "scope-b", "scope-instrument", "1", "USD", now, now)
	if err == nil {
		t.Fatal("cross-portfolio lot insertion unexpectedly succeeded")
	}
	for i, query := range queries {
		var got int
		if err := database.DB.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != before[i] {
			t.Fatalf("%s changed from %d to %d", query, before[i], got)
		}
	}
}

func TestHoldingMigrationRejectsNonCanonicalNewAndRemovedTimestampsAndActors(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding-time.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "time-portfolio", "Time", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "time-instrument", "TIME", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "time-portfolio", "time-instrument", now); err != nil {
		t.Fatal(err)
	}
	for ordinal, createdAt := range []string{
		"2026-01-02T12:00:00Z", "2026-01-02T12:00:00.1Z", "2026-01-02T12:00:00+00:00",
		"2026-01-02T12:00:00.000000000+00:00", "2026-02-30T12:00:00.000000000Z", "2026-01-02T12:00:00.0000000000Z",
	} {
		_, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "bad-time-"+string(rune('a'+ordinal)), "time-portfolio", "time-instrument", "1", "USD", createdAt, createdAt)
		if err == nil {
			t.Fatalf("non-canonical created_at %q was accepted", createdAt)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "time-lot", "time-portfolio", "time-instrument", "1", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	for _, removedAt := range []string{"bad", "2026-01-02T11:59:59.000000000Z", "2026-01-02T12:00:00+00:00", "2026-02-30T12:00:00.000000000Z"} {
		_, err := database.DB.Exec(`UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id='time-lot'`, removedAt, "browser_user", "local-owner")
		if err == nil {
			t.Fatalf("invalid removed_at %q was accepted", removedAt)
		}
	}
	for category, actorID := range map[string]string{"bogus": "local-owner", "browser_user": "bad actor", "machine": "-bad"} {
		_, err := database.DB.Exec(`UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id='time-lot'`, "2026-01-02T12:00:00.000000000Z", category, actorID)
		if err == nil {
			t.Fatalf("invalid removal actor %q/%q was accepted", category, actorID)
		}
	}
	if _, err := database.DB.Exec(`UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id='time-lot'`, "2026-01-02T12:00:00.000000001Z", "browser_user", "local-owner"); err != nil {
		t.Fatal(err)
	}
}

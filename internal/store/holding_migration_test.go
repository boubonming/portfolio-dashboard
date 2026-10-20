package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
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

func TestHoldingMigrationGuardsAllPost010LotUpdatesWithoutSideEffects(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding-update-guards.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const now = "2026-01-02T12:00:00.000000000Z"
	for _, portfolio := range []string{"guard-a", "guard-b"} {
		if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, instrument := range []string{"guard-instrument-a", "guard-instrument-b"} {
		if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, instrument, instrument, "USD", now); err != nil {
			t.Fatal(err)
		}
	}
	for _, association := range [][2]string{{"guard-a", "guard-instrument-a"}, {"guard-b", "guard-instrument-b"}} {
		if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, association[0], association[1], now); err != nil {
			t.Fatal(err)
		}
	}
	for _, account := range [][2]string{{"guard-account-a", "guard-a"}, {"guard-account-b", "guard-b"}} {
		if _, err := database.DB.Exec(`INSERT INTO accounts(id,portfolio_id,broker,created_at) VALUES(?,?,?,?)`, account[0], account[1], "Guard Broker", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "guard-lot", "guard-a", "guard-instrument-a", "guard-account-a", "1", "2", "2", "USD", "2025-01-01", now, now); err != nil {
		t.Fatal(err)
	}

	queries := []string{
		`SELECT id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,created_at,lifecycle,removed_at,removed_by_category,removed_by_id,created_at_order_key FROM lots WHERE id='guard-lot'`,
		`SELECT COUNT(*) FROM portfolio_instruments`,
		`SELECT holdings_revision FROM portfolios WHERE id='guard-a'`,
		`SELECT COUNT(*) FROM audit_events`,
		`SELECT COUNT(*) FROM activity_events`,
		`SELECT COUNT(*) FROM holding_mutation_idempotency`,
	}
	before := snapshotHoldingGuardState(t, database.DB, queries)
	for _, mutation := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "foreign instrument reassignment", query: `UPDATE lots SET instrument_id=? WHERE id='guard-lot'`, args: []any{"guard-instrument-b"}},
		{name: "foreign portfolio reassignment", query: `UPDATE lots SET portfolio_id=?,instrument_id=? WHERE id='guard-lot'`, args: []any{"guard-b", "guard-instrument-a"}},
		{name: "active archival field rewrite", query: `UPDATE lots SET portfolio_id=?,instrument_id=?,account_id=?,quantity=?,unit_cost=?,cost_basis=?,currency=?,acquisition_date=?,created_at=?,created_at_order_key=?,lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id='guard-lot'`, args: []any{"guard-b", "guard-instrument-b", "guard-account-b", "9", "8", "72", "EUR", "2026-02-02", "2026-01-03T12:00:00.000000000Z", "2026-01-03T12:00:00.000000000Z", "2026-01-03T12:00:00.000000000Z", "machine", "guard-machine"}},
		{name: "active created timestamp rewrite", query: `UPDATE lots SET created_at=?,created_at_order_key=? WHERE id='guard-lot'`, args: []any{"2026-01-03T12:00:00.000000000Z", "2026-01-03T12:00:00.000000000Z"}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			tx, err := database.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(mutation.query, mutation.args...); err == nil {
				tx.Rollback()
				t.Fatal("guarded update unexpectedly succeeded")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertHoldingGuardState(t, database.DB, queries, before)
		})
	}

	if _, err := database.DB.Exec(`UPDATE lots SET portfolio_id=?,instrument_id=?,account_id=?,quantity=?,unit_cost=?,cost_basis=?,acquisition_date=? WHERE id='guard-lot'`, "guard-b", "guard-instrument-b", "guard-account-b", "3", "4", "12", "2025-02-01"); err != nil {
		t.Fatalf("authorized pre-associated update failed: %v", err)
	}
	if _, err := database.DB.Exec(`UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id='guard-lot'`, "2026-01-03T12:00:00.000000000Z", "machine", "guard-machine"); err != nil {
		t.Fatalf("authorized archival update failed: %v", err)
	}
	archived := snapshotHoldingGuardState(t, database.DB, queries)
	for _, mutation := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "archived quantity rewrite", query: `UPDATE lots SET quantity=? WHERE id='guard-lot'`, args: []any{"99"}},
		{name: "archived identity rewrite", query: `UPDATE lots SET instrument_id=? WHERE id='guard-lot'`, args: []any{"guard-instrument-b"}},
		{name: "archived creation timestamp rewrite", query: `UPDATE lots SET created_at=?,created_at_order_key=? WHERE id='guard-lot'`, args: []any{"2026-01-04T12:00:00.000000000Z", "2026-01-04T12:00:00.000000000Z"}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			tx, err := database.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(mutation.query, mutation.args...); err == nil {
				tx.Rollback()
				t.Fatal("archived update unexpectedly succeeded")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertHoldingGuardState(t, database.DB, queries, archived)
		})
	}
}

func snapshotHoldingGuardState(t *testing.T, database *sql.DB, queries []string) []string {
	t.Helper()
	state := make([]string, len(queries))
	var fields [15]sql.NullString
	if err := database.QueryRow(queries[0]).Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &fields[5], &fields[6], &fields[7], &fields[8], &fields[9], &fields[10], &fields[11], &fields[12], &fields[13], &fields[14]); err != nil {
		t.Fatal(err)
	}
	values := make([]string, len(fields))
	for i, field := range fields {
		values[i] = field.String + ":" + boolString(field.Valid)
	}
	state[0] = strings.Join(values, ";")
	for i, query := range queries[1:] {
		var value int
		if err := database.QueryRow(query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		state[i+1] = strconv.Itoa(value)
	}
	return state
}

func assertHoldingGuardState(t *testing.T, database *sql.DB, queries, want []string) {
	t.Helper()
	got := snapshotHoldingGuardState(t, database, queries)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("state query %d changed from %q to %q", i, want[i], got[i])
		}
	}
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

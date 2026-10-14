package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func openThrough007(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "phase-d.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "008_") {
			break
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			db.Close()
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", entry.Name(), "2026-01-01T00:00:00.000000000Z"); err != nil {
			tx.Rollback()
			db.Close()
			t.Fatalf("record migration %s: %v", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db, ctx
}

func seedLegacyActivity(t *testing.T, db *sql.DB, twoPortfolios bool) {
	t.Helper()
	now := "2020-01-02T12:00:00.000000000Z"
	for _, portfolio := range []string{"legacy-a", "legacy-b"} {
		if portfolio == "legacy-b" && !twoPortfolios {
			break
		}
		if _, err := db.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "legacy-instrument", "LEG", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at) VALUES(?,?,?,?,?)`, "legacy-snapshot", "legacy-a", "v1", `{}`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, "legacy-audit", "system", "legacy.action", "legacy", "legacy-entity", `{}`, now); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,sort_time,source_audit_event_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`
	if _, err := db.Exec(insert, "legacy-null", "legacy-a", "system", "legacy.action", "legacy", "legacy", "null-source", "kept", "", now, now, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "legacy-source", "legacy-a", "system", "legacy.action", "legacy", "legacy", "source", "kept", "", now, now, "legacy-audit"); err != nil {
		t.Fatal(err)
	}
	if twoPortfolios {
		if _, err := db.Exec(insert, "legacy-source-b", "legacy-b", "system", "legacy.action", "legacy", "legacy", "source", "kept", "", now, now, "legacy-audit"); err != nil {
			t.Fatal(err)
		}
	}
}

func apply008(t *testing.T, db *sql.DB, ctx context.Context) error {
	t.Helper()
	body, err := migrationFiles.ReadFile("migrations/008_scoped_reviews.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, string(body)); err != nil {
		tx.Rollback()
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", "008_scoped_reviews.sql", "2026-01-01T00:00:00.000000000Z"); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestPhaseDMigrationPreservesAndBackfillsLegacyActivity(t *testing.T) {
	db, ctx := openThrough007(t)
	defer db.Close()
	seedLegacyActivity(t, db, false)
	if err := apply008(t, db, ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("legacy activity count = %d, want 2", count)
	}
	var source, retain string
	if err := db.QueryRow(`SELECT source_audit_event_id,retain_until FROM activity_events WHERE id='legacy-source'`).Scan(&source, &retain); err != nil {
		t.Fatal(err)
	}
	if source != "legacy-audit" || retain != "2027-01-02T12:00:00.000000000Z" {
		t.Fatalf("source activity = %q retain=%q", source, retain)
	}
	var nullSource sql.NullString
	if err := db.QueryRow(`SELECT source_audit_event_id FROM activity_events WHERE id='legacy-null'`).Scan(&nullSource); err != nil {
		t.Fatal(err)
	}
	if nullSource.Valid {
		t.Fatalf("nullable legacy source was rewritten: %q", nullSource.String)
	}
	var ownerRetention sql.NullString
	if err := db.QueryRow(`SELECT retain_until FROM audit_event_portfolios WHERE audit_event_id='legacy-audit' AND portfolio_id='legacy-a'`).Scan(&ownerRetention); err != nil {
		t.Fatal(err)
	}
	if !ownerRetention.Valid || ownerRetention.String != retain {
		t.Fatalf("audit owner retention = %v, want %q", ownerRetention, retain)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("foreign key violations = %d", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_temp_master WHERE name LIKE '_phase_d_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("temporary migration tables remain: %d", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version='008_scoped_reviews.sql'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("008 migration rows = %d", count)
	}
}

func TestPhaseDMigrationRollsBackOnLegacyActivityOwnershipConflict(t *testing.T) {
	db, ctx := openThrough007(t)
	defer db.Close()
	seedLegacyActivity(t, db, true)
	if err := apply008(t, db, ctx); err == nil {
		t.Fatal("migration unexpectedly accepted one audit source in two portfolios")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("migration rows after rollback = %d, want 7", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='advisory_reviews'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("Phase D tables survived failed migration")
	}
}

func TestPhaseDDecimalStringMatrixRoundTripsExactly(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "decimal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	accepted := []string{"0", "-0", "1", "-1", "10", "-10", "0.1", "-0.1"}
	rejected := []string{"", "-", "+1", "01", "-01", "00", "1.", ".1", "1.2.3", "1e3", "NaN", " 1", "1 ", "١", "--1", "-.", "-0.", "0.", "00.1", "01.2", "1\n", "1/2", strings.Repeat("9", 129)}
	asOf := "2026-01-02T12:00:00.000000000Z"
	for ordinal, value := range accepted {
		if _, err := s.DB.Exec(`INSERT INTO review_metrics(review_id,portfolio_id,ordinal,name,value,unit,as_of,source,deterministic) VALUES(?,?,?,?,?,?,?,?,?)`, "decimal", "decimal", ordinal, fmt.Sprintf("accepted-%d", ordinal), value, "unit", asOf, "submitted", 1); err != nil {
			t.Fatalf("accepted %q: %v", value, err)
		}
		var roundTrip string
		if err := s.DB.QueryRow(`SELECT value FROM review_metrics WHERE review_id='decimal' AND ordinal=?`, ordinal).Scan(&roundTrip); err != nil {
			t.Fatal(err)
		}
		if roundTrip != value {
			t.Fatalf("%q round-tripped as %q", value, roundTrip)
		}
	}
	for ordinal, value := range rejected {
		if _, err := s.DB.Exec(`INSERT INTO review_metrics(review_id,portfolio_id,ordinal,name,value,unit,as_of,source,deterministic) VALUES(?,?,?,?,?,?,?,?,?)`, "decimal", "decimal", ordinal+len(accepted), fmt.Sprintf("rejected-%d", ordinal), value, "unit", asOf, "submitted", 1); err == nil {
			t.Fatalf("rejected %q was accepted", value)
		}
	}
	if len(accepted) != 8 || len(rejected) != 23 {
		t.Fatalf("matrix counts accepted=%d rejected=%d", len(accepted), len(rejected))
	}
}

func TestRecordActivityBackfillsSourceAuditRetentionAndOwnership(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "activity-retention.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "retention-portfolio", "Retention", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, "retention-audit", "system", "legacy.action", "legacy", "legacy-entity", `{}`, now); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordActivity(context.Background(), tx, "retention-portfolio", "system", "legacy.action", "legacy", "legacy", "entity", "summary", "details", now, "retention-audit"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var activityRetention, auditRetention, ownerRetention string
	if err := s.DB.QueryRow(`SELECT retain_until FROM activity_events WHERE source_audit_event_id='retention-audit'`).Scan(&activityRetention); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT retain_until FROM audit_events WHERE id='retention-audit'`).Scan(&auditRetention); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT retain_until FROM audit_event_portfolios WHERE audit_event_id='retention-audit' AND portfolio_id='retention-portfolio'`).Scan(&ownerRetention); err != nil {
		t.Fatal(err)
	}
	if activityRetention != "2033-01-02T12:00:00.000000000Z" || auditRetention != activityRetention || ownerRetention != activityRetention {
		t.Fatalf("retention activity=%q audit=%q owner=%q", activityRetention, auditRetention, ownerRetention)
	}
}

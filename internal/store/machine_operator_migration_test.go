package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func openThrough010(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "machine-operator-migration.sqlite"))
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
		if strings.HasPrefix(entry.Name(), "010_") {
			break
		}
	}
	return db, ctx
}

func TestMachineOperatorMigrationUpgradesExistingTenMigrationDatabase(t *testing.T) {
	database, ctx := openThrough010(t)
	defer database.Close()
	const now = "2026-01-02T12:00:00.000000000Z"
	if _, err := database.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, "legacy-audit", "system", "legacy_action", "legacy", "legacy-entity", `{"kept":true}`, now, "2033-01-02T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 15 {
		t.Fatalf("migration count after upgrade=%d", migrationCount)
	}
	var phaseColumn, deleteTrigger, immutableTrigger int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('market_refresh_idempotency') WHERE name='phase'`).Scan(&phaseColumn); err != nil || phaseColumn != 1 {
		t.Fatalf("idempotency phase column=%d err=%v", phaseColumn, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('market_refresh_idempotency_no_delete','market_refresh_idempotency_committed_immutable')`).Scan(&deleteTrigger); err != nil || deleteTrigger != 2 {
		t.Fatalf("idempotency triggers=%d err=%v", deleteTrigger, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='market_refresh_idempotency'`).Scan(&immutableTrigger); err != nil || immutableTrigger != 1 {
		t.Fatalf("idempotency table=%d err=%v", immutableTrigger, err)
	}
	var action, payload string
	if err := database.QueryRow(`SELECT action,payload FROM audit_events WHERE id='legacy-audit'`).Scan(&action, &payload); err != nil {
		t.Fatal(err)
	}
	if action != "legacy_action" || payload != `{"kept":true}` {
		t.Fatalf("legacy audit changed to action=%q payload=%q", action, payload)
	}
	if _, err := database.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, "operator-audit", "operator", "machine_holdings_grant", "machine_principal", "operator-machine", `{"portfolio_id":"portfolio-default"}`, now, "2033-01-02T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, "operator-audit", "portfolio-default", "2033-01-02T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE audit_events SET payload=? WHERE id=?`, `{"changed":true}`, "operator-audit"); err == nil {
		t.Fatal("upgraded operator audit was mutable")
	}
	if _, err := database.Exec(`DELETE FROM audit_event_portfolios WHERE audit_event_id=?`, "operator-audit"); err == nil {
		t.Fatal("upgraded operator audit owner was deletable")
	}
	var foreignKeyErrors int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyErrors); err != nil {
		t.Fatal(err)
	}
	if foreignKeyErrors != 0 {
		t.Fatalf("foreign_key_check rows=%d", foreignKeyErrors)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 15 {
		t.Fatalf("migration count after repeat=%d", migrationCount)
	}
}

func TestMachineOperatorMigrationRollsBackAtomicallyOnTriggerConflict(t *testing.T) {
	database, ctx := openThrough010(t)
	defer database.Close()
	if _, err := database.Exec(`CREATE TRIGGER machine_operator_audit_owner_insert_guard BEFORE INSERT ON audit_event_portfolios BEGIN SELECT RAISE(ABORT,'pre-existing trigger'); END`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err == nil {
		t.Fatal("migration unexpectedly succeeded with a conflicting trigger")
	}
	var migrationCount, createdCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 10 {
		t.Fatalf("migration rows after rollback=%d", migrationCount)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='machine_operator_audit_append_only_update'`).Scan(&createdCount); err != nil {
		t.Fatal(err)
	}
	if createdCount != 0 {
		t.Fatal("partial operator audit triggers survived failed migration")
	}
	if _, err := database.Exec(`DROP TRIGGER machine_operator_audit_owner_insert_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 15 {
		t.Fatalf("migration rows after retry=%d", migrationCount)
	}
}

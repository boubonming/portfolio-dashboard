package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pragmas := []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"}
	for _, pragma := range pragmas {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err = Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		version := entry.Name()
		var exists int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + version)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))", version); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	// 005 predates activity on databases created by the merged base. Keep the
	// schema upgrade safe for those databases without rewriting any rows.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS activity_events (
		id TEXT PRIMARY KEY,
		portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
		actor_category TEXT NOT NULL,
		action TEXT NOT NULL,
		category TEXT NOT NULL,
		entity_type TEXT,
		entity_id TEXT,
		summary TEXT NOT NULL,
		details TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL,
		source_audit_event_id TEXT REFERENCES audit_events(id)
	)`); err != nil {
		return fmt.Errorf("ensure activity table: %w", err)
	}
	for _, index := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_activity_source_portfolio ON activity_events(source_audit_event_id, portfolio_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_portfolio_order ON activity_events(portfolio_id, occurred_at DESC, id DESC)`,
	} {
		if _, err := db.ExecContext(ctx, index); err != nil {
			return fmt.Errorf("ensure activity index: %w", err)
		}
	}
	return nil
}

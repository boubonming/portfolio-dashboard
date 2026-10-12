package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	DB        *sql.DB
	cursorKey []byte
	clock     func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate activity cursor key: %w", err)
	}
	return openWithActivityCursorKey(ctx, path, key)
}

// OpenWithActivityCursorSecret keeps opaque activity cursors stable across
// process restarts. Production callers should derive this from their existing
// high-entropy session secret rather than introducing another configuration
// secret.
func OpenWithActivityCursorSecret(ctx context.Context, path string, secret []byte) (*Store, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("activity cursor secret must contain at least 32 bytes")
	}
	return openWithActivityCursorKey(ctx, path, secret)
}

func openWithActivityCursorKey(ctx context.Context, path string, key []byte) (*Store, error) {
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
	return &Store{DB: db, cursorKey: append([]byte(nil), key...), clock: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock provides deterministic time to validation tests and callers that
// coordinate several writes against one logical clock.
func (s *Store) SetClock(clock func() time.Time) {
	if clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
		return
	}
	s.clock = clock
}

func (s *Store) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

// SetActivityCursorSecret is used by the authenticated HTTP constructor. It
// intentionally copies the derived key and never persists or logs it.
func (s *Store) SetActivityCursorSecret(secret []byte) error {
	if len(secret) < 32 {
		return fmt.Errorf("activity cursor secret must contain at least 32 bytes")
	}
	s.cursorKey = append(s.cursorKey[:0], secret...)
	return nil
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
	if err := verifyActivitySchema(ctx, db); err != nil {
		return fmt.Errorf("canonical activity schema verification failed: %w", err)
	}
	return nil
}

func verifyActivitySchema(ctx context.Context, db *sql.DB) error {
	expectedColumns := []struct {
		name, typ   string
		notnull, pk int
	}{
		{"id", "TEXT", 0, 1}, {"portfolio_id", "TEXT", 1, 0}, {"actor_category", "TEXT", 1, 0},
		{"action", "TEXT", 1, 0}, {"category", "TEXT", 1, 0}, {"entity_type", "TEXT", 0, 0},
		{"entity_id", "TEXT", 0, 0}, {"summary", "TEXT", 1, 0}, {"details", "TEXT", 1, 0},
		{"occurred_at", "TEXT", 1, 0}, {"sort_time", "TEXT", 1, 0}, {"source_audit_event_id", "TEXT", 0, 0},
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(activity_events)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for index, expected := range expectedColumns {
		if !rows.Next() {
			return fmt.Errorf("activity_events column %q is missing", expected.name)
		}
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return err
		}
		if cid != index || name != expected.name || strings.ToUpper(typ) != expected.typ || notnull != expected.notnull || pk != expected.pk {
			return fmt.Errorf("activity_events column %d is %s/%s notnull=%d pk=%d, expected %s/%s notnull=%d pk=%d", index, name, typ, notnull, pk, expected.name, expected.typ, expected.notnull, expected.pk)
		}
	}
	if rows.Next() {
		return errors.New("activity_events has unexpected columns")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var foreignKeyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('activity_events') WHERE "table"='portfolios' AND "from"='portfolio_id' AND "to"='id'`).Scan(&foreignKeyCount); err != nil {
		return err
	}
	if foreignKeyCount != 1 {
		return errors.New("activity_events portfolio foreign key is missing or incorrect")
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('activity_events') WHERE "table"='audit_events' AND "from"='source_audit_event_id' AND "to"='id'`).Scan(&foreignKeyCount); err != nil {
		return err
	}
	if foreignKeyCount != 1 {
		return errors.New("activity_events audit foreign key is missing or incorrect")
	}
	var unique int
	if err := db.QueryRowContext(ctx, `SELECT "unique" FROM pragma_index_list('activity_events') WHERE name='idx_activity_source_portfolio'`).Scan(&unique); err != nil {
		return fmt.Errorf("activity source index: %w", err)
	}
	if unique != 1 {
		return errors.New("activity source index is not unique")
	}
	if err := verifyIndexColumns(ctx, db, "idx_activity_source_portfolio", []string{"source_audit_event_id", "portfolio_id"}, []int{0, 0}); err != nil {
		return err
	}
	if err := verifyIndexColumns(ctx, db, "idx_activity_portfolio_order", []string{"portfolio_id", "sort_time", "id"}, []int{0, 1, 1}); err != nil {
		return err
	}
	var tableSQL string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='activity_events'").Scan(&tableSQL); err != nil {
		return fmt.Errorf("activity table definition: %w", err)
	}
	lowerSQL := strings.ToLower(tableSQL)
	for _, fragment := range []string{"length(occurred_at) = 30", "glob", "strftime", "julianday", "1970-01-01t00:00:00.000000000z", "sort_time = occurred_at"} {
		if !strings.Contains(lowerSQL, fragment) {
			return fmt.Errorf("activity timestamp check is missing %q", fragment)
		}
	}
	for _, trigger := range []struct{ name, operation string }{{"activity_events_append_only_update", "before update on activity_events"}, {"activity_events_append_only_delete", "before delete on activity_events"}} {
		var triggerSQL string
		if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", trigger.name).Scan(&triggerSQL); err != nil {
			return fmt.Errorf("activity trigger %s: %w", trigger.name, err)
		}
		triggerSQL = strings.ToLower(triggerSQL)
		if !strings.Contains(triggerSQL, "append-only") || !strings.Contains(triggerSQL, trigger.operation) || !strings.Contains(triggerSQL, "raise(abort") {
			return fmt.Errorf("activity trigger %s is not append-only", trigger.name)
		}
	}
	return nil
}

func verifyIndexColumns(ctx context.Context, db *sql.DB, name string, expected []string, expectedDesc []int) error {
	rows, err := db.QueryContext(ctx, "PRAGMA index_xinfo("+quoteSQLiteIdentifier(name)+")")
	if err != nil {
		return fmt.Errorf("index %s: %w", name, err)
	}
	defer rows.Close()
	for index := range expected {
		if !rows.Next() {
			return fmt.Errorf("index %s is missing column %s", name, expected[index])
		}
		var seqno, cid, desc, key int
		var column, collation string
		if err := rows.Scan(&seqno, &cid, &column, &desc, &collation, &key); err != nil {
			return err
		}
		if seqno != index || column != expected[index] || desc != expectedDesc[index] || key != 1 {
			return fmt.Errorf("index %s column %d is %s desc=%d, expected %s desc=%d", name, index, column, desc, expected[index], expectedDesc[index])
		}
	}
	return nil
}

func quoteSQLiteIdentifier(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

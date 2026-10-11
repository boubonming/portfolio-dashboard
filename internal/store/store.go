package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
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
	return nil
}

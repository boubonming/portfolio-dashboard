package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationsAreIdempotentAndEnforceDecimalText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portfolio.sqlite")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	if err := first.DB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 4 {
		t.Fatalf("migration count = %d", migrationCount)
	}
	var foreignKeys int
	if err := first.DB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d", foreignKeys)
	}
	var columnType string
	if err := first.DB.QueryRow("SELECT type FROM pragma_table_info('lots') WHERE name='quantity'").Scan(&columnType); err != nil {
		t.Fatal(err)
	}
	if columnType != "TEXT" {
		t.Fatalf("quantity type = %s", columnType)
	}
	first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.DB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 4 {
		t.Fatalf("repeat migration count = %d", migrationCount)
	}
}

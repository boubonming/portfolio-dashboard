package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
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
	if migrationCount != 5 {
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
	if migrationCount != 5 {
		t.Fatalf("repeat migration count = %d", migrationCount)
	}
}

func TestAddProviderMappingRollsBackWhenAuditInsertFails(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "mapping.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-02T12:00:00Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('mapping-portfolio','Mapping fixture','USD',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES('mapping-instrument','MAP','USD',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER fail_mapping_audit BEFORE INSERT ON audit_events WHEN NEW.action = 'market.mapping.added' BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = s.AddProviderMapping(ctx, ProviderMappingInput{InstrumentID: "mapping-instrument", Provider: marketdata.ProviderFinnhub, ProviderSymbol: "NASDAQ:MAP", QuoteCurrency: "USD", ActiveFrom: now, Provenance: "fixture"})
	if err == nil {
		t.Fatal("expected audit insertion failure")
	}
	var mappings int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM instrument_provider_mappings WHERE instrument_id='mapping-instrument'`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatalf("mapping was committed despite audit failure: %d", mappings)
	}
}

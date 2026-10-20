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

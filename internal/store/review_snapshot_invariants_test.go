package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestCanonicalSnapshotHoldingOrderRejectsReorderingAndDuplicates(t *testing.T) {
	input := []domain.ValuationLot{
		{ID: "lot-001", InstrumentID: "instrument-shared", Symbol: "SAME", Currency: "MYR", Quantity: "1"},
		{ID: "lot-002", InstrumentID: "instrument-shared", Symbol: "SAME", Currency: "MYR", Quantity: "2"},
	}
	valuation := []domain.LotValuation{
		{LotID: "lot-001", InstrumentID: "instrument-shared", Symbol: "SAME", Currency: "MYR", Quantity: "1"},
		{LotID: "lot-002", InstrumentID: "instrument-shared", Symbol: "SAME", Currency: "MYR", Quantity: "2"},
	}
	if !canonicalSnapshotLots(input, valuation) {
		t.Fatal("canonical ascending lots should pass")
	}
	cases := []struct {
		name      string
		input     []domain.ValuationLot
		valuation []domain.LotValuation
	}{
		{name: "input swap", input: []domain.ValuationLot{input[1], input[0]}, valuation: []domain.LotValuation{valuation[1], valuation[0]}},
		{name: "duplicate", input: []domain.ValuationLot{input[0], input[0]}, valuation: []domain.LotValuation{valuation[0], valuation[0]}},
		{name: "valuation swap", input: input, valuation: []domain.LotValuation{valuation[1], valuation[0]}},
		{name: "valuation divergence", input: input, valuation: []domain.LotValuation{{LotID: "lot-001"}, {LotID: "lot-003"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if canonicalSnapshotLots(tc.input, tc.valuation) {
				t.Fatalf("accepted noncanonical lots: %+v / %+v", tc.input, tc.valuation)
			}
		})
	}
}

func TestIncompleteSnapshotInvariantMatchesCalculationReasons(t *testing.T) {
	asOf := time.Now().UTC()
	missingInput := domain.ValuationInput{Lots: []domain.ValuationLot{{ID: "lot-missing", InstrumentID: "instrument-missing", Symbol: "MISS", Currency: "MYR", Quantity: "1"}}, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour}
	missing, err := domain.Calculate(missingInput)
	if err != nil || missing.Complete || !hasIncompleteHolding(missing.Lots) || !incompleteDependenciesMatch(missingInput, missing.Lots, missing.MissingDependencies, missing.StaleDependencies, missing.InvalidDependencies, "MYR") {
		t.Fatalf("missing quote matrix failed: %+v, %v", missing, err)
	}

	staleInput := domain.ValuationInput{Lots: []domain.ValuationLot{{ID: "lot-stale", InstrumentID: "instrument-stale", Symbol: "STALE", Currency: "MYR", Quantity: "1"}}, Quotes: []domain.Quote{{ID: "quote-stale", InstrumentID: "instrument-stale", Price: "2", Currency: "MYR", MarketAt: asOf.Add(-2 * time.Hour).Format(time.RFC3339Nano)}}, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour}
	stale, err := domain.Calculate(staleInput)
	if err != nil || stale.Complete || !hasIncompleteHolding(stale.Lots) || !incompleteDependenciesMatch(staleInput, stale.Lots, stale.MissingDependencies, stale.StaleDependencies, stale.InvalidDependencies, "MYR") {
		t.Fatalf("stale quote matrix failed: %+v, %v", stale, err)
	}

	invalidInput := domain.ValuationInput{Lots: []domain.ValuationLot{{ID: "lot-invalid", InstrumentID: "instrument-invalid", Symbol: "INVALID", Currency: "MYR", Quantity: "-1"}}, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour}
	invalid, err := domain.Calculate(invalidInput)
	if err != nil || invalid.Complete || !hasIncompleteHolding(invalid.Lots) || !incompleteDependenciesMatch(invalidInput, invalid.Lots, invalid.MissingDependencies, invalid.StaleDependencies, invalid.InvalidDependencies, "MYR") {
		t.Fatalf("invalid quantity matrix failed: %+v, %v", invalid, err)
	}

	allFreshInput := domain.ValuationInput{Lots: []domain.ValuationLot{{ID: "lot-fresh", InstrumentID: "instrument-fresh", Symbol: "FRESH", Currency: "MYR", Quantity: "1"}}, Quotes: []domain.Quote{{ID: "quote-fresh", InstrumentID: "instrument-fresh", Price: "2", Currency: "MYR", MarketAt: asOf.Format(time.RFC3339Nano)}}, ReportingCurrency: "MYR", AsOf: asOf, MaxAge: time.Hour}
	allFresh, err := domain.Calculate(allFreshInput)
	if err != nil || !allFresh.Complete {
		t.Fatalf("fresh setup failed: %+v, %v", allFresh, err)
	}
	allFresh.Complete = false
	allFresh.ReportingTotal = ""
	allFresh.MissingDependencies = nil
	allFresh.StaleDependencies = nil
	allFresh.InvalidDependencies = nil
	if hasIncompleteHolding(allFresh.Lots) || len(allFresh.MissingDependencies)+len(allFresh.StaleDependencies)+len(allFresh.InvalidDependencies) != 0 {
		t.Fatal("fresh setup unexpectedly has an incomplete reason")
	}
	if hasIncompleteHolding(allFresh.Lots) || incompleteDependenciesMatch(allFreshInput, allFresh.Lots, nil, nil, nil, "MYR") {
		t.Fatal("accepted all-fresh, fully valued, dependency-free incomplete state")
	}
	allFresh.StaleDependencies = []string{"quote:unrelated"}
	if incompleteDependenciesMatch(allFreshInput, allFresh.Lots, nil, allFresh.StaleDependencies, nil, "MYR") {
		t.Fatal("accepted dependency/freshness contradiction")
	}
}

func TestSnapshotHoldingLimitBoundaryAndRollback(t *testing.T) {
	ctx := context.Background()
	database, err := Open(ctx, filepath.Join(t.TempDir(), "holding-limit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := canonicalSnapshotTime(time.Now().UTC())
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-limit", "Limit", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-limit", "LIMIT", "MYR", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-limit", "instrument-limit", now); err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxSnapshotHoldings; index++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("lot-%05d", index), "portfolio-limit", "instrument-limit", "1", "MYR", now, now); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := database.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-limit", ReportingCurrency: "MYR", CalculationVersion: "limit.v1", AsOf: time.Now().UTC(), MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("exactly %d holdings rejected: %v", maxSnapshotHoldings, err)
	}
	if status.SnapshotID == "" {
		t.Fatal("boundary snapshot has no ID")
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?)`, "lot-10000", "portfolio-limit", "instrument-limit", "1", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-limit", ReportingCurrency: "MYR", CalculationVersion: "limit.v2", AsOf: time.Now().UTC(), MaxAge: time.Hour})
	if !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("expected bounded corruption for %d holdings, got %v", maxSnapshotHoldings+1, err)
	}
	var snapshotCount, holdingCount int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE portfolio_id=?`, "portfolio-limit").Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM snapshot_holdings WHERE portfolio_id=?`, "portfolio-limit").Scan(&holdingCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 1 || holdingCount != maxSnapshotHoldings {
		t.Fatalf("oversized create left partial rows: snapshots=%d holdings=%d", snapshotCount, holdingCount)
	}
}

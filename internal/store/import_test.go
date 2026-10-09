package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func approvedPreview(hash string) domain.Preview {
	lots := make([]domain.Lot, 0, 35)
	for i := 0; i < 31; i++ {
		lots = append(lots, domain.Lot{
			Symbol: "SYM" + string(rune('A'+i)), Currency: "USD", Quantity: "1", UnitCost: ptr("10"),
			CostBasis: ptr("10"), SourceRow: i + 1, Classification: "lot", Included: true,
		})
	}
	lots = append(lots,
		domain.Lot{Symbol: "SYM" + string(rune('A')), Currency: "USD", Quantity: "2", UnitCost: ptr("11"), CostBasis: ptr("22"), SourceRow: 32, Classification: "lot", Included: true},
		domain.Lot{Symbol: "SYMB", Currency: "USD", Quantity: "3", UnitCost: ptr("12"), CostBasis: ptr("36"), SourceRow: 33, Classification: "lot", Included: true},
		domain.Lot{Symbol: "SYM" + string(rune('A')), Currency: "USD", SourceRow: 34, Classification: "aggregate", Included: false},
		domain.Lot{Symbol: "SYMB", Currency: "USD", SourceRow: 35, Classification: "aggregate", Included: false},
	)
	return domain.Preview{
		Source: "fixture.md", SourceSHA256: hash, Lots: lots,
		Summary: domain.PreviewSummary{SourceTableRows: 35, HoldingRows: 33, LotsIncluded: 33, AggregateRows: 2, DistinctSymbols: 31},
	}
}

func ptr(value string) *string { return &value }

func TestApplyPersistsApprovedImportAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("a", 64))
	result, err := s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.LotsImported != 33 || result.ItemsImported != 35 {
		t.Fatalf("result = %+v", result)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM lots WHERE source_sha256 = ?", preview.SourceSHA256).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 33 {
		t.Fatalf("lots = %d", count)
	}
	var aggregateItems int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM import_items WHERE classification = 'aggregate'").Scan(&aggregateItems); err != nil {
		t.Fatal(err)
	}
	if aggregateItems != 2 {
		t.Fatalf("aggregate import items = %d", aggregateItems)
	}
	var reportingCurrency string
	if err := s.DB.QueryRow("SELECT reporting_currency FROM portfolios").Scan(&reportingCurrency); err != nil {
		t.Fatal(err)
	}
	if reportingCurrency != "MYR" {
		t.Fatalf("reporting currency = %s", reportingCurrency)
	}
	var duplicateLots int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM lots WHERE instrument_id = (SELECT id FROM instruments WHERE symbol = 'SYMA')").Scan(&duplicateLots); err != nil {
		t.Fatal(err)
	}
	if duplicateLots != 2 {
		t.Fatalf("duplicate-symbol lots = %d", duplicateLots)
	}
	second, err := s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyApplied || second.Status != "already_applied" {
		t.Fatalf("second result = %+v", second)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit events = %d", count)
	}
}

func TestApplyRejectsHashMismatchBeforeWrites(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("b", 64))
	if _, err := s.Apply(ctx, preview, "fixture.md", strings.Repeat("c", 64), "MYR"); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("error = %v", err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM portfolios").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("portfolios after mismatch = %d", count)
	}
}

func TestApplyRollsBackAfterTransactionalConstraintFailure(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("d", 64))
	preview.Lots[1].SourceRow = preview.Lots[0].SourceRow
	if _, err := s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR"); err == nil || !strings.Contains(err.Error(), "persist import item") {
		t.Fatalf("error = %v", err)
	}
	for _, table := range []string{"portfolios", "import_runs", "import_items", "instruments", "lots", "audit_events"} {
		var count int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s after rollback = %d", table, count)
		}
	}
}

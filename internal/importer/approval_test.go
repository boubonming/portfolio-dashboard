package importer

import (
	"strings"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestValidateApprovedRejectsChangedReconciliation(t *testing.T) {
	preview := domain.Preview{Summary: domain.PreviewSummary{SourceTableRows: ApprovedSourceTableRows - 1, LotsIncluded: ApprovedLots, AggregateRows: ApprovedAggregateRows, DistinctSymbols: ApprovedDistinctSymbols}}
	if err := ValidateApproved(preview); err == nil || !strings.Contains(err.Error(), "reconciliation counts differ") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateApprovedRejectsMalformedDecimal(t *testing.T) {
	cost := "12.50"
	preview := domain.Preview{
		Summary: domain.PreviewSummary{SourceTableRows: ApprovedSourceTableRows, LotsIncluded: ApprovedLots, AggregateRows: ApprovedAggregateRows, DistinctSymbols: ApprovedDistinctSymbols},
		Lots:    []domain.Lot{{Symbol: "ABC", Currency: "USD", Quantity: "not-a-decimal", UnitCost: &cost, SourceRow: 1, Included: true}},
	}
	if err := ValidateApproved(preview); err == nil || !strings.Contains(err.Error(), "invalid quantity") {
		t.Fatalf("error = %v", err)
	}
}

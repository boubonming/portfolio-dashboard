package store

import (
	"encoding/json"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestStatusFromPayloadNormalizesHistoricalNilArraysWithoutRewritingPayload(t *testing.T) {
	encoded, err := json.Marshal(snapshotPayload{
		SnapshotID:         "snapshot-historical",
		PortfolioID:        "portfolio-default",
		CalculationVersion: domain.CalculationVersion,
		CreatedAt:          "2026-01-01T00:00:00.000000000Z",
		AsOf:               "2026-01-01T00:00:00.000000000Z",
		Valuation:          domain.Valuation{ReportingCurrency: "MYR", Subtotals: nil, MissingDependencies: nil, StaleDependencies: nil, FutureDependencies: nil, InvalidDependencies: nil, QuoteIDs: nil, FXIDs: nil, SourceTimestamps: nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := statusFromPayload(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string][]string{
		"currencies":           status.Currencies,
		"missing_dependencies": status.MissingDependencies,
		"stale_dependencies":   status.StaleDependencies,
		"future_dependencies":  status.FutureDependencies,
		"invalid_dependencies": status.InvalidDependencies,
		"quote_ids":            status.QuoteIDs,
		"fx_ids":               status.FXIDs,
		"source_timestamps":    status.SourceTimestamps,
	} {
		if values == nil {
			t.Errorf("%s is nil", name)
		}
	}
	for name, values := range map[string]any{
		"subtotals":          status.Subtotals,
		"quote_observations": status.QuoteObservations,
		"fx_observations":    status.FXObservations,
	} {
		if values == nil {
			t.Errorf("%s is nil", name)
		}
	}
}

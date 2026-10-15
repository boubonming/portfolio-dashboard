package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestReviewSnapshotRejectsDatabaseSemanticCorruption(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "semantic-corruption.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err = s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-semantic", "Semantic", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-semantic", "SEM", "Semantic", "MYR", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,cost_basis,created_at) VALUES(?,?,?,?,?,?,?)`, "lot-semantic", "portfolio-semantic", "instrument-semantic", "1", "MYR", "1", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddQuote(ctx, QuoteInput{InstrumentID: "instrument-semantic", Currency: "MYR", Price: "2", Source: "fixture", MarketAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-semantic", ReportingCurrency: "MYR", CalculationVersion: "semantic.v1", AsOf: now, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.SnapshotShow(ctx, status.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	canonical := raw.(snapshotPayload)

	corruptions := map[string]func(*snapshotPayload){
		"arithmetic-and-cost-pnl": func(payload *snapshotPayload) {
			payload.Valuation.Lots[0].Price = "999"
			payload.Valuation.Lots[0].MarketValue = "999"
			payload.Valuation.Lots[0].ReportingValue = stringPtr("999")
			payload.Valuation.Lots[0].CostBasis = stringPtr("999")
			payload.Valuation.Lots[0].UnrealisedPnL = stringPtr("0")
			payload.Valuation.ReportingTotal = "999"
			payload.Valuation.Subtotals[0].MarketValue = "999"
			payload.Valuation.Subtotals[0].CostBasis = "999"
			payload.Valuation.Subtotals[0].UnrealisedPnL = "0"
		},
		"freshness-age": func(payload *snapshotPayload) {
			payload.Valuation.Lots[0].Freshness.Age = "2h"
		},
		"quote-identity": func(payload *snapshotPayload) {
			payload.Valuation.Lots[0].QuoteID = "unrelated-quote"
		},
		"fx-identity": func(payload *snapshotPayload) {
			payload.Valuation.Lots[0].FXIDs = []string{"unrelated-fx"}
		},
		"complete-state": func(payload *snapshotPayload) {
			payload.Valuation.Complete = false
			payload.Valuation.ReportingTotal = ""
			payload.Valuation.MissingDependencies = []string{"quote:unrelated"}
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			canonicalJSON, marshalErr := json.Marshal(canonical)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var payload snapshotPayload
			if unmarshalErr := json.Unmarshal(canonicalJSON, &payload); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			payload.SnapshotID = "corrupt-" + name
			corrupt(&payload)
			encoded, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err = s.DB.Exec(`INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at,input_hash) VALUES(?,?,?,?,?,?)`, payload.SnapshotID, payload.PortfolioID, payload.CalculationVersion, encoded, payload.CreatedAt, "corrupt-"+name); err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(`INSERT INTO review_snapshots(portfolio_id,snapshot_id,calculation_version) VALUES(?,?,?)`, payload.PortfolioID, payload.SnapshotID, payload.CalculationVersion); err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(`INSERT INTO snapshot_holdings(portfolio_id,snapshot_id,holding_id,instrument_id) VALUES(?,?,?,?)`, payload.PortfolioID, payload.SnapshotID, "lot-semantic", "instrument-semantic"); err != nil {
				t.Fatal(err)
			}
			_, err = s.ReviewSnapshot(ctx, payload.PortfolioID, payload.SnapshotID, false)
			if !errors.Is(err, ErrSnapshotCorrupt) {
				t.Fatalf("corrupt %s was accepted: %v", name, err)
			}
		})
	}
}

func TestFXDependencyIdentityMatrix(t *testing.T) {
	input := domain.ValuationInput{Lots: []domain.ValuationLot{{ID: "lot", Currency: "MYR"}}, ReportingCurrency: "USD"}
	valuation := []domain.LotValuation{{LotID: "lot", Currency: "MYR"}}
	cases := []struct {
		name string
		fx   []domain.FXRate
		dep  string
		want bool
	}{
		{name: "pair direct", dep: "missing fx:MYR/USD", want: true},
		{name: "pair inverse text", dep: "missing fx:USD/MYR", want: false},
		{name: "same currency fabricated", dep: "missing fx:MYR/MYR", want: false},
		{name: "id direct", fx: []domain.FXRate{{ID: "fx-direct", BaseCurrency: "MYR", QuoteCurrency: "USD"}}, dep: "stale fx:fx-direct", want: true},
		{name: "id inverse", fx: []domain.FXRate{{ID: "fx-inverse", BaseCurrency: "USD", QuoteCurrency: "MYR"}}, dep: "stale fx:fx-inverse", want: true},
		{name: "id wrong pair", fx: []domain.FXRate{{ID: "fx-wrong", BaseCurrency: "EUR", QuoteCurrency: "USD"}}, dep: "stale fx:fx-wrong", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseInput := input
			if tc.name == "same currency fabricated" {
				caseInput.ReportingCurrency = "MYR"
			}
			caseInput.FXRates = tc.fx
			if got := incompleteDependenciesMatch(caseInput, valuation, []string{tc.dep}, nil, nil, caseInput.ReportingCurrency); got != tc.want {
				t.Fatalf("dependency match=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidCurrencyUsesExactOpenAPIGrammar(t *testing.T) {
	for _, value := range []string{"AAA", "ZZZ"} {
		if !validCurrency(value) {
			t.Errorf("valid currency %q rejected", value)
		}
	}
	for _, value := range []string{"A[A", "AA1", "aaA", "ＡＡＡ", "AA", "AAAA", ""} {
		if validCurrency(value) {
			t.Errorf("invalid currency %q accepted", value)
		}
	}
}

func stringPtr(value string) *string { return &value }

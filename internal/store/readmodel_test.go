package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestPortfolioOverviewFailsSafeForLegacySnapshotWithoutMaxAge(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-freshness", "Freshness fixture", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-freshness", "FRESH", "Freshness instrument", "USD", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at) VALUES(?,?,?,?,?,?,?)`, "lot-freshness", "portfolio-freshness", "instrument-freshness", "2", "5", "USD", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "quote-freshness", InstrumentID: "instrument-freshness", Currency: "USD", Price: "10", Source: "fixture", MarketAt: now.Add(-3 * time.Hour).Format(time.RFC3339Nano), Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFXRate(ctx, FXInput{ID: "fx-freshness", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.5", Source: "fixture", MarketAt: now.Add(-time.Hour).Format(time.RFC3339Nano), Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-freshness", ReportingCurrency: "MYR", CalculationVersion: "freshness.test.v1", AsOf: now, MaxAge: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if status.Complete {
		t.Fatalf("stale source unexpectedly complete: %+v", status)
	}
	var storedPayload []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT payload FROM snapshots WHERE id = ?`, status.SnapshotID).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(storedPayload, &encoded); err != nil {
		t.Fatal(err)
	}
	delete(encoded, "max_age")
	legacySnapshotID := "snapshot-legacy-freshness"
	encoded["snapshot_id"] = legacySnapshotID
	encoded["calculation_version"] = "freshness.legacy.v1"
	legacyPayload, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at,input_hash) VALUES(?,?,?,?,?,?)`, legacySnapshotID, "portfolio-freshness", "freshness.legacy.v1", legacyPayload, now.Add(time.Minute).Format(time.RFC3339Nano), "legacy-freshness-input"); err != nil {
		t.Fatal(err)
	}
	myr, err := s.PortfolioOverview(ctx, "portfolio-freshness", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	usd, err := s.PortfolioOverview(ctx, "portfolio-freshness", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if myr.Snapshot.SnapshotID != usd.Snapshot.SnapshotID || myr.Snapshot.Complete || usd.Snapshot.Complete || usd.Snapshot.State != "incomplete" {
		t.Fatalf("alternate read changed snapshot semantics: MYR=%+v USD=%+v", myr.Snapshot, usd.Snapshot)
	}
	if myr.Snapshot.ReportingTotal != status.ReportingTotal || len(myr.Holdings) != 1 || myr.Holdings[0].Freshness.Status != domain.Stale {
		t.Fatalf("same-currency read did not preserve stored valuation: status=%+v holdings=%+v", myr.Snapshot, myr.Holdings)
	}
	if usd.Snapshot.ReportingTotal != "" || !containsString(usd.Snapshot.MissingDependencies, "snapshot:max_age") || len(usd.Snapshot.MissingDependencies) != 1 || len(usd.Holdings) != 1 || usd.Holdings[0].ReportingValue != nil {
		t.Fatalf("alternate holding quality = %+v", usd.Holdings)
	}
	if usd.Allocation.Coverage.MissingDependencies != len(usd.Snapshot.MissingDependencies) {
		t.Fatalf("legacy allocation dependency count = %+v", usd.Allocation.Coverage)
	}
	if myr.Allocation.State == "complete" || usd.Allocation.State == "complete" || len(myr.Allocation.ByInstrument) != 0 || len(usd.Allocation.BySourceCurrency) != 0 {
		t.Fatalf("legacy allocation should be withheld: MYR=%+v USD=%+v", myr.Allocation, usd.Allocation)
	}
}

func TestPortfolioOverviewDoesNotLeakCrossPortfolioAccount(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, portfolio := range [2]string{"portfolio-owner", "portfolio-broker"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "MYR", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO accounts(id,portfolio_id,broker,created_at) VALUES(?,?,?,?)`, "account-broker", "portfolio-broker", "Foreign Broker", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-owner", "OWNER", "MYR", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?,?)`, "lot-owner", "portfolio-owner", "instrument-owner", "account-broker", "1", "MYR", now); err != nil {
		t.Fatal(err)
	}
	view, err := s.PortfolioOverview(ctx, "portfolio-owner", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Holdings) != 1 || view.Holdings[0].Broker != nil || !containsString(view.Holdings[0].DataQuality, "missing_broker") {
		t.Fatalf("foreign account leaked: %+v", view.Holdings)
	}
}

func TestPortfolioOverviewExposesDerivedCostBasisAndSourceUnitCost(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-cost", "Cost fixture", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-cost", "COST", "Cost instrument", "MYR", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at) VALUES(?,?,?,?,?,?,?)`, "lot-cost", "portfolio-cost", "instrument-cost", "2", "5", "MYR", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "quote-cost", InstrumentID: "instrument-cost", Currency: "MYR", Price: "10", Source: "fixture", MarketAt: now.Add(-time.Hour).Format(time.RFC3339Nano), Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-cost", ReportingCurrency: "MYR", AsOf: now, MaxAge: 2 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	view, err := s.PortfolioOverview(ctx, "portfolio-cost", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Holdings) != 1 || view.Holdings[0].UnitCost == nil || *view.Holdings[0].UnitCost != "5" || view.Holdings[0].CostBasis == nil || *view.Holdings[0].CostBasis != "10" {
		t.Fatalf("cost presentation = %+v", view.Holdings)
	}
}

func TestDependencyFlagsUseExactLotAndDependencyIdentity(t *testing.T) {
	input := domain.ValuationInput{
		Lots: []domain.ValuationLot{
			{ID: "lot-usd", InstrumentID: "instrument-usd", Currency: "USD"},
			{ID: "lot-myr", InstrumentID: "instrument-myr", Currency: "MYR"},
		},
		Quotes:  []domain.Quote{{ID: "quote-with-different-id", InstrumentID: "instrument-usd"}},
		FXRates: []domain.FXRate{{ID: "fx-usd-myr", BaseCurrency: "USD", QuoteCurrency: "MYR"}},
	}
	valuation := domain.Valuation{
		MissingDependencies: []string{"quote:instrument-usd", "missing fx:USD/MYR"},
		StaleDependencies:   []string{"quote:quote-with-different-id", "stale fx:fx-usd-myr"},
		InvalidDependencies: []string{"lot:lot-usd:quantity", "cost:lot-usd"},
	}
	var usd, myr domain.HoldingView
	appendDependencyFlags(&usd, valuation, input.Lots[0], input, "MYR")
	appendDependencyFlags(&myr, valuation, input.Lots[1], input, "MYR")
	if !containsString(usd.DataQuality, "stale_dependency") || !containsString(usd.DataQuality, "invalid_dependency") || !containsString(usd.DataQuality, "missing_dependency") {
		t.Fatalf("USD dependencies = %v", usd.DataQuality)
	}
	if len(myr.DataQuality) != 0 {
		t.Fatalf("unrelated MYR dependencies = %v", myr.DataQuality)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

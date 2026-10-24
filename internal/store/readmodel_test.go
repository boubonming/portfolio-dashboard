package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestPortfolioOverviewDoesNotLeakCrossPortfolioAccount(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := canonicalSnapshotTime(time.Now().UTC())
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
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-owner", "instrument-owner", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "lot-foreign-account", "portfolio-owner", "instrument-owner", "account-broker", "1", "MYR", now, now); err == nil {
		t.Fatal("foreign account fixture was accepted")
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "lot-owner", "portfolio-owner", "instrument-owner", nil, "1", "MYR", now, now); err != nil {
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
	nowText := canonicalSnapshotTime(now)
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-cost", "Cost fixture", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-cost", "COST", "Cost instrument", "MYR", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-cost", "instrument-cost", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "lot-cost", "portfolio-cost", "instrument-cost", "2", "5", "MYR", nowText, nowText); err != nil {
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
	if len(view.Holdings) != 1 || view.Holdings[0].UnitCost == nil || *view.Holdings[0].UnitCost != "5" || view.Holdings[0].CostBasis == nil || *view.Holdings[0].CostBasis != "10" || view.Holdings[0].UnrealisedPnL == nil || *view.Holdings[0].UnrealisedPnL != "10" {
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

func TestPortfolioOverviewIncompleteSnapshotKeepsUnaffectedLotPnL(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	nowText := canonicalSnapshotTime(now)
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-partial", "Partial fixture", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	for _, instrument := range []struct{ id, symbol string }{{"instrument-good", "GOOD"}, {"instrument-missing", "MISSING"}} {
		if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, instrument.id, instrument.symbol, "MYR", nowText); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-partial", instrument.id, nowText); err != nil {
			t.Fatal(err)
		}
	}
	for _, lot := range []struct{ id, instrument, quantity, unitCost string }{
		{"lot-good", "instrument-good", "2", "5"},
		{"lot-missing", "instrument-missing", "3", "2"},
	} {
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, lot.id, "portfolio-partial", lot.instrument, lot.quantity, lot.unitCost, "MYR", nowText, nowText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "quote-good", InstrumentID: "instrument-good", Currency: "MYR", Price: "10", Source: "fixture", MarketAt: nowText, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-partial", ReportingCurrency: "MYR", AsOf: now, MaxAge: time.Hour})
	if err != nil || status.Complete {
		t.Fatalf("partial snapshot=%+v err=%v", status, err)
	}
	view, err := s.PortfolioOverview(ctx, "portfolio-partial", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if view.Snapshot.State != "incomplete" || len(view.Holdings) != 2 {
		t.Fatalf("partial overview=%+v", view)
	}
	byLot := make(map[string]domain.HoldingView, len(view.Holdings))
	for _, holding := range view.Holdings {
		byLot[holding.LotID] = holding
	}
	good := byLot["lot-good"]
	if good.LatestPrice == nil || *good.LatestPrice != "10" || good.LatestValue == nil || *good.LatestValue != "20" || good.UnrealisedPnL == nil || *good.UnrealisedPnL != "10" {
		t.Fatalf("unaffected lot lost exact source valuation: %+v", good)
	}
	if byLot["lot-missing"].UnrealisedPnL != nil {
		t.Fatalf("affected lot exposed P/L: %+v", byLot["lot-missing"])
	}
}

func TestPortfolioOverviewReportingFXOnlyIncompleteStillExposesSourcePnL(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	nowText := canonicalSnapshotTime(now)
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-fx-missing", "FX fixture", "MYR", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-fx-missing", "USDLOT", "USD", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-fx-missing", "instrument-fx-missing", nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, "lot-fx-missing", "portfolio-fx-missing", "instrument-fx-missing", "2", "5", "USD", nowText, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "quote-fx-missing", InstrumentID: "instrument-fx-missing", Currency: "USD", Price: "10", Source: "fixture", MarketAt: nowText, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-fx-missing", ReportingCurrency: "MYR", AsOf: now, MaxAge: time.Hour})
	if err != nil || status.Complete {
		t.Fatalf("missing FX snapshot=%+v err=%v", status, err)
	}
	view, err := s.PortfolioOverview(ctx, "portfolio-fx-missing", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Holdings) != 1 || view.Holdings[0].UnrealisedPnL == nil || *view.Holdings[0].UnrealisedPnL != "10" || view.Holdings[0].ReportingValue != nil {
		t.Fatalf("source P/L was not preserved when only reporting FX was incomplete: %+v", view.Holdings)
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

func TestPortfolioOverviewPnLUsesLotIdentityAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	nowText := canonicalSnapshotTime(now)
	for _, portfolio := range []string{"portfolio-pnl", "portfolio-other", "portfolio-no-snapshot"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolio, portfolio, "MYR", nowText, nowText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-pnl", "SAME", "MYR", nowText); err != nil {
		t.Fatal(err)
	}
	for _, portfolio := range []string{"portfolio-pnl", "portfolio-other", "portfolio-no-snapshot"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, portfolio, "instrument-pnl", nowText); err != nil {
			t.Fatal(err)
		}
	}
	for _, lot := range []struct {
		id, portfolio, quantity, unitCost string
	}{
		{id: "lot-pnl-a", portfolio: "portfolio-pnl", quantity: "2", unitCost: "5"},
		{id: "lot-pnl-b", portfolio: "portfolio-pnl", quantity: "3", unitCost: "2"},
		{id: "lot-other", portfolio: "portfolio-other", quantity: "99", unitCost: "1"},
		{id: "lot-no-snapshot", portfolio: "portfolio-no-snapshot", quantity: "1", unitCost: "1"},
	} {
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?)`, lot.id, lot.portfolio, "instrument-pnl", lot.quantity, lot.unitCost, "MYR", nowText, nowText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "quote-pnl", InstrumentID: "instrument-pnl", Price: "10", Currency: "MYR", Source: "fixture", MarketAt: nowText, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-pnl", ReportingCurrency: "MYR", AsOf: now, MaxAge: time.Hour})
	if err != nil || !status.Complete {
		t.Fatalf("target snapshot=%+v err=%v", status, err)
	}
	otherStatus, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-other", ReportingCurrency: "MYR", AsOf: now, MaxAge: time.Hour})
	if err != nil || !otherStatus.Complete {
		t.Fatalf("other snapshot=%+v err=%v", otherStatus, err)
	}

	view, err := s.PortfolioOverview(ctx, "portfolio-pnl", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Holdings) != 2 {
		t.Fatalf("target holdings=%d, want 2", len(view.Holdings))
	}
	gotPnL := map[string]string{}
	for _, holding := range view.Holdings {
		if holding.UnrealisedPnL == nil {
			t.Fatalf("complete lot %s omitted P/L: %+v", holding.LotID, holding)
		}
		gotPnL[holding.LotID] = *holding.UnrealisedPnL
	}
	if gotPnL["lot-pnl-a"] != "10" || gotPnL["lot-pnl-b"] != "24" {
		t.Fatalf("per-lot P/L=%v, want lot-pnl-a=10 and lot-pnl-b=24", gotPnL)
	}
	otherView, err := s.PortfolioOverview(ctx, "portfolio-other", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(otherView.Holdings) != 1 || otherView.Holdings[0].LotID != "lot-other" || otherView.Holdings[0].UnrealisedPnL == nil || *otherView.Holdings[0].UnrealisedPnL != "891" {
		t.Fatalf("other portfolio view leaked or misidentified lots: %+v", otherView.Holdings)
	}
	noSnapshot, err := s.PortfolioOverview(ctx, "portfolio-no-snapshot", "MYR")
	if err != nil {
		t.Fatal(err)
	}
	if len(noSnapshot.Holdings) != 1 || noSnapshot.Holdings[0].UnrealisedPnL != nil {
		t.Fatalf("no-snapshot P/L=%+v", noSnapshot.Holdings)
	}

	if _, err := s.DB.Exec(`DROP TRIGGER snapshots_immutable_update`); err != nil {
		t.Fatal(err)
	}
	var original snapshotPayload
	var encoded []byte
	if err := s.DB.QueryRow(`SELECT payload FROM snapshots WHERE id=?`, status.SnapshotID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &original); err != nil {
		t.Fatal(err)
	}
	setLot := func(payload *snapshotPayload, lotID string, update func(*domain.LotValuation)) {
		for i := range payload.Valuation.Lots {
			if payload.Valuation.Lots[i].LotID == lotID {
				update(&payload.Valuation.Lots[i])
				return
			}
		}
		t.Fatalf("snapshot lot %s not found", lotID)
	}
	writePayload := func(payload snapshotPayload) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec(`UPDATE snapshots SET payload=? WHERE id=?`, encoded, status.SnapshotID); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		mutate func(*snapshotPayload)
	}{
		{name: "incomplete valuation", mutate: func(payload *snapshotPayload) { payload.Valuation.Complete = false }},
		{name: "stale holdings revision", mutate: func(payload *snapshotPayload) { payload.Input.HoldingsRevision = 999 }},
		{name: "missing cost", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.CostBasis = nil })
		}},
		{name: "missing pnl", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.UnrealisedPnL = nil })
		}},
		{name: "missing price", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.Price = "" })
		}},
		{name: "stale lot valuation", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.Freshness.Status = domain.Stale })
		}},
		{name: "missing lot valuation", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.Freshness.Status = domain.Missing })
		}},
		{name: "future lot valuation", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.Freshness.Status = domain.Future })
		}},
		{name: "invalid lot valuation", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.Freshness.Status = domain.Invalid })
		}},
		{name: "inconsistent pnl", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.UnrealisedPnL = stringPointer("999") })
		}},
		{name: "malformed pnl", mutate: func(payload *snapshotPayload) {
			setLot(payload, "lot-pnl-a", func(lot *domain.LotValuation) { lot.UnrealisedPnL = stringPointer("not-a-decimal") })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var payload snapshotPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&payload)
			}
			writePayload(payload)
			view, err := s.PortfolioOverview(ctx, "portfolio-pnl", "MYR")
			if err != nil {
				t.Fatal(err)
			}
			for _, holding := range view.Holdings {
				if tc.name == "incomplete valuation" {
					if holding.UnrealisedPnL == nil {
						t.Fatalf("%s omitted exact P/L for %s: %+v", tc.name, holding.LotID, holding)
					}
				} else if holding.LotID == "lot-pnl-a" && holding.UnrealisedPnL != nil {
					t.Fatalf("%s exposed P/L for %s: %+v", tc.name, holding.LotID, holding)
				}
			}
		})
	}
}

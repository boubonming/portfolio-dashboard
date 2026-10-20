package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestReviewHTTPDuplicateInstrumentNormalizedCorruption(t *testing.T) {
	handler, database, submission, snapshotID := reviewBoundaryFixture(t)
	defer database.Close()
	now := canonicalReviewFixtureTimestamp(time.Now().UTC())
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-api-2", "FIX2", "Fixture instrument 2", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-api", "instrument-api-2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?)`, "lot-api-2", "portfolio-api", "instrument-api-2", "1", "2", "2", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddQuote(context.Background(), store.QuoteInput{InstrumentID: "instrument-api-2", Price: "2", Currency: "USD", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatal(err)
	}
	status, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "review.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil || !status.Complete {
		t.Fatalf("second snapshot=%+v err=%v", status, err)
	}
	snapshotID = status.SnapshotID
	submission.SnapshotID = snapshotID
	submission.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{"lot-api", "lot-api-2"}, InstrumentIDs: []string{"instrument-api", "instrument-api-2"}}
	body, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}

	key := "duplicate-instrument-key"
	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var result domain.AdvisoryReview
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DROP TRIGGER phase_d_review_targets_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`UPDATE review_targets SET instrument_id='instrument-api' WHERE review_id=? AND ordinal=1`, result.ReviewID); err != nil {
		t.Fatal(err)
	}
	response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
	assertPhaseDError(t, response, http.StatusInternalServerError, "internal_error")
}

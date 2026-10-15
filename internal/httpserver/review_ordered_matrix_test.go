package httpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestReviewHTTPEveryOrderedChildFamilyExtraAndOrdinalMatrix(t *testing.T) {
	families := []struct {
		name, extraSQL, gapSQL string
		argsExtra, argsGap     []any
	}{
		{"targets", `INSERT INTO review_targets(review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal) VALUES(?,?,?,?,?,?,?)`, `UPDATE review_targets SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", "missing-snapshot", "review.v1", "extra-holding", "instrument-api", 1}, nil},
		{"risks", `INSERT INTO review_risks(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, `UPDATE review_risks SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 1, "extra"}, nil},
		{"counterarguments", `INSERT INTO review_counterarguments(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, `UPDATE review_counterarguments SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 1, "extra"}, nil},
		{"conditions", `INSERT INTO review_conditions(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, `UPDATE review_conditions SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 1, "extra"}, nil},
		{"metrics", `INSERT INTO review_metrics(review_id,portfolio_id,ordinal,name,value,unit,as_of,source,deterministic) VALUES(?,?,?,?,?,?,?,?,?)`, `UPDATE review_metrics SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 1, "extra", "1", "USD", "2026-01-15T08:00:00.000000000Z", "submitted", 1}, nil},
		{"evidence", `INSERT INTO review_evidence(review_id,portfolio_id,evidence_id,ordinal,url_host,url_suffix,title,accessed_at,availability,freshness,excerpt) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, `UPDATE review_evidence SET ordinal=7 WHERE review_id=?`, []any{"portfolio-api", "extra-evidence", 1, "example.test", "/extra", "extra", "2026-01-15T08:00:00.000000000Z", "available", "current", "extra"}, nil},
		{"sourced-facts", `INSERT INTO review_sourced_facts(review_id,portfolio_id,fact_ordinal,statement) VALUES(?,?,?,?)`, `UPDATE review_sourced_facts SET fact_ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 1, "unused fact"}, nil},
		{"fact-evidence", `INSERT INTO review_fact_evidence(review_id,portfolio_id,fact_ordinal,evidence_id,evidence_ordinal) VALUES(?,?,?,?,?)`, `UPDATE review_fact_evidence SET evidence_ordinal=7 WHERE review_id=?`, []any{"portfolio-api", 0, "missing-evidence", 1}, nil},
		{"permission-snapshots", `INSERT INTO review_submission_permission_snapshots(review_id,portfolio_id,ordinal,permission) VALUES(?,?,?,?)`, `UPDATE review_submission_permission_snapshots SET ordinal=2 WHERE review_id=?`, []any{"portfolio-api", 2, "reviews:write"}, nil},
	}
	for _, family := range families {
		for _, mode := range []string{"extra", "ordinal-gap"} {
			t.Run(family.name+"/"+mode, func(t *testing.T) {
				handler, database, submission, _ := reviewBoundaryFixture(t)
				defer database.Close()
				submission.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{"lot-api"}, InstrumentIDs: []string{"instrument-api"}}
				submission.Risks = []string{"risk"}
				submission.Counterarguments = []string{"counterargument"}
				submission.Conditions = []string{"condition"}
				submission.Metrics = []domain.ReviewMetric{{Name: "metric", Value: "1.25", Unit: "USD", AsOf: "2026-01-15T08:00:00.000000000Z", Source: "submitted", Deterministic: true}}
				body, err := json.Marshal(submission)
				if err != nil {
					t.Fatal(err)
				}
				key := "ordered-" + family.name + "-" + mode + "-key"
				created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
				if created.Code != http.StatusCreated {
					t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
				}
				var result domain.AdvisoryReview
				if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				prepareReviewCorruption(t, database, result.ReviewID)
				if mode == "extra" {
					args := append([]any{result.ReviewID}, family.argsExtra...)
					if _, err := database.DB.Exec(family.extraSQL, args...); err != nil {
						t.Fatal(err)
					}
				} else if _, err := database.DB.Exec(family.gapSQL, result.ReviewID); err != nil {
					t.Fatal(err)
				}
				response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
				assertPhaseDError(t, response, http.StatusInternalServerError, "internal_error")
			})
		}
	}
}

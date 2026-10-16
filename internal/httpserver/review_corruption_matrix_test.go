package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

var reviewCorruptionTables = []string{
	"advisory_reviews", "review_targets", "review_risks", "review_counterarguments", "review_conditions", "review_metrics", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios", "activity_events",
}

func reviewHTTPCounts(t *testing.T, database *store.Store) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(reviewCorruptionTables))
	for _, table := range reviewCorruptionTables {
		query := "SELECT COUNT(*) FROM " + table
		if table == "audit_events" || table == "activity_events" {
			query += " WHERE action='review_submitted'"
		}
		if table == "audit_event_portfolios" {
			query = "SELECT COUNT(*) FROM audit_event_portfolios p JOIN audit_events a ON a.id=p.audit_event_id WHERE a.action='review_submitted'"
		}
		var count int
		if err := database.DB.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func prepareReviewCorruption(t *testing.T, database *store.Store, reviewID string) {
	t.Helper()
	for _, table := range []string{"advisory_reviews", "review_targets", "review_risks", "review_counterarguments", "review_conditions", "review_metrics", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios", "activity_events"} {
		trigger := "phase_d_" + table + "_update"
		if table == "advisory_reviews" {
			trigger = "phase_d_review_append_only_update"
		} else if table == "review_submission_permission_snapshots" {
			trigger = "phase_d_review_submission_permission_snapshots_update"
		} else if table == "review_idempotency" {
			trigger = "phase_d_review_idempotency_update"
		} else if table == "audit_events" {
			trigger = "phase_d_audit_update"
		} else if table == "audit_event_portfolios" {
			trigger = "phase_d_audit_scope_update"
		} else if table == "activity_events" {
			trigger = "activity_events_append_only_update"
		}
		if _, err := database.DB.Exec("DROP TRIGGER IF EXISTS " + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec("DROP TRIGGER IF EXISTS phase_d_review_submission_permission_snapshots_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("DROP TRIGGER IF EXISTS phase_d_review_permission_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec("PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	_ = reviewID
}

func TestReviewHTTPNormalizedCorruptionMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *store.Store, string)
	}{
		{"target-wrong-snapshot", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET snapshot_id='wrong-snapshot' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-wrong-calculation-version", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET calculation_version='wrong-version' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-nonmember-holding", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET holding_id='foreign-holding' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-mismatched-instrument", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET instrument_id='foreign-instrument' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-foreign-portfolio", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-duplicate-holding", func(t *testing.T, db *store.Store, id string) {
			replaceReviewTargetsWithoutConstraints(t, db, id, func(row reviewTargetCorruptionRow) reviewTargetCorruptionRow {
				row.instrumentID = "duplicate-instrument"
				row.ordinal = 1
				return row
			})
		}},
		{"target-duplicate-instrument", func(t *testing.T, db *store.Store, id string) {
			replaceReviewTargetsWithoutConstraints(t, db, id, func(row reviewTargetCorruptionRow) reviewTargetCorruptionRow {
				row.holdingID = "duplicate-holding"
				row.ordinal = 1
				return row
			})
		}},
		{"target-hidden-foreign-owner-row", func(t *testing.T, db *store.Store, id string) {
			replaceReviewTargetsWithoutConstraints(t, db, id, func(row reviewTargetCorruptionRow) reviewTargetCorruptionRow {
				row.portfolioID = "foreign-portfolio"
				row.holdingID = "foreign-holding"
				row.instrumentID = "foreign-instrument"
				row.ordinal = 1
				return row
			})
		}},
		{"target-ordinal-gap", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"target-malformed-id", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_targets SET holding_id='bad id' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"risk-foreign-row", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_risks SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"risk-ordinal-gap", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_risks SET ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"counterargument-foreign-row", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_counterarguments SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"condition-ordinal-gap", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_conditions SET ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"metric-malformed-decimal", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_metrics SET value='1.2.3' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"metric-malformed-timestamp", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_metrics SET as_of='not-a-time' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"metric-malformed-enum", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_metrics SET source='invalid' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"evidence-foreign-row", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_evidence SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"evidence-ordinal-gap", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_evidence SET ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"evidence-malformed-timestamp", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_evidence SET accessed_at='not-a-time' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-foreign-row", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_sourced_facts SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-ordinal-gap", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_sourced_facts SET fact_ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-malformed-text", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_sourced_facts SET statement=char(1) WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-evidence-cross-review", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_fact_evidence SET review_id='foreign-review' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-evidence-cross-portfolio", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_fact_evidence SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-evidence-gapped-ordinal", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_fact_evidence SET evidence_ordinal=7 WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"fact-evidence-mismatched-witness", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_fact_evidence SET evidence_id='other-evidence' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"permission-wrong", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_submission_permission_snapshots SET permission='reviews:write' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"permission-foreign-row", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_submission_permission_snapshots SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"permission-missing", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`DELETE FROM review_submission_permission_snapshots WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-scope", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET scope_type='invalid' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-recommendation", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET recommendation='invalid' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-status", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET historical_status='invalid' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-principal-kind", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET submitted_by_kind='browser' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-primary-permission", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET submitted_by_primary_permission='portfolio:read' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-snapshot-association", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET snapshot_id='wrong-snapshot' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-invalid-version-association", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET calculation_version='wrong-version' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"parent-witness-contradiction", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE advisory_reviews SET primary_evidence_id='missing-evidence' WHERE id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"idempotency-principal-mismatch", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_idempotency SET principal_id='foreign-principal' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"idempotency-portfolio-mismatch", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_idempotency SET portfolio_id='foreign-portfolio' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"child-scan-error", func(t *testing.T, db *store.Store, id string) {
			_, err := db.DB.Exec(`UPDATE review_evidence SET ordinal=x'ff' WHERE review_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "corrupt-"+tc.name+"-key", "application/json", body)
			if created.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
			}
			var result domain.AdvisoryReview
			if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			prepareReviewCorruption(t, database, result.ReviewID)
			tc.mutate(t, database, result.ReviewID)
			before := reviewHTTPCounts(t, database)
			readShouldRemainAvailable := strings.HasPrefix(tc.name, "idempotency-")
			for _, path := range []string{"/api/v1/portfolios/portfolio-api/reviews", "/api/v1/portfolios/portfolio-api/reviews/" + result.ReviewID} {
				read := reviewBoundaryRequest(handler, http.MethodGet, path, reviewBoundarySecret, "", "", nil)
				if readShouldRemainAvailable {
					if read.Code != http.StatusOK {
						t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
					}
				} else {
					assertPhaseDError(t, read, http.StatusInternalServerError, "internal_error")
					if bytes.Contains(read.Body.Bytes(), []byte(result.ReviewID)) || bytes.Contains(read.Body.Bytes(), []byte("rationale")) {
						t.Fatalf("read corruption leaked content: %s", read.Body.String())
					}
				}
			}
			if after := reviewHTTPCounts(t, database); !reflect.DeepEqual(after, before) {
				t.Fatalf("read changed counts before=%v after=%v", before, after)
			}
			replay := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "corrupt-"+tc.name+"-key", "application/json", body)
			assertPhaseDError(t, replay, http.StatusInternalServerError, "internal_error")
			if bytes.Contains(replay.Body.Bytes(), []byte(result.ReviewID)) || bytes.Contains(replay.Body.Bytes(), []byte("rationale")) {
				t.Fatalf("corruption leaked content: %s", replay.Body.String())
			}
			if after := reviewHTTPCounts(t, database); !reflect.DeepEqual(after, before) {
				t.Fatalf("counts changed before=%v after=%v", before, after)
			}
		})
	}
}

type reviewTargetCorruptionRow struct {
	reviewID, portfolioID, snapshotID, calculationVersion string
	holdingID, instrumentID                               string
	ordinal                                               int
}

func replaceReviewTargetsWithoutConstraints(t *testing.T, database *store.Store, reviewID string, mutate func(reviewTargetCorruptionRow) reviewTargetCorruptionRow) {
	t.Helper()
	var original reviewTargetCorruptionRow
	if err := database.DB.QueryRow(`SELECT review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal FROM review_targets WHERE review_id=? ORDER BY ordinal LIMIT 1`, reviewID).Scan(&original.reviewID, &original.portfolioID, &original.snapshotID, &original.calculationVersion, &original.holdingID, &original.instrumentID, &original.ordinal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`CREATE TABLE review_targets_corrupt AS SELECT review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal FROM review_targets`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DROP TABLE review_targets`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`CREATE TABLE review_targets (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, calculation_version TEXT NOT NULL, holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL, ordinal INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO review_targets SELECT review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal FROM review_targets_corrupt`); err != nil {
		t.Fatal(err)
	}
	extra := mutate(original)
	if _, err := database.DB.Exec(`INSERT INTO review_targets(review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal) VALUES(?,?,?,?,?,?,?)`, extra.reviewID, extra.portfolioID, extra.snapshotID, extra.calculationVersion, extra.holdingID, extra.instrumentID, extra.ordinal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DROP TABLE review_targets_corrupt`); err != nil {
		t.Fatal(err)
	}
}

func TestReviewHTTPCorruptionMissingParentBehindReservation(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "missing-parent-reservation-key", "application/json", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var result domain.AdvisoryReview
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DROP TRIGGER phase_d_review_append_only_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`DELETE FROM advisory_reviews WHERE id=?`, result.ReviewID); err != nil {
		t.Fatal(err)
	}
	response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "missing-parent-reservation-key", "application/json", body)
	assertPhaseDError(t, response, http.StatusInternalServerError, "internal_error")
}

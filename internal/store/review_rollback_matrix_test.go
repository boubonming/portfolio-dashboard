package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestReviewRollbackFailureMatrixAllOwnedFamilies(t *testing.T) {
	failures := []struct {
		name  string
		table string
		when  string
	}{
		{"parent", "advisory_reviews", "BEFORE INSERT"},
		{"targets", "review_targets", "BEFORE INSERT"},
		{"risks", "review_risks", "BEFORE INSERT"},
		{"counterarguments", "review_counterarguments", "BEFORE INSERT"},
		{"conditions", "review_conditions", "BEFORE INSERT"},
		{"metrics", "review_metrics", "BEFORE INSERT"},
		{"evidence", "review_evidence", "BEFORE INSERT"},
		{"sourced-facts", "review_sourced_facts", "BEFORE INSERT"},
		{"fact-evidence", "review_fact_evidence", "BEFORE INSERT"},
		{"permission-snapshot", "review_submission_permission_snapshots", "BEFORE INSERT"},
		{"idempotency", "review_idempotency", "BEFORE INSERT"},
		{"audit", "audit_events", "BEFORE INSERT"},
		{"audit-association", "audit_event_portfolios", "BEFORE INSERT"},
		{"activity", "activity_events", "BEFORE INSERT"},
		{"final-reconstruction", "review_evidence", "AFTER INSERT"},
		{"final-commit", "activity_events", "AFTER INSERT"},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			f := newReviewRegressionFixture(t)
			defer f.close()
			f.submission.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{f.snapshot.Holdings[0].HoldingID}, InstrumentIDs: []string{f.snapshot.Holdings[0].InstrumentID}}
			f.submission.Metrics = []domain.ReviewMetric{{Name: "metric", Value: "1.25", Unit: "USD", AsOf: "2026-01-02T12:00:00.000000000Z", Source: "submitted", Deterministic: true}}
			beforeUnrelated := unrelatedReviewPreservationCounts(t, f)
			condition := "1"
			if failure.table == "audit_events" || failure.table == "activity_events" {
				condition = "NEW.action='review_submitted'"
			}
			trigger := "fail_review_all_" + strings.ReplaceAll(failure.name, "-", "_")
			statement := fmt.Sprintf("CREATE TRIGGER %s %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT, 'regression rollback failure'); END", trigger, failure.when, failure.table, condition)
			if _, err := f.store.DB.Exec(statement); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := ReviewFingerprint(f.submission)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "rollback-all-"+failure.name, fingerprint, f.submission); err == nil {
				t.Fatal("failure trigger did not abort submission")
			}
			counts := reviewTableCounts(t, f.store)
			for _, table := range []string{"advisory_reviews", "review_targets", "review_risks", "review_counterarguments", "review_conditions", "review_metrics", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios", "activity_events"} {
				if counts[table] != 0 {
					t.Fatalf("%s retained rows after %s failure: %d", table, failure.name, counts[table])
				}
			}
			if after := unrelatedReviewPreservationCounts(t, f); !reflect.DeepEqual(beforeUnrelated, after) {
				t.Fatalf("unrelated data changed before=%v after=%v", beforeUnrelated, after)
			}
		})
	}
}

func unrelatedReviewPreservationCounts(t *testing.T, f *reviewRegressionFixture) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"lots", "snapshots", "portfolio_theses", "accounts", "quotes", "fx_rates", "instrument_provider_mappings"} {
		var count int
		if err := f.store.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

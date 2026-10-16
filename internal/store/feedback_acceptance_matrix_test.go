package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func feedbackCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"review_feedback", "feedback_permission_snapshots", "feedback_idempotency", "audit_events", "audit_event_portfolios", "activity_events"} {
		query := "SELECT COUNT(*) FROM " + table
		args := []any{}
		switch table {
		case "audit_events":
			query += " WHERE action='feedback_submitted'"
		case "audit_event_portfolios":
			query = "SELECT COUNT(*) FROM audit_event_portfolios p JOIN audit_events a ON a.id=p.audit_event_id WHERE a.action='feedback_submitted'"
		case "activity_events":
			query += " WHERE action='feedback_submitted'"
		}
		var count int
		if err := s.DB.QueryRow(query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func createFeedbackFixture(t *testing.T) (*reviewRegressionFixture, domain.AdvisoryReview, string, domain.FeedbackSubmission, string) {
	t.Helper()
	fixture := newReviewRegressionFixture(t)
	review, replay := submitRegressionReview(t, fixture, "feedback-acceptance-review-key-0001", fixture.submission)
	if replay {
		t.Fatal("review fixture unexpectedly replayed")
	}
	submission := domain.FeedbackSubmission{Decision: "disagree"}
	fingerprint, err := FeedbackFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, review, "feedback-acceptance-key-0001", submission, fingerprint
}

func TestFeedbackStoreExactAccountingReplayAndImmutableAttribution(t *testing.T) {
	fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
	defer fixture.close()
	fixture.store.SetClock(func() time.Time { return time.Date(2026, time.January, 9, 10, 11, 12, 123456789, time.UTC) })
	created, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
	if err != nil || replay || created.FeedbackID == "" {
		t.Fatalf("created=%+v replay=%v err=%v", created, replay, err)
	}
	want := map[string]int{"review_feedback": 1, "feedback_permission_snapshots": 1, "feedback_idempotency": 1, "audit_events": 1, "audit_event_portfolios": 1, "activity_events": 1}
	if got := feedbackCounts(t, fixture.store); !reflect.DeepEqual(got, want) {
		t.Fatalf("new accounting=%v want=%v", got, want)
	}
	replayed, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, domain.FeedbackSubmission{Decision: "disagree", Note: nil})
	if err != nil || !replay || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("replay=%+v flag=%v err=%v want=%+v", replayed, replay, err, created)
	}
	if got := feedbackCounts(t, fixture.store); !reflect.DeepEqual(got, want) {
		t.Fatalf("replay accounting=%v want=%v", got, want)
	}
	var principal, portfolio, reviewID, decision, note, occurred, retained, submittedID, submittedKind, permission string
	if err := fixture.store.DB.QueryRow(`SELECT browser_principal_id,portfolio_id,review_id,decision,COALESCE(note,''),occurred_at,retain_until,submitted_by_id,submitted_by_kind,submitted_by_primary_permission FROM review_feedback WHERE id=?`, created.FeedbackID).Scan(&principal, &portfolio, &reviewID, &decision, &note, &occurred, &retained, &submittedID, &submittedKind, &permission); err != nil {
		t.Fatal(err)
	}
	if principal != "local-owner" || portfolio != fixture.portfolio || reviewID != review.ReviewID || decision != "disagree" || note != "" || occurred != "2026-01-09T10:11:12.123456789Z" || retained != "2033-01-09T10:11:12.123456789Z" || submittedID != "local-owner" || submittedKind != "browser_user" || permission != "portfolio:read" {
		t.Fatalf("feedback attribution=%q/%q/%q/%q/%q/%q/%q/%q/%q/%q", principal, portfolio, reviewID, decision, note, occurred, retained, submittedID, submittedKind, permission)
	}
	var auditPayload, actor, action, entityType, entityID string
	if err := fixture.store.DB.QueryRow(`SELECT actor,action,entity_type,entity_id,payload FROM audit_events WHERE action='feedback_submitted'`).Scan(&actor, &action, &entityType, &entityID, &auditPayload); err != nil {
		t.Fatal(err)
	}
	if actor != "browser" || action != "feedback_submitted" || entityType != "review_feedback" || entityID != created.FeedbackID || auditPayload == "" {
		t.Fatalf("audit=%q/%q/%q/%q payload=%q", actor, action, entityType, entityID, auditPayload)
	}
	var activityActor, activityAction, category, activityEntity, summary, details string
	if err := fixture.store.DB.QueryRow(`SELECT actor_category,action,category,entity_id,summary,details FROM activity_events WHERE action='feedback_submitted'`).Scan(&activityActor, &activityAction, &category, &activityEntity, &summary, &details); err != nil {
		t.Fatal(err)
	}
	if activityActor != "browser" || activityAction != "feedback_submitted" || category != "feedback" || activityEntity != created.FeedbackID || summary != "Review feedback submitted" || details != "Attributable human feedback was submitted." {
		t.Fatalf("activity=%q/%q/%q/%q/%q/%q", activityActor, activityAction, category, activityEntity, summary, details)
	}
}

func TestFeedbackStoreKeyNamespaceAndValidationMatrix(t *testing.T) {
	fixture := newReviewRegressionFixture(t)
	defer fixture.close()
	fingerprint, err := FeedbackFingerprint(domain.FeedbackSubmission{Decision: "agree"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, principal, portfolio, review, key, fp string
		want                                        error
	}{
		{"principal", "not-local-owner", fixture.portfolio, "review", "feedback-invalid-principal-0001", fingerprint, ErrFeedbackInvalid},
		{"portfolio", "local-owner", "", "review", "feedback-invalid-portfolio-0001", fingerprint, ErrFeedbackInvalid},
		{"review-id", "local-owner", fixture.portfolio, "", "feedback-invalid-review-0001", fingerprint, ErrFeedbackInvalid},
		{"key", "local-owner", fixture.portfolio, "review", "bad", fingerprint, ErrFeedbackInvalid},
		{"fingerprint", "local-owner", fixture.portfolio, "review", "feedback-invalid-fp-00001", "not-canonical", ErrFeedbackInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, replay, callErr := fixture.store.SubmitReviewFeedback(context.Background(), tc.principal, tc.portfolio, tc.review, tc.key, tc.fp, domain.FeedbackSubmission{Decision: "agree"})
			if !errors.Is(callErr, tc.want) || replay {
				t.Fatalf("replay=%v err=%v want=%v", replay, callErr, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name        string
		submission  domain.FeedbackSubmission
		fingerprint string
	}{
		{"decision", domain.FeedbackSubmission{Decision: "invalid"}, strings.Repeat("0", 64)},
		{"empty-note", domain.FeedbackSubmission{Decision: "agree", Note: stringPtr("")}, strings.Repeat("0", 64)},
		{"overlong-note", domain.FeedbackSubmission{Decision: "agree", Note: stringPtr(strings.Repeat("x", 2001))}, strings.Repeat("0", 64)},
		{"malformed-fingerprint", domain.FeedbackSubmission{Decision: "agree"}, strings.Repeat("0", 64)},
	} {
		t.Run("submission/"+tc.name, func(t *testing.T) {
			_, replay, callErr := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, "review", "feedback-invalid-submission-0001", tc.fingerprint, tc.submission)
			if !errors.Is(callErr, ErrFeedbackInvalid) || replay {
				t.Fatalf("replay=%v err=%v want=%v", replay, callErr, ErrFeedbackInvalid)
			}
		})
	}
	first, replay := submitRegressionReview(t, fixture, "feedback-namespace-review-0001", fixture.submission)
	if replay {
		t.Fatal("first review replayed")
	}
	created, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, first.ReviewID, "feedback-namespace-key-0001", fingerprint, domain.FeedbackSubmission{Decision: "agree"})
	if err != nil || replay || created.FeedbackID == "" {
		t.Fatalf("first feedback=%+v replay=%v err=%v", created, replay, err)
	}
	second, replay := submitRegressionReview(t, fixture, "feedback-namespace-review-0002", fixture.submission)
	if replay {
		t.Fatal("second review replayed")
	}
	createdSecond, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, second.ReviewID, "feedback-namespace-key-0001", fingerprint, domain.FeedbackSubmission{Decision: "agree"})
	if err != nil || replay || createdSecond.FeedbackID == created.FeedbackID {
		t.Fatalf("same key was not review-scoped: second=%+v replay=%v err=%v", createdSecond, replay, err)
	}
	conflicting := domain.FeedbackSubmission{Decision: "defer"}
	conflictingFingerprint, err := FeedbackFingerprint(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err = fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, first.ReviewID, "feedback-namespace-key-0001", conflictingFingerprint, conflicting)
	if !errors.Is(err, ErrFeedbackIdempotencyConflict) || replay {
		t.Fatalf("same review conflict replay=%v err=%v", replay, err)
	}
}

func TestFeedbackStoreReplayCorruptionMatrixReturnsNoPartialData(t *testing.T) {
	cases := []struct {
		name    string
		removed map[string]int
		mutate  func(*testing.T, *reviewRegressionFixture, domain.ReviewFeedback)
	}{
		{"missing-idempotency", map[string]int{"feedback_idempotency": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_idempotency_delete", "feedback_idempotency")
			execFeedbackMutation(t, f, `DELETE FROM feedback_idempotency WHERE feedback_id=?`, feedback.FeedbackID)
		}},
		{"missing-feedback", map[string]int{"review_feedback": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_append_only_delete", "review_feedback")
			execFeedbackMutation(t, f, `PRAGMA foreign_keys=OFF`)
			execFeedbackMutation(t, f, `DELETE FROM review_feedback WHERE id=?`, feedback.FeedbackID)
			execFeedbackMutation(t, f, `PRAGMA foreign_keys=ON`)
		}},
		{"dangling-idempotency", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_idempotency_update", "feedback_idempotency")
			execFeedbackMutation(t, f, `PRAGMA foreign_keys=OFF`)
			execFeedbackMutation(t, f, `UPDATE feedback_idempotency SET feedback_id='feedback-missing-dangling' WHERE feedback_id=?`, feedback.FeedbackID)
			execFeedbackMutation(t, f, `PRAGMA foreign_keys=ON`)
		}},
		{"extra-idempotency", map[string]int{"feedback_idempotency": -1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			var fingerprint, createdAt, retainUntil string
			if err := f.store.DB.QueryRow(`SELECT request_fingerprint,created_at,retain_until FROM feedback_idempotency WHERE feedback_id=?`, feedback.FeedbackID).Scan(&fingerprint, &createdAt, &retainUntil); err != nil {
				t.Fatal(err)
			}
			execFeedbackMutation(t, f, `INSERT INTO feedback_idempotency(browser_principal_id,portfolio_id,review_id,idempotency_key,request_fingerprint,feedback_id,created_at,retain_until) SELECT browser_principal_id,portfolio_id,review_id,'feedback-extra-reservation-0001',request_fingerprint,feedback_id,created_at,retain_until FROM feedback_idempotency WHERE feedback_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-feedback", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_append_only_update", "review_feedback")
			execFeedbackMutation(t, f, `UPDATE review_feedback SET decision='defer' WHERE id=?`, feedback.FeedbackID)
		}},

		{"missing-permission", map[string]int{"feedback_permission_snapshots": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_permission_snapshots_delete", "feedback_permission_snapshots")
			execFeedbackMutation(t, f, `DELETE FROM feedback_permission_snapshots WHERE feedback_id=?`, feedback.FeedbackID)
		}},
		{"missing-audit", map[string]int{"activity_events": 1, "audit_event_portfolios": 1, "audit_events": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_delete", "activity_events")
			dropFeedbackTrigger(t, f, "phase_d_audit_scope_delete", "audit_event_portfolios")
			dropFeedbackTrigger(t, f, "phase_d_audit_delete", "audit_events")
			execFeedbackMutation(t, f, `DELETE FROM activity_events WHERE entity_id=?`, feedback.FeedbackID)
			var auditID string
			if err := f.store.DB.QueryRow(`SELECT id FROM audit_events WHERE entity_id=?`, feedback.FeedbackID).Scan(&auditID); err != nil {
				t.Fatal(err)
			}
			execFeedbackMutation(t, f, `DELETE FROM audit_event_portfolios WHERE audit_event_id=?`, auditID)
			execFeedbackMutation(t, f, `DELETE FROM audit_events WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"missing-audit-scope", map[string]int{"activity_events": 1, "audit_event_portfolios": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_delete", "activity_events")
			dropFeedbackTrigger(t, f, "phase_d_audit_scope_delete", "audit_event_portfolios")
			var auditID string
			if err := f.store.DB.QueryRow(`SELECT id FROM audit_events WHERE entity_id=?`, feedback.FeedbackID).Scan(&auditID); err != nil {
				t.Fatal(err)
			}
			execFeedbackMutation(t, f, `DELETE FROM activity_events WHERE entity_id=?`, feedback.FeedbackID)
			execFeedbackMutation(t, f, `DELETE FROM audit_event_portfolios WHERE audit_event_id=?`, auditID)
		}},
		{"missing-activity", map[string]int{"activity_events": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_delete", "activity_events")
			execFeedbackMutation(t, f, `DELETE FROM activity_events WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-activity-id", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_update", "activity_events")
			execFeedbackMutation(t, f, `UPDATE activity_events SET id='activity-corrupt-id' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-fingerprint", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_feedback_idempotency_update", "feedback_idempotency")
			execFeedbackMutation(t, f, `UPDATE feedback_idempotency SET request_fingerprint=? WHERE feedback_id=?`, "bad", feedback.FeedbackID)
		}},
		{"mismatched-audit-action", map[string]int{"audit_events": 1, "audit_event_portfolios": 1}, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_audit_update", "audit_events")
			execFeedbackMutation(t, f, `UPDATE audit_events SET action='review_submitted' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-audit-payload", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_audit_update", "audit_events")
			execFeedbackMutation(t, f, `UPDATE audit_events SET payload='{}' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-audit-time", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "phase_d_audit_update", "audit_events")
			execFeedbackMutation(t, f, `UPDATE audit_events SET created_at='2026-01-10T10:11:12.123456789Z' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-activity-owner", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_update", "activity_events")
			execFeedbackMutation(t, f, `UPDATE activity_events SET actor_category='system' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-activity-summary", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_update", "activity_events")
			execFeedbackMutation(t, f, `UPDATE activity_events SET summary='wrong' WHERE entity_id=?`, feedback.FeedbackID)
		}},
		{"mismatched-activity-time", nil, func(t *testing.T, f *reviewRegressionFixture, feedback domain.ReviewFeedback) {
			dropFeedbackTrigger(t, f, "activity_events_append_only_update", "activity_events")
			execFeedbackMutation(t, f, `UPDATE activity_events SET occurred_at='2026-01-10T10:11:12.123456789Z',sort_time='2026-01-10T10:11:12.123456789Z' WHERE entity_id=?`, feedback.FeedbackID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
			defer fixture.close()
			feedback, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
			if err != nil || replay {
				t.Fatalf("setup feedback=%+v replay=%v err=%v", feedback, replay, err)
			}
			before := feedbackCounts(t, fixture.store)
			tc.mutate(t, fixture, feedback)
			_, replay, err = fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
			if !errors.Is(err, ErrFeedbackCorrupt) || replay {
				t.Fatalf("replay=%v err=%v want corruption", replay, err)
			}
			want := make(map[string]int, len(before))
			for table, count := range before {
				want[table] = count
			}
			for table, delta := range tc.removed {
				want[table] -= delta
			}
			if after := feedbackCounts(t, fixture.store); !reflect.DeepEqual(after, want) {
				t.Fatalf("corrupt replay wrote rows: before=%v after=%v want=%v", before, after, want)
			}
		})
	}
}

func TestFeedbackStoreRejectsCorruptedActivityID(t *testing.T) {
	fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
	defer fixture.close()
	feedback, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
	if err != nil || replay {
		t.Fatalf("setup feedback=%+v replay=%v err=%v", feedback, replay, err)
	}
	dropFeedbackTrigger(t, fixture, "activity_events_append_only_update", "activity_events")
	execFeedbackMutation(t, fixture, `UPDATE activity_events SET id='activity-corrupt-id' WHERE entity_id=?`, feedback.FeedbackID)
	_, replay, err = fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
	if !errors.Is(err, ErrFeedbackCorrupt) || replay {
		t.Fatalf("replay=%v err=%v want corruption", replay, err)
	}
}

func dropFeedbackTrigger(t *testing.T, f *reviewRegressionFixture, trigger, table string) {
	t.Helper()
	if _, err := f.store.DB.Exec("DROP TRIGGER " + trigger); err != nil {
		t.Fatalf("drop %s for %s: %v", trigger, table, err)
	}
}

func execFeedbackMutation(t *testing.T, f *reviewRegressionFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.store.DB.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackStoreRollbackInjectionLeavesZeroOwnedRows(t *testing.T) {
	for _, tc := range []struct{ name, table, trigger string }{
		{"feedback", "review_feedback", "phase_d_feedback_append_only_update"},
		{"permission", "feedback_permission_snapshots", "phase_d_feedback_permission_snapshots_update"},
		{"idempotency", "feedback_idempotency", "phase_d_feedback_idempotency_update"},
		{"audit", "audit_events", "phase_d_audit_update"},
		{"audit-portfolio", "audit_event_portfolios", "phase_d_audit_scope_update"},
		{"activity", "activity_events", "activity_events_append_only_update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
			defer fixture.close()
			// Use a BEFORE INSERT failure trigger; append-only update triggers are
			// dropped only to permit a deterministic isolated write failure.
			triggerName := "fail_feedback_" + strings.ReplaceAll(tc.name, "-", "_")
			if _, err := fixture.store.DB.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'feedback injection'); END", triggerName, tc.table)); err != nil {
				t.Fatal(err)
			}
			_, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
			if err == nil || replay {
				t.Fatalf("replay=%v err=%v", replay, err)
			}
			counts := feedbackCounts(t, fixture.store)
			for table, count := range counts {
				if count != 0 {
					t.Fatalf("%s retained %d rows after %s failure", table, count, tc.name)
				}
			}
		})
	}
}

func TestFeedbackStoreConcurrentConflictingRequestsHaveOneGraph(t *testing.T) {
	fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
	defer fixture.close()
	const workers = 8
	stores := make([]*Store, workers)
	for i := range stores {
		var err error
		stores[i], err = Open(context.Background(), fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[i].Close()
	}
	results := make(chan struct {
		replay bool
		err    error
	}, workers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for i, database := range stores {
		group.Add(1)
		go func(i int, database *Store) {
			defer group.Done()
			<-start
			callSubmission := submission
			callFingerprint := fingerprint
			if i%2 == 1 {
				callSubmission = domain.FeedbackSubmission{Decision: "defer"}
				callFingerprint, _ = FeedbackFingerprint(callSubmission)
			}
			_, callReplay, callErr := database.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, callFingerprint, callSubmission)
			results <- struct {
				replay bool
				err    error
			}{callReplay, callErr}
		}(i, database)
	}
	close(start)
	group.Wait()
	close(results)
	created, replays, conflicts := 0, 0, 0
	for result := range results {
		switch {
		case result.err == nil && !result.replay:
			created++
		case result.err == nil && result.replay:
			replays++
		case errors.Is(result.err, ErrFeedbackIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", result.err)
		}
	}
	if created != 1 || replays != workers/2-1 || conflicts != workers/2 {
		t.Fatalf("created=%d replays=%d conflicts=%d", created, replays, conflicts)
	}
	want := map[string]int{"review_feedback": 1, "feedback_permission_snapshots": 1, "feedback_idempotency": 1, "audit_events": 1, "audit_event_portfolios": 1, "activity_events": 1}
	if got := feedbackCounts(t, fixture.store); !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent graph=%v want=%v", got, want)
	}
}

func TestFeedbackStoreExistingPortfolioDataIsUnchanged(t *testing.T) {
	fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
	defer fixture.close()
	before := make(map[string]int)
	for _, table := range []string{"snapshots", "advisory_reviews", "review_evidence", "review_sourced_facts", "lots", "portfolio_theses", "thesis_revisions", "machine_principals"} {
		var count int
		if err := fixture.store.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		before[table] = count
	}
	if _, _, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission); err != nil {
		t.Fatal(err)
	}
	for table, want := range before {
		var got int
		if err := fixture.store.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s changed from %d to %d", table, want, got)
		}
	}
}

package store

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestReviewFeedbackAppendReplayAndCanonicalNullNote(t *testing.T) {
	fixture := newReviewRegressionFixture(t)
	defer fixture.close()
	submission := domain.FeedbackSubmission{Decision: "agree"}
	fingerprint, err := FeedbackFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	created, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, "missing-review", "feedback-test-key-0001", fingerprint, submission)
	if err == nil || replay || !reflect.DeepEqual(created, domain.ReviewFeedback{}) {
		t.Fatalf("missing review result=%+v replay=%v err=%v", created, replay, err)
	}
	// Submit against the real review created through the normal review path.
	reviewFingerprint, err := ReviewFingerprint(fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err := fixture.store.SubmitAdvisoryReview(context.Background(), fixture.principal, fixture.portfolio, "feedback-review-key-0001", reviewFingerprint, fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	created, replay, err = fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, "feedback-test-key-0001", fingerprint, submission)
	if err != nil || replay || created.Note != nil || created.Decision != "agree" || created.SubmittedBy.PrincipalID != "local-owner" {
		t.Fatalf("created=%+v replay=%v err=%v", created, replay, err)
	}
	nullSubmission := domain.FeedbackSubmission{Decision: "agree", Note: nil}
	nullFingerprint, err := FeedbackFingerprint(nullSubmission)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, "feedback-test-key-0001", nullFingerprint, nullSubmission)
	if err != nil || !replay || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("null replay=%+v replayed=%v err=%v", replayed, replay, err)
	}
	present := "different"
	presentSubmission := domain.FeedbackSubmission{Decision: "agree", Note: &present}
	presentFingerprint, err := FeedbackFingerprint(presentSubmission)
	if err != nil {
		t.Fatal(err)
	}
	conflict, replay, err := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, "feedback-test-key-0001", presentFingerprint, presentSubmission)
	if err != ErrFeedbackIdempotencyConflict || replay || !reflect.DeepEqual(conflict, domain.ReviewFeedback{}) {
		t.Fatalf("conflict=%+v replay=%v err=%v", conflict, replay, err)
	}
	var count int
	if err := fixture.store.DB.QueryRow(`SELECT COUNT(*) FROM review_feedback`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("feedback count=%d err=%v", count, err)
	}
}

func TestReviewFeedbackConcurrentIdenticalRequestsMakeOneImmutableGraph(t *testing.T) {
	fixture := newReviewRegressionFixture(t)
	defer fixture.close()
	reviewFingerprint, err := ReviewFingerprint(fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err := fixture.store.SubmitAdvisoryReview(context.Background(), fixture.principal, fixture.portfolio, "feedback-concurrent-review-key", reviewFingerprint, fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	note := "concurrent"
	submission := domain.FeedbackSubmission{Decision: "defer", Note: &note}
	fingerprint, err := FeedbackFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	stores := make([]*Store, workers)
	for index := range stores {
		stores[index], err = Open(context.Background(), fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[index].Close()
	}
	start := make(chan struct{})
	results := make(chan struct {
		feedback domain.ReviewFeedback
		replay   bool
		err      error
	}, workers)
	var group sync.WaitGroup
	for _, database := range stores {
		group.Add(1)
		go func(database *Store) {
			defer group.Done()
			<-start
			feedback, replay, callErr := database.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, "feedback-concurrent-key", fingerprint, submission)
			results <- struct {
				feedback domain.ReviewFeedback
				replay   bool
				err      error
			}{feedback, replay, callErr}
		}(database)
	}
	close(start)
	group.Wait()
	close(results)
	created := 0
	var original domain.ReviewFeedback
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent feedback error: %v", result.err)
		}
		if !result.replay {
			created++
			original = result.feedback
		} else if !reflect.DeepEqual(original, domain.ReviewFeedback{}) && !reflect.DeepEqual(original, result.feedback) {
			t.Fatalf("replay changed response: original=%+v replay=%+v", original, result.feedback)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d want 1", created)
	}
	var count int
	if err := fixture.store.DB.QueryRow(`SELECT COUNT(*) FROM review_feedback WHERE review_id=?`, review.ReviewID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("feedback rows=%d err=%v", count, err)
	}
}

func TestReviewFeedbackAcceptsEveryDecision(t *testing.T) {
	fixture := newReviewRegressionFixture(t)
	defer fixture.close()
	reviewFingerprint, err := ReviewFingerprint(fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	review, _, err := fixture.store.SubmitAdvisoryReview(context.Background(), fixture.principal, fixture.portfolio, "feedback-decisions-review-key", reviewFingerprint, fixture.submission)
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"agree", "disagree", "defer", "request_another_review"} {
		submission := domain.FeedbackSubmission{Decision: decision}
		fingerprint, fingerprintErr := FeedbackFingerprint(submission)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		feedback, replay, callErr := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, "feedback-decision-"+decision, fingerprint, submission)
		if callErr != nil || replay || feedback.Decision != decision || feedback.Note != nil {
			t.Fatalf("decision=%s feedback=%+v replay=%v err=%v", decision, feedback, replay, callErr)
		}
	}
}

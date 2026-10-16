package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestFeedbackRetryConvergesAfterBusyAndHonorsCancellation(t *testing.T) {
	t.Run("converges", func(t *testing.T) {
		fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
		defer fixture.close()
		second, err := Open(context.Background(), fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		setReviewBusyTimeout(t, fixture.store)
		acquireReviewWriteLock(t, second)
		var attempts atomic.Int32
		restore := withReviewRetryHooks(func(attempt int) {
			attempts.Add(1)
			if attempt == 1 {
				releaseReviewWriteLock(t, second)
			}
		}, nil)
		defer restore()
		created, replay, callErr := fixture.store.SubmitReviewFeedback(context.Background(), "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
		if callErr != nil || replay || created.FeedbackID == "" {
			t.Fatalf("created=%+v replay=%v err=%v", created, replay, callErr)
		}
		if got := attempts.Load(); got != 2 {
			t.Fatalf("attempts=%d want 2", got)
		}
	})

	t.Run("cancellation-during-backoff", func(t *testing.T) {
		fixture, review, key, submission, fingerprint := createFeedbackFixture(t)
		defer fixture.close()
		second, err := Open(context.Background(), fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		setReviewBusyTimeout(t, fixture.store)
		acquireReviewWriteLock(t, second)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts atomic.Int32
		restore := withReviewRetryHooks(func(int) { attempts.Add(1) }, func(int) { cancel() })
		defer restore()
		_, replay, callErr := fixture.store.SubmitReviewFeedback(ctx, "local-owner", fixture.portfolio, review.ReviewID, key, fingerprint, submission)
		if replay || !errors.Is(callErr, context.Canceled) {
			t.Fatalf("replay=%v err=%v", replay, callErr)
		}
		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts=%d want 1", got)
		}
		if got := feedbackCounts(t, fixture.store); got["review_feedback"] != 0 || got["feedback_idempotency"] != 0 {
			t.Fatalf("cancellation wrote rows: %v", got)
		}
		releaseReviewWriteLock(t, second)
	})
}

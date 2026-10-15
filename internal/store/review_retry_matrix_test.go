package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func withReviewRetryHooks(attempt, backoff func(int)) func() {
	previousAttempt, previousBackoff := reviewAttemptHook, reviewBackoffHook
	reviewAttemptHook, reviewBackoffHook = attempt, backoff
	return func() { reviewAttemptHook, reviewBackoffHook = previousAttempt, previousBackoff }
}

func acquireReviewWriteLock(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.DB.Exec(`PRAGMA busy_timeout=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
}

func releaseReviewWriteLock(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.DB.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
}

func setReviewBusyTimeout(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.DB.Exec(`PRAGMA busy_timeout=1`); err != nil {
		t.Fatal(err)
	}
}

func TestReviewRetryConvergesAfterRealSQLiteBusyWithExactAttempts(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	setReviewBusyTimeout(t, f.store)
	acquireReviewWriteLock(t, second)
	var attempts atomic.Int32
	restore := withReviewRetryHooks(func(attempt int) {
		attempts.Add(1)
		if attempt == 1 {
			releaseReviewWriteLock(t, second)
		}
	}, nil)
	defer restore()
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	result, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "real-busy-retry-key", fingerprint, f.submission)
	if err != nil || replay || result.ReviewID == "" {
		t.Fatalf("result=%+v replay=%v err=%v", result, replay, err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts=%d want 2", got)
	}
}

func TestReviewRetryCancellationDuringBackoffPerformsNoLaterAttempt(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	setReviewBusyTimeout(t, f.store)
	acquireReviewWriteLock(t, second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	restore := withReviewRetryHooks(func(int) { attempts.Add(1) }, func(int) { cancel() })
	defer restore()
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(ctx, f.principal, f.portfolio, "cancel-backoff-key", fingerprint, f.submission)
	if replay || !errors.Is(err, context.Canceled) {
		t.Fatalf("replay=%v err=%v", replay, err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts=%d want 1", got)
	}
	if counts := reviewTableCounts(t, f.store); counts["advisory_reviews"] != 0 || counts["review_idempotency"] != 0 {
		t.Fatalf("cancellation wrote rows: %v", counts)
	}
	releaseReviewWriteLock(t, second)
}

func TestReviewRetryCancellationBetweenAttemptsPerformsNoLaterWrite(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	setReviewBusyTimeout(t, f.store)
	acquireReviewWriteLock(t, second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	restore := withReviewRetryHooks(func(attempt int) {
		attempts.Add(1)
		if attempt == 1 {
			cancel()
			releaseReviewWriteLock(t, second)
		}
	}, nil)
	defer restore()
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(ctx, f.principal, f.portfolio, "cancel-between-attempts-key", fingerprint, f.submission)
	if replay || !errors.Is(err, context.Canceled) {
		t.Fatalf("replay=%v err=%v", replay, err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts=%d want 2", got)
	}
	if counts := reviewTableCounts(t, f.store); counts["advisory_reviews"] != 0 || counts["review_idempotency"] != 0 {
		t.Fatalf("cancellation wrote rows: %v", counts)
	}
}

func TestReviewRetryNonTransientClassesUseOneAttempt(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = submitRegressionReview(t, f, "nontransient-existing-key", f.submission)
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"idempotency-conflict", func(ctx context.Context) error {
			different := f.submission
			different.Rationale = "different"
			fp, fpErr := ReviewFingerprint(different)
			if fpErr != nil {
				return fpErr
			}
			_, _, callErr := f.store.SubmitAdvisoryReview(ctx, f.principal, f.portfolio, "nontransient-existing-key", fp, different)
			return callErr
		}},
		{"auth", func(ctx context.Context) error {
			denied := f.principal
			denied.ReviewsWrite = false
			_, _, callErr := f.store.SubmitAdvisoryReview(ctx, denied, f.portfolio, "nontransient-auth-key", fingerprint, f.submission)
			return callErr
		}},
		{"validation", func(ctx context.Context) error {
			invalid := f.submission
			invalid.Rationale = ""
			_, _, callErr := f.store.SubmitAdvisoryReview(ctx, f.principal, f.portfolio, "nontransient-validation-key", fingerprint, invalid)
			return callErr
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			restore := withReviewRetryHooks(func(int) { attempts.Add(1) }, nil)
			defer restore()
			callErr := tc.call(context.Background())
			if callErr == nil {
				t.Fatal("non-transient operation unexpectedly succeeded")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("attempts=%d want 1 err=%v", got, callErr)
			}
		})
	}
}

func TestReviewRetryStructuredBusyLockDoesNotExceedFourAttempts(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	setReviewBusyTimeout(t, f.store)
	acquireReviewWriteLock(t, second)
	var attempts atomic.Int32
	restore := withReviewRetryHooks(func(int) { attempts.Add(1) }, nil)
	defer restore()
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, submitErr := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "busy-bound-attempts-key", fingerprint, f.submission)
	if replay || submitErr == nil {
		t.Fatalf("replay=%v err=%v", replay, submitErr)
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts=%d want 4", got)
	}
	releaseReviewWriteLock(t, second)
}

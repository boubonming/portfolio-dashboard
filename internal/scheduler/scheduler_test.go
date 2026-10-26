package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func testSchedule(t *testing.T, expression string) config.MarketRefreshConfig {
	t.Helper()
	value, err := config.ParseMarketRefreshConfig(expression, "Asia/Singapore", "portfolio-default", "")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSchedulerDisabledDoesNotStartOrRun(t *testing.T) {
	var calls atomic.Int32
	s := NewWithJob(testSchedule(t, ""), func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error) {
		calls.Add(1)
		return domain.MarketRefreshResult{}, false, nil
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSlot(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled scheduler called job %d times", calls.Load())
	}
}

func TestSchedulerHasNoStartupRunAndStableSlotKeys(t *testing.T) {
	var calls atomic.Int32
	s := NewWithJob(testSchedule(t, "0 5 * * 1-5"), func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error) {
		calls.Add(1)
		return domain.MarketRefreshResult{}, false, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("scheduler ran during startup")
	}
	fireAt := time.Date(2026, time.January, 5, 5, 0, 37, 0, time.FixedZone("SGT", 8*60*60))
	if ScheduledIdempotencyKey("portfolio-default", fireAt) != ScheduledIdempotencyKey("portfolio-default", fireAt.Add(20*time.Second)) {
		t.Fatal("slot key changed within a cron minute")
	}
	if ScheduledIdempotencyKey("portfolio-default", fireAt) == ScheduledIdempotencyKey("portfolio-other", fireAt) {
		t.Fatal("portfolio slot keys collided")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerSkipsOverlapAndStopsOnCancellation(t *testing.T) {
	configuration := testSchedule(t, "0 5 * * 1-5")
	started := make(chan struct{})
	release := make(chan struct{})
	s := NewWithJob(configuration, func(ctx context.Context, _ time.Time) (domain.MarketRefreshResult, bool, error) {
		close(started)
		select {
		case <-release:
			return domain.MarketRefreshResult{}, false, nil
		case <-ctx.Done():
			return domain.MarketRefreshResult{}, false, ctx.Err()
		}
	})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := s.RunSlot(context.Background(), time.Now())
		firstDone <- err
	}()
	<-started
	if _, _, err := s.RunSlot(context.Background(), time.Now()); !errors.Is(err, ErrOverlapSkipped) {
		t.Fatalf("overlap error=%v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.RunSlot(cancelCtx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run error=%v", err)
	}
}

func TestSchedulerReconfigureRestoresOldScheduleWhenCandidateLifecycleIsCanceled(t *testing.T) {
	configuration := testSchedule(t, "0 5 * * 1-5")
	var calls atomic.Int32
	s := NewWithJob(configuration, func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error) {
		calls.Add(1)
		return domain.MarketRefreshResult{Complete: true}, true, nil
	})
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	if err := s.Start(lifecycle); err != nil {
		t.Fatal(err)
	}
	candidateLifecycle, cancelCandidate := context.WithCancel(context.Background())
	cancelCandidate()
	err := s.ReconfigureWithLifecycle(context.Background(), testSchedule(t, "30 6 * * *"), candidateLifecycle)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled candidate error=%v", err)
	}
	if got, degraded, statusErr := s.RuntimeStatus(); degraded || statusErr != nil || got.Cron != configuration.Cron {
		t.Fatalf("old runtime was not restored: config=%+v degraded=%v err=%v", got, degraded, statusErr)
	}
	s.mu.Lock()
	restored := s.cron != nil
	s.mu.Unlock()
	if !restored {
		t.Fatal("old cron schedule was not restored")
	}
	if _, _, err := s.RunSlot(context.Background(), time.Now()); err != nil {
		t.Fatalf("restored schedule not runnable: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("restored schedule calls=%d", calls.Load())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerRecoveryFailureIsDegradedAndReconciles(t *testing.T) {
	configuration := testSchedule(t, "0 5 * * 1-5")
	s := NewWithJob(configuration, func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error) {
		return domain.MarketRefreshResult{Complete: true}, true, nil
	})
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	if err := s.Start(lifecycle); err != nil {
		t.Fatal(err)
	}
	cancelLifecycle()
	candidateLifecycle, cancelCandidate := context.WithCancel(context.Background())
	cancelCandidate()
	if err := s.ReconfigureWithLifecycle(context.Background(), testSchedule(t, "30 6 * * *"), candidateLifecycle); err == nil {
		t.Fatal("expected candidate failure")
	}
	if _, degraded, _ := s.RuntimeStatus(); !degraded {
		t.Fatal("scheduler did not enter degraded state after recovery failure")
	}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if _, degraded, statusErr := s.RuntimeStatus(); degraded || statusErr != nil {
		t.Fatalf("reconcile did not clear degraded state: degraded=%v err=%v", degraded, statusErr)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerReconfigureChangesActualCronEntryWithoutBackfill(t *testing.T) {
	var calls atomic.Int32
	s := NewWithJob(testSchedule(t, "0 5 * * *"), func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error) {
		calls.Add(1)
		return domain.MarketRefreshResult{Complete: true}, true, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entries := s.cron.Entries()
	s.mu.Unlock()
	if len(entries) != 1 {
		t.Fatalf("initial cron entries=%d", len(entries))
	}
	when := time.Date(2026, time.January, 1, 4, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	oldNext := entries[0].Schedule.Next(when)
	if err := s.Reconfigure(ctx, testSchedule(t, "30 6 * * *")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entries = s.cron.Entries()
	s.mu.Unlock()
	if len(entries) != 1 {
		t.Fatalf("rescheduled cron entries=%d", len(entries))
	}
	newNext := entries[0].Schedule.Next(when)
	if oldNext == newNext || newNext.Hour() != 6 || newNext.Minute() != 30 {
		t.Fatalf("cron entry was not rescheduled: old=%s new=%s", oldNext, newNext)
	}
	if calls.Load() != 0 {
		t.Fatalf("reconfigure backfilled a run: calls=%d", calls.Load())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

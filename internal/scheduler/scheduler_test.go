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

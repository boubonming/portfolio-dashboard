package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketrefresh"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"github.com/robfig/cron/v3"
)

var ErrOverlapSkipped = errors.New("scheduled market refresh skipped because another run is active")

const (
	schedulerActorCategory = "scheduler"
	schedulerActorID       = "market-refresh-scheduler"
)

type jobFunc func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error)

// Scheduler owns only the in-process trigger. Durable idempotency and lease
// recovery remain in the shared market refresh service and SQLite store.
type Scheduler struct {
	configuration config.MarketRefreshConfig
	job           jobFunc
	now           func() time.Time

	mu            sync.Mutex
	running       bool
	runningDone   chan struct{}
	cron          *cron.Cron
	cancel        context.CancelFunc
	reconfigureMu sync.Mutex
}

func New(service *marketrefresh.Service, configuration config.MarketRefreshConfig) *Scheduler {
	scheduler := NewWithJob(configuration, nil)
	scheduler.job = func(ctx context.Context, fireAt time.Time) (domain.MarketRefreshResult, bool, error) {
		scheduler.mu.Lock()
		current := scheduler.configuration
		scheduler.mu.Unlock()
		return service.Refresh(ctx, marketrefresh.Request{
			PortfolioID:       current.PortfolioID,
			ReportingCurrency: current.ReportingCurrency,
			IdempotencyKey:    ScheduledIdempotencyKey(current.PortfolioID, fireAt),
			Actor:             schedulerActor(),
			AsOf:              fireAt.UTC(),
		})
	}
	return scheduler
}

func NewWithJob(configuration config.MarketRefreshConfig, job func(context.Context, time.Time) (domain.MarketRefreshResult, bool, error)) *Scheduler {
	return &Scheduler{configuration: configuration, job: job, now: time.Now}
}

func (s *Scheduler) SetClock(now func() time.Time) {
	if now == nil {
		s.now = time.Now
		return
	}
	s.now = now
}

func schedulerActor() store.MutationActor {
	return store.MutationActor{Category: schedulerActorCategory, ID: schedulerActorID, Permissions: []string{"market_data:refresh"}}
}

// ScheduledIdempotencyKey is stable for one configured portfolio and one
// cron fire instant, while remaining safe for the bounded receipt key column.
func ScheduledIdempotencyKey(portfolioID string, fireAt time.Time) string {
	slot := fireAt.UTC().Truncate(time.Minute)
	witness := sha256.Sum256([]byte(portfolioID + "\x00" + slot.Format(time.RFC3339Nano)))
	return "scheduled-" + hex.EncodeToString(witness[:])
}

func SchedulerActor() (category, id string) { return schedulerActorCategory, schedulerActorID }

func (s *Scheduler) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configuration.Enabled()
}

// Start never invokes the job immediately. A disabled schedule creates no
// cron object and no goroutine.
func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil || !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	configuration := s.configuration
	s.mu.Unlock()
	if configuration.Location == nil {
		return errors.New("market refresh scheduler timezone is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		return errors.New("market refresh scheduler is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c := cron.New(
		cron.WithLocation(configuration.Location),
		cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)),
	)
	if _, err := c.AddFunc(configuration.Cron, func() {
		fireAt := s.now().In(configuration.Location).Truncate(time.Minute)
		result, _, err := s.RunSlot(runCtx, fireAt)
		if (err != nil && !errors.Is(err, ErrOverlapSkipped) && !errors.Is(err, context.Canceled)) || (err == nil && !result.Complete) {
			// Do not print provider or database error text: it may contain
			// untrusted response details or deployment-specific secrets.
			log.Printf("scheduled market refresh failed or incomplete for slot %s", fireAt.UTC().Format(time.RFC3339))
		}
	}); err != nil {
		cancel()
		return err
	}
	s.cron, s.cancel = c, cancel
	c.Start()
	return nil
}

// Stop cancels provider work and waits for an active cron invocation to exit.
func (s *Scheduler) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	c, cancel, runningDone := s.cron, s.cancel, s.runningDone
	s.cron, s.cancel = nil, nil
	s.mu.Unlock()
	if c == nil {
		if runningDone == nil {
			return nil
		}
	}
	if cancel != nil {
		cancel()
	}
	if c == nil {
		select {
		case <-runningDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var stopped <-chan struct{}
	stopped = c.Stop().Done()
	select {
	case <-stopped:
		if runningDone != nil {
			select {
			case <-runningDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reconfigure applies a new setting without an immediate run. It serializes
// stop/start so an active invocation is fully quiescent before a replacement
// schedule is installed.
func (s *Scheduler) Reconfigure(ctx context.Context, configuration config.MarketRefreshConfig) error {
	if s == nil {
		return errors.New("market refresh scheduler is nil")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.Stop(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.configuration = configuration
	s.mu.Unlock()
	return s.Start(ctx)
}

// RunSlot is an injectable boundary for deterministic tests and operational
// retries. fireAt is normalized to the configured cron minute.
func (s *Scheduler) RunSlot(ctx context.Context, fireAt time.Time) (domain.MarketRefreshResult, bool, error) {
	if s == nil || !s.Enabled() {
		return domain.MarketRefreshResult{}, false, nil
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return domain.MarketRefreshResult{}, false, ErrOverlapSkipped
	}
	s.running = true
	s.runningDone = make(chan struct{})
	runningDone := s.runningDone
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		close(runningDone)
		s.runningDone = nil
		s.mu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	if s.job == nil {
		return domain.MarketRefreshResult{}, false, errors.New("market refresh scheduler job is not configured")
	}
	s.mu.Lock()
	location := s.configuration.Location
	s.mu.Unlock()
	return s.job(ctx, fireAt.In(location).Truncate(time.Minute))
}

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
var ErrRuntimeDegraded = errors.New("market refresh scheduler runtime is degraded")

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
	lifecycle     context.Context
	degraded      bool
	degradedErr   error

	mu            sync.Mutex
	running       bool
	runningDone   chan struct{}
	reconfiguring bool
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
	if s == nil {
		return nil
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.mu.Lock()
	configuration := s.configuration
	if configuration.Enabled() && s.cron != nil {
		s.mu.Unlock()
		return errors.New("market refresh scheduler is already running")
	}
	s.mu.Unlock()
	prepared, err := s.prepare(configuration, ctx)
	if err != nil {
		return err
	}
	if err := s.install(prepared, configuration); err != nil {
		prepared.cancel()
		return err
	}
	return nil
}

// Stop cancels provider work and waits for an active cron invocation to exit.
func (s *Scheduler) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.mu.Lock()
	c, cancel, runningDone := s.cron, s.cancel, s.runningDone
	s.cron, s.cancel, s.lifecycle = nil, nil, nil
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
	return s.reconfigure(ctx, configuration, ctx)
}

// ReconfigureWithLifecycle uses ctx for quiescing and lifecycle for the
// context of a successfully installed schedule. HTTP callers therefore cannot
// accidentally turn a request context into the lifetime of a cron job.
func (s *Scheduler) ReconfigureWithLifecycle(ctx context.Context, configuration config.MarketRefreshConfig, lifecycle context.Context) error {
	return s.reconfigure(ctx, configuration, lifecycle)
}

func (s *Scheduler) reconfigure(ctx context.Context, configuration config.MarketRefreshConfig, lifecycle context.Context) error {
	if s == nil {
		return errors.New("market refresh scheduler is nil")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	prepared, err := s.prepare(configuration, lifecycle)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.reconfiguring = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.reconfiguring = false
		s.mu.Unlock()
	}()
	oldConfiguration, oldLifecycle := s.snapshot()
	if err := s.quiesce(ctx); err != nil {
		// quiesce cancels the old cron before waiting. Detach that stopped
		// handle before recovery so a timeout cannot leave a dead schedule
		// masquerading as installed state.
		s.detach()
		prepared.cancel()
		return s.restoreAfterFailure(oldConfiguration, oldLifecycle, err)
	}
	s.detach()
	if err := ctx.Err(); err != nil {
		prepared.cancel()
		return s.restoreAfterFailure(oldConfiguration, oldLifecycle, err)
	}
	if err := s.install(prepared, configuration); err != nil {
		prepared.cancel()
		return s.restoreAfterFailure(oldConfiguration, oldLifecycle, err)
	}
	return nil
}

type preparedSchedule struct {
	cron      *cron.Cron
	cancel    context.CancelFunc
	lifecycle context.Context
}

func (s *Scheduler) prepare(configuration config.MarketRefreshConfig, lifecycle context.Context) (*preparedSchedule, error) {
	if !configuration.Enabled() {
		return &preparedSchedule{}, nil
	}
	if configuration.Location == nil {
		return nil, errors.New("market refresh scheduler timezone is not initialized")
	}
	runCtx, cancel := context.WithCancel(lifecycle)
	c := cron.New(
		cron.WithLocation(configuration.Location),
		cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)),
	)
	if _, err := c.AddFunc(configuration.Cron, func() {
		fireAt := s.now().In(configuration.Location).Truncate(time.Minute)
		result, _, err := s.RunSlot(runCtx, fireAt)
		if (err != nil && !errors.Is(err, ErrOverlapSkipped) && !errors.Is(err, context.Canceled)) || (err == nil && !result.Complete) {
			log.Printf("scheduled market refresh failed or incomplete for slot %s", fireAt.UTC().Format(time.RFC3339))
		}
	}); err != nil {
		cancel()
		return nil, err
	}
	return &preparedSchedule{cron: c, cancel: cancel, lifecycle: lifecycle}, nil
}

func (s *Scheduler) install(prepared *preparedSchedule, configuration config.MarketRefreshConfig) error {
	if prepared == nil {
		return errors.New("market refresh scheduler candidate is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		return errors.New("market refresh scheduler is already running")
	}
	if prepared.cron != nil {
		if err := s.lifecycleError(prepared); err != nil {
			return err
		}
		s.cron, s.cancel = prepared.cron, prepared.cancel
		s.lifecycle = prepared.lifecycle
		prepared.cron.Start()
	}
	s.configuration = configuration
	s.degraded = false
	s.degradedErr = nil
	return nil
}

func (s *Scheduler) lifecycleError(prepared *preparedSchedule) error {
	// The prepared callback already carries the lifecycle context. Checking it
	// here makes cancellation between quiescing and installation fail closed.
	if prepared.lifecycle == nil {
		return nil
	}
	return prepared.lifecycle.Err()
}

func (s *Scheduler) snapshot() (config.MarketRefreshConfig, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycle == nil {
		return s.configuration, context.Background()
	}
	return s.configuration, s.lifecycle
}

func (s *Scheduler) quiesce(ctx context.Context) error {
	s.mu.Lock()
	c, cancel, runningDone := s.cron, s.cancel, s.runningDone
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		select {
		case <-c.Stop().Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if runningDone != nil {
		select {
		case <-runningDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Scheduler) detach() {
	s.mu.Lock()
	s.cron, s.cancel, s.lifecycle = nil, nil, nil
	s.mu.Unlock()
}

func (s *Scheduler) restoreAfterFailure(configuration config.MarketRefreshConfig, lifecycle context.Context, cause error) error {
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := recoveryCtx.Err(); err != nil {
		return errors.Join(ErrRuntimeDegraded, cause, err)
	}
	prepared, err := s.prepare(configuration, lifecycle)
	if err == nil {
		err = s.install(prepared, configuration)
	}
	if err == nil {
		return cause
	}
	if prepared != nil && prepared.cancel != nil {
		prepared.cancel()
	}
	s.mu.Lock()
	s.configuration = configuration
	s.degraded = true
	s.degradedErr = errors.Join(ErrRuntimeDegraded, cause, err)
	s.mu.Unlock()
	return errors.Join(ErrRuntimeDegraded, cause, err)
}

// RuntimeStatus reports whether this portfolio is safe to claim as effective.
func (s *Scheduler) RuntimeStatus() (configuration config.MarketRefreshConfig, degraded bool, err error) {
	if s == nil {
		return config.MarketRefreshConfig{}, true, errors.New("market refresh scheduler is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configuration, s.degraded, s.degradedErr
}

// Reconcile retries restoration of the last known valid configuration after a
// recovery failure. It never installs the failed candidate.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	if s == nil {
		return errors.New("market refresh scheduler is nil")
	}
	s.mu.Lock()
	configuration, lifecycle := s.configuration, s.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	s.mu.Unlock()
	if err := s.ReconfigureWithLifecycle(ctx, configuration, lifecycle); err != nil {
		return err
	}
	return nil
}

// RunSlot is an injectable boundary for deterministic tests and operational
// retries. fireAt is normalized to the configured cron minute.
func (s *Scheduler) RunSlot(ctx context.Context, fireAt time.Time) (domain.MarketRefreshResult, bool, error) {
	if s == nil || !s.Enabled() {
		return domain.MarketRefreshResult{}, false, nil
	}
	s.mu.Lock()
	if s.reconfiguring || s.running {
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

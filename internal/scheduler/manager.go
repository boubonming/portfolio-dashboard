package scheduler

import (
	"context"
	"errors"
	"sync"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/marketrefresh"
)

// Manager owns one scheduler per portfolio. Reconfiguration is serialized so
// each portfolio's old schedule is quiescent before its replacement starts.
type Manager struct {
	service    *marketrefresh.Service
	mu         sync.Mutex
	schedulers map[string]*Scheduler
	root       context.Context
}

func NewManager(service *marketrefresh.Service) *Manager {
	return &Manager{service: service, schedulers: make(map[string]*Scheduler)}
}

func (m *Manager) Start(ctx context.Context, configuration config.MarketRefreshConfig) error {
	m.mu.Lock()
	m.root = ctx
	m.mu.Unlock()
	return m.Reconfigure(ctx, configuration)
}

func (m *Manager) Reconfigure(ctx context.Context, configuration config.MarketRefreshConfig) error {
	if m == nil || m.service == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lifecycle := m.root
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	current := m.schedulers[configuration.PortfolioID]
	if current == nil {
		current = New(m.service, configuration)
		if err := current.Start(lifecycle); err != nil {
			return err
		}
		m.schedulers[configuration.PortfolioID] = current
		return nil
	}
	return current.ReconfigureWithLifecycle(ctx, configuration, lifecycle)
}

// RuntimeStatus is deliberately small: callers may only claim a schedule is
// effective when Degraded is false. Error is operational state for logs and
// reconciliation, not a value to serialize to browsers.
func (m *Manager) RuntimeStatus(portfolioID string) (configuration config.MarketRefreshConfig, degraded bool, err error) {
	if m == nil {
		return config.MarketRefreshConfig{}, true, errors.New("market refresh scheduler manager is nil")
	}
	m.mu.Lock()
	current := m.schedulers[portfolioID]
	m.mu.Unlock()
	if current == nil {
		return config.MarketRefreshConfig{}, true, errors.New("market refresh scheduler is not configured")
	}
	return current.RuntimeStatus()
}

func (m *Manager) ScheduleRuntimeDegraded(portfolioID string) bool {
	_, degraded, _ := m.RuntimeStatus(portfolioID)
	return degraded
}

// Reconcile restores the last known valid schedule after a failed recovery.
func (m *Manager) Reconcile(ctx context.Context, portfolioID string) error {
	if m == nil {
		return errors.New("market refresh scheduler manager is nil")
	}
	m.mu.Lock()
	current := m.schedulers[portfolioID]
	m.mu.Unlock()
	if current == nil {
		return errors.New("market refresh scheduler is not configured")
	}
	return current.Reconcile(ctx)
}

func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for _, current := range m.schedulers {
		if err := current.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

package scheduler

import (
	"context"
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.root != nil {
		ctx = m.root
	}
	current := m.schedulers[configuration.PortfolioID]
	if current == nil {
		current = New(m.service, configuration)
		m.schedulers[configuration.PortfolioID] = current
		return current.Start(ctx)
	}
	return current.Reconfigure(ctx, configuration)
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

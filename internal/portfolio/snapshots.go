// Package portfolio coordinates immutable snapshot creation without exposing
// portfolio mutation over HTTP.
package portfolio

import (
	"context"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

type SnapshotService struct{ Store *store.Store }

func (s SnapshotService) CreateSnapshot(ctx context.Context, request store.SnapshotRequest) (domain.SnapshotStatus, error) {
	return s.Store.CreateSnapshot(ctx, request)
}
func (s SnapshotService) Status(ctx context.Context, snapshotID string) (domain.SnapshotStatus, error) {
	return s.Store.SnapshotStatus(ctx, snapshotID)
}
func (s SnapshotService) Show(ctx context.Context, snapshotID string, includeLots bool) (any, error) {
	return s.Store.SnapshotShow(ctx, snapshotID, includeLots)
}

// Request is a CLI/API-friendly alias retaining explicit UTC and freshness
// controls at the service boundary.
type Request struct {
	PortfolioID, ReportingCurrency, CalculationVersion string
	AsOf                                               time.Time
	MaxAge                                             time.Duration
}

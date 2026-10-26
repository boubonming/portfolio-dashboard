package marketrefresh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Request is the transport-independent input to the canonical market refresh
// workflow. Authentication and HTTP parsing stay outside this package.
type Request struct {
	PortfolioID       string
	ReportingCurrency string
	IdempotencyKey    string
	Actor             store.MutationActor
	AsOf              time.Time
	Providers         []marketdata.Provider
	// FingerprintScope lets non-HTTP callers include transport-specific
	// options without changing the legacy browser/Hermes fingerprint.
	FingerprintScope string
}

type Service struct {
	Database  *store.Store
	Clients   map[marketdata.Provider]marketdata.ProviderClient
	Readiness []domain.ProviderReadiness
}

func New(database *store.Store, clients map[marketdata.Provider]marketdata.ProviderClient, readiness []domain.ProviderReadiness) *Service {
	return &Service{Database: database, Clients: clients, Readiness: append([]domain.ProviderReadiness(nil), readiness...)}
}

// Refresh performs setup lookup, reporting-currency resolution, request
// fingerprinting, missing-mapping labeling, durable idempotency, provider
// refresh, and immutable snapshot creation for every caller.
func (s *Service) Refresh(ctx context.Context, request Request) (domain.MarketRefreshResult, bool, error) {
	if s == nil || s.Database == nil {
		return domain.MarketRefreshResult{}, false, errors.New("market refresh service is not configured")
	}
	portfolioID := strings.TrimSpace(request.PortfolioID)
	if portfolioID == "" {
		return domain.MarketRefreshResult{}, false, errors.New("portfolio is required")
	}
	setup, err := s.Database.MarketDataSetup(ctx, portfolioID, s.Readiness)
	if err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	reportingCurrency := strings.TrimSpace(request.ReportingCurrency)
	if reportingCurrency == "" {
		if err := s.Database.DB.QueryRowContext(ctx, "SELECT reporting_currency FROM portfolios WHERE id=?", portfolioID).Scan(&reportingCurrency); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
		reportingCurrency = strings.TrimSpace(reportingCurrency)
	}
	if !currencyPattern.MatchString(reportingCurrency) {
		return domain.MarketRefreshResult{}, false, errors.New("invalid reporting currency")
	}
	if request.IdempotencyKey == "" {
		return domain.MarketRefreshResult{}, false, errors.New("idempotency key is required")
	}
	fingerprintInput, _ := json.Marshal(struct {
		PortfolioID       string `json:"portfolio_id"`
		Operation         string `json:"operation"`
		ReportingCurrency string `json:"reporting_currency"`
	}{portfolioID, "refresh", reportingCurrency})
	if request.FingerprintScope != "" {
		fingerprintInput, _ = json.Marshal(struct {
			PortfolioID       string `json:"portfolio_id"`
			Operation         string `json:"operation"`
			ReportingCurrency string `json:"reporting_currency"`
			Scope             string `json:"scope"`
		}{portfolioID, "refresh", reportingCurrency, request.FingerprintScope})
	}
	fingerprintBytes := sha256.Sum256(fingerprintInput)
	fingerprint := hex.EncodeToString(fingerprintBytes[:])
	missing := make([]string, 0)
	for _, instrument := range setup.Instruments {
		if len(instrument.Mappings) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s)", instrument.Symbol, instrument.Currency))
		}
	}
	return s.Database.RefreshMarketDataIdempotent(ctx, store.MarketRefreshIdempotencyOptions{
		ActorCategory:      request.Actor.Category,
		ActorID:            request.Actor.ID,
		Permissions:        append([]string(nil), request.Actor.Permissions...),
		PortfolioID:        portfolioID,
		IdempotencyKey:     request.IdempotencyKey,
		RequestFingerprint: fingerprint,
		Refresh: store.RefreshOptions{
			PortfolioID:       portfolioID,
			ReportingCurrency: reportingCurrency,
			Providers:         append([]marketdata.Provider(nil), request.Providers...),
			Clients:           s.Clients,
			AsOf:              request.AsOf,
		},
		Snapshot: store.SnapshotRequest{
			PortfolioID:       portfolioID,
			ReportingCurrency: reportingCurrency,
			AsOf:              request.AsOf,
			MaxAge:            domain.DefaultFreshness,
		},
		Missing: missing,
	})
}

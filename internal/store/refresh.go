package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

type RefreshOptions struct {
	PortfolioID       string
	ReportingCurrency string
	Providers         []marketdata.Provider
	Clients           map[marketdata.Provider]marketdata.ProviderClient
	AsOf              time.Time
}

type refreshTarget struct {
	InstrumentID string
	Symbol       string
	Currency     string
}

type refreshMappingPlan struct {
	assignments map[marketdata.Provider]map[string]domain.ProviderMapping
	missing     map[marketdata.Provider]int
	lookupError map[marketdata.Provider]int
}

func (s *Store) Refresh(ctx context.Context, options RefreshOptions) (domain.RefreshResult, error) {
	if options.PortfolioID == "" {
		options.PortfolioID = defaultPortfolioID
	}
	if options.ReportingCurrency == "" {
		if err := s.DB.QueryRowContext(ctx, "SELECT reporting_currency FROM portfolios WHERE id = ?", options.PortfolioID).Scan(&options.ReportingCurrency); err != nil && err != sql.ErrNoRows {
			return domain.RefreshResult{}, err
		}
		if options.ReportingCurrency == "" {
			options.ReportingCurrency = "MYR"
		}
	}
	if options.AsOf.IsZero() {
		options.AsOf = time.Now().UTC()
	} else {
		options.AsOf = options.AsOf.UTC()
	}
	var portfolioExists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id = ?", options.PortfolioID).Scan(&portfolioExists); err != nil {
		return domain.RefreshResult{}, err
	}
	if portfolioExists == 0 {
		return domain.RefreshResult{}, fmt.Errorf("unknown portfolio %s", options.PortfolioID)
	}
	targets, err := s.refreshTargets(ctx, options.PortfolioID)
	if err != nil {
		return domain.RefreshResult{}, err
	}
	providers := options.Providers
	if len(providers) == 0 {
		providers = []marketdata.Provider{marketdata.ProviderFinnhub, marketdata.ProviderAlphaVantage, marketdata.ProviderCoinGecko, marketdata.ProviderOpenExchangeRates}
	}
	plan, err := s.planRefreshMappings(ctx, targets, providers, options.AsOf)
	if err != nil {
		return domain.RefreshResult{}, err
	}
	started := time.Now().UTC()
	result := domain.RefreshResult{RefreshID: deterministicID("refresh", options.PortfolioID, started.Format(time.RFC3339Nano)), PortfolioID: options.PortfolioID, Status: "complete", StartedAt: started.Format(time.RFC3339Nano)}
	for _, provider := range providers {
		status := s.refreshProvider(ctx, provider, options, targets, plan)
		result.Providers = append(result.Providers, status)
		result.ObservationIDs = append(result.ObservationIDs, status.ObservationIDs...)
		if status.Status != "complete" {
			result.Status = "incomplete"
		}
		if err := s.persistRefreshRun(ctx, result.RefreshID, options.PortfolioID, status, started); err != nil {
			return domain.RefreshResult{}, err
		}
	}
	result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, _ := json.Marshal(struct {
		RefreshID string                         `json:"refresh_id"`
		Status    string                         `json:"status"`
		Providers []domain.ProviderRefreshStatus `json:"providers"`
	}{result.RefreshID, result.Status, result.Providers})
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, deterministicID("audit", "refresh", result.RefreshID), "administrative-refresh", "market.refresh.completed", "refresh_run", result.RefreshID, payload, result.EndedAt); err != nil {
		return domain.RefreshResult{}, err
	}
	return result, nil
}

func (s *Store) refreshTargets(ctx context.Context, portfolioID string) ([]refreshTarget, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT l.instrument_id,i.symbol,l.currency FROM lots l JOIN instruments i ON i.id=l.instrument_id WHERE l.portfolio_id=? ORDER BY l.instrument_id`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []refreshTarget
	for rows.Next() {
		var target refreshTarget
		if err := rows.Scan(&target.InstrumentID, &target.Symbol, &target.Currency); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) activeMapping(ctx context.Context, target refreshTarget, provider marketdata.Provider, asOf time.Time) (domain.ProviderMapping, error) {
	var mapping domain.ProviderMapping
	err := s.DB.QueryRowContext(ctx, `SELECT id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at FROM instrument_provider_mappings WHERE instrument_id=? AND provider=? AND active_from<=? ORDER BY active_from DESC LIMIT 1`, target.InstrumentID, provider.String(), asOf.Format(time.RFC3339Nano)).Scan(&mapping.ID, &mapping.InstrumentID, &mapping.Provider, &mapping.ProviderSymbol, &mapping.QuoteCurrency, &mapping.ActiveFrom, &mapping.Provenance, &mapping.CreatedAt)
	return mapping, err
}

func (s *Store) planRefreshMappings(ctx context.Context, targets []refreshTarget, providers []marketdata.Provider, asOf time.Time) (refreshMappingPlan, error) {
	plan := refreshMappingPlan{
		assignments: map[marketdata.Provider]map[string]domain.ProviderMapping{},
		missing:     map[marketdata.Provider]int{},
		lookupError: map[marketdata.Provider]int{},
	}
	quoteProviders := make([]marketdata.Provider, 0, len(providers))
	seen := map[marketdata.Provider]bool{}
	for _, provider := range providers {
		if provider == marketdata.ProviderOpenExchangeRates || seen[provider] {
			continue
		}
		seen[provider] = true
		quoteProviders = append(quoteProviders, provider)
		plan.assignments[provider] = map[string]domain.ProviderMapping{}
	}
	if len(quoteProviders) == 0 {
		return plan, nil
	}
	for _, target := range targets {
		assigned := false
		for _, provider := range quoteProviders {
			mapping, err := s.activeMapping(ctx, target, provider, asOf)
			if err == nil {
				plan.assignments[provider][target.InstrumentID] = mapping
				assigned = true
				break
			}
			if !errors.Is(err, sql.ErrNoRows) {
				plan.lookupError[provider]++
			}
		}
		if !assigned {
			// A missing instrument belongs to one status only, rather than being
			// counted once for every selected provider.
			plan.missing[quoteProviders[0]]++
		}
	}
	return plan, nil
}

func (s *Store) refreshProvider(ctx context.Context, provider marketdata.Provider, options RefreshOptions, targets []refreshTarget, plan refreshMappingPlan) domain.ProviderRefreshStatus {
	status := domain.ProviderRefreshStatus{Provider: provider.String(), Status: "complete"}
	status.MissingMapping = plan.missing[provider]
	if lookupErrors := plan.lookupError[provider]; lookupErrors > 0 {
		status.FailedCount += lookupErrors
		status.Status = "incomplete"
		status.Errors = append(status.Errors, "mapping_lookup")
	}
	client := options.Clients[provider]
	quoteRequests := []marketdata.QuoteRequest{}
	fxRequests := []marketdata.FXRequest{}
	for _, target := range targets {
		if provider == marketdata.ProviderOpenExchangeRates {
			if strings.EqualFold(target.Currency, options.ReportingCurrency) {
				continue
			}
			fxRequests = appendUniqueFX(fxRequests, marketdata.FXRequest{BaseCurrency: target.Currency, QuoteCurrency: options.ReportingCurrency, AsOf: options.AsOf})
			continue
		}
		mapping, assigned := plan.assignments[provider][target.InstrumentID]
		if !assigned {
			continue
		}
		if !strings.EqualFold(mapping.QuoteCurrency, target.Currency) {
			status.FailedCount++
			status.Status = "incomplete"
			status.Errors = append(status.Errors, "currency_mismatch")
			continue
		}
		quoteRequests = append(quoteRequests, marketdata.QuoteRequest{InstrumentID: target.InstrumentID, Symbol: target.Symbol, ProviderSymbol: mapping.ProviderSymbol, Currency: target.Currency, AsOf: options.AsOf})
	}
	if status.MissingMapping > 0 {
		status.Status = "incomplete"
	}
	if client == nil {
		if (provider == marketdata.ProviderOpenExchangeRates && len(fxRequests) == 0) || (provider != marketdata.ProviderOpenExchangeRates && len(quoteRequests) == 0) {
			return status
		}
		status.Status = "incomplete"
		status.FailedCount++
		status.Errors = append(status.Errors, "configuration")
		return status
	}
	if provider == marketdata.ProviderOpenExchangeRates {
		if len(fxRequests) == 0 {
			return status
		}
		if batch, ok := client.(marketdata.BatchFXClient); ok {
			status.RequestCount = 1
			rates, failures := batch.FXBatch(ctx, fxRequests)
			for _, request := range fxRequests {
				key := strings.ToUpper(request.BaseCurrency) + "/" + strings.ToUpper(request.QuoteCurrency)
				if failure, exists := failures[key]; exists {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, refreshErrorClass(failure))
					continue
				}
				rate, exists := rates[key]
				if !exists {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, "provider_error")
					continue
				}
				stored, storeErr := s.AddFXRate(ctx, FXInput{ID: rate.ID, BaseCurrency: rate.BaseCurrency, QuoteCurrency: rate.QuoteCurrency, Rate: rate.Rate, Source: rate.Source, MarketAt: rate.MarketAt, FetchedAt: rate.FetchedAt, Basis: rate.Basis, Provenance: rate.Provenance})
				if storeErr != nil {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, "persistence")
				} else {
					status.SucceededCount++
					status.ObservationIDs = append(status.ObservationIDs, stored.ID)
				}
			}
			return status
		}
		for _, request := range fxRequests {
			status.RequestCount++
			rate, callErr := client.FX(ctx, request)
			if callErr != nil {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, refreshErrorClass(callErr))
				continue
			}
			stored, storeErr := s.AddFXRate(ctx, FXInput{ID: rate.ID, BaseCurrency: rate.BaseCurrency, QuoteCurrency: rate.QuoteCurrency, Rate: rate.Rate, Source: rate.Source, MarketAt: rate.MarketAt, FetchedAt: rate.FetchedAt, Basis: rate.Basis, Provenance: rate.Provenance})
			if storeErr != nil {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, "persistence")
			} else {
				status.SucceededCount++
				status.ObservationIDs = append(status.ObservationIDs, stored.ID)
			}
		}
		return status
	}
	if len(quoteRequests) == 0 {
		return status
	}
	if batch, ok := client.(marketdata.BatchQuoteClient); ok {
		status.RequestCount = 1
		if counter, ok := client.(marketdata.BatchQuoteRequestCounter); ok {
			status.RequestCount = counter.QuoteBatchRequestCount(quoteRequests)
		}
		quotes, failures := batch.QuoteBatch(ctx, quoteRequests)
		for _, request := range quoteRequests {
			if failure, exists := failures[request.InstrumentID]; exists {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, refreshErrorClass(failure))
				continue
			}
			quote, exists := quotes[request.InstrumentID]
			if !exists {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, "provider_error")
				continue
			}
			stored, storeErr := s.AddQuote(ctx, QuoteInput{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Price: quote.Price, Source: quote.Source, MarketAt: quote.MarketAt, FetchedAt: quote.FetchedAt, Basis: quote.Basis, Provenance: quote.Provenance})
			if storeErr != nil {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, "persistence")
			} else {
				status.SucceededCount++
				status.ObservationIDs = append(status.ObservationIDs, stored.ID)
			}
		}
		return status
	}
	for _, request := range quoteRequests {
		status.RequestCount++
		quote, callErr := client.Quote(ctx, request)
		if callErr != nil {
			status.FailedCount++
			status.Status = "incomplete"
			status.Errors = append(status.Errors, refreshErrorClass(callErr))
			continue
		}
		stored, storeErr := s.AddQuote(ctx, QuoteInput{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Price: quote.Price, Source: quote.Source, MarketAt: quote.MarketAt, FetchedAt: quote.FetchedAt, Basis: quote.Basis, Provenance: quote.Provenance})
		if storeErr != nil {
			status.FailedCount++
			status.Status = "incomplete"
			status.Errors = append(status.Errors, "persistence")
		} else {
			status.SucceededCount++
			status.ObservationIDs = append(status.ObservationIDs, stored.ID)
		}
	}
	return status
}

func appendUniqueFX(requests []marketdata.FXRequest, candidate marketdata.FXRequest) []marketdata.FXRequest {
	key := strings.ToUpper(candidate.BaseCurrency) + "/" + strings.ToUpper(candidate.QuoteCurrency)
	for _, existing := range requests {
		if strings.ToUpper(existing.BaseCurrency)+"/"+strings.ToUpper(existing.QuoteCurrency) == key {
			return requests
		}
	}
	return append(requests, candidate)
}

func refreshErrorClass(err error) string {
	var providerErr *marketdata.ProviderError
	if errors.As(err, &providerErr) && providerErr.Classification != "" {
		return providerErr.Classification
	}
	return "provider_error"
}

func (s *Store) persistRefreshRun(ctx context.Context, refreshID, portfolioID string, status domain.ProviderRefreshStatus, started time.Time) error {
	ended := time.Now().UTC()
	classification := ""
	if len(status.Errors) > 0 {
		classification = status.Errors[0]
	}
	id := deterministicID("refresh-run", refreshID, status.Provider)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO refresh_runs(id,portfolio_id,provider,status,started_at,ended_at,request_count,succeeded_count,failed_count,missing_mapping_count,error_classification) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, portfolioID, status.Provider, status.Status, started.Format(time.RFC3339Nano), ended.Format(time.RFC3339Nano), status.RequestCount, status.SucceededCount, status.FailedCount, status.MissingMapping, classification)
	return err
}

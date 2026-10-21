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
	// receipt binds refresh persistence to the durable idempotency operation.
	// It is intentionally private: only the idempotent workflow can provide it.
	receipt   *marketRefreshReceiptBinding
	RefreshID string
}

type refreshTarget struct {
	InstrumentID string
	Symbol       string
	Currency     string
}

type refreshMappingPlan struct {
	assignments      map[marketdata.Provider]map[string]domain.ProviderMapping
	missing          map[marketdata.Provider]int
	lookupError      map[marketdata.Provider]int
	mappingAmbiguous map[marketdata.Provider]int
}

type refreshObservations struct {
	quotes []domain.Quote
	fx     []domain.FXRate
}

var errRefreshProviderContract = errors.New("provider response does not match request")

func markRefreshObservationConflict(result *domain.RefreshResult, provider, observationID string) {
	for index := range result.Providers {
		status := &result.Providers[index]
		if status.Provider != provider {
			continue
		}
		if status.SucceededCount > 0 {
			status.SucceededCount--
		}
		status.FailedCount++
		status.Status = "incomplete"
		status.Errors = append(status.Errors, "persistence")
		for idIndex, id := range status.ObservationIDs {
			if id == observationID {
				status.ObservationIDs = append(status.ObservationIDs[:idIndex], status.ObservationIDs[idIndex+1:]...)
				break
			}
		}
		break
	}
	for index, id := range result.ObservationIDs {
		if id == observationID {
			result.ObservationIDs = append(result.ObservationIDs[:index], result.ObservationIDs[index+1:]...)
			break
		}
	}
	for index, observation := range result.QuoteObservations {
		if observation.ID == observationID {
			result.QuoteObservations = append(result.QuoteObservations[:index], result.QuoteObservations[index+1:]...)
			break
		}
	}
	for index, observation := range result.FXObservations {
		if observation.ID == observationID {
			result.FXObservations = append(result.FXObservations[:index], result.FXObservations[index+1:]...)
			break
		}
	}
	result.Status = "incomplete"
}

func quoteObservationSeen(values []domain.Quote, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func fxObservationSeen(values []domain.FXRate, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
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
	refreshID := options.RefreshID
	if refreshID == "" {
		refreshID = deterministicID("refresh", options.PortfolioID, started.Format(time.RFC3339Nano))
	}
	result := domain.RefreshResult{RefreshID: refreshID, PortfolioID: options.PortfolioID, Status: "complete", StartedAt: started.Format(time.RFC3339Nano)}
	observations := refreshObservations{}
	for _, provider := range providers {
		status := s.refreshProvider(ctx, provider, options, targets, plan, &observations)
		result.Providers = append(result.Providers, status)
		result.ObservationIDs = append(result.ObservationIDs, status.ObservationIDs...)
		if status.Status != "complete" {
			result.Status = "incomplete"
		}
	}
	for _, quote := range observations.quotes {
		result.QuoteObservations = append(result.QuoteObservations, domain.QuoteObservation{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Source: quote.Source})
	}
	for _, fx := range observations.fx {
		result.FXObservations = append(result.FXObservations, domain.FXObservation{ID: fx.ID, BaseCurrency: fx.BaseCurrency, QuoteCurrency: fx.QuoteCurrency, Source: fx.Source})
	}
	result.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if hookErr := s.marketRefreshFailure("before_observation_commit"); hookErr != nil {
		return domain.RefreshResult{}, hookErr
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.RefreshResult{}, err
	}
	defer tx.Rollback()
	for _, quote := range observations.quotes {
		if err := s.persistQuoteTx(ctx, tx, quote, []string{options.PortfolioID}); err != nil {
			if errors.Is(err, ErrObservationIDConflict) {
				markRefreshObservationConflict(&result, quote.Source, quote.ID)
				continue
			}
			return domain.RefreshResult{}, err
		}
	}
	for _, fx := range observations.fx {
		if err := s.persistFXTx(ctx, tx, fx, []string{options.PortfolioID}); err != nil {
			if errors.Is(err, ErrObservationIDConflict) {
				markRefreshObservationConflict(&result, fx.Source, fx.ID)
				continue
			}
			return domain.RefreshResult{}, err
		}
	}
	payload, _ := json.Marshal(struct {
		RefreshID string                         `json:"refresh_id"`
		Status    string                         `json:"status"`
		Providers []domain.ProviderRefreshStatus `json:"providers"`
	}{result.RefreshID, result.Status, result.Providers})
	for _, status := range result.Providers {
		if err := s.persistRefreshRunTx(ctx, tx, result.RefreshID, options.PortfolioID, status, started); err != nil {
			return domain.RefreshResult{}, err
		}
	}
	auditID := deterministicID("audit", "refresh", result.RefreshID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, auditID, "administrative-refresh", "market.refresh.completed", "refresh_run", result.RefreshID, payload, result.EndedAt); err != nil {
		return domain.RefreshResult{}, err
	}
	if err := recordActivity(ctx, tx, options.PortfolioID, "administrative", "market.refresh.completed", "market", "refresh_run", result.RefreshID, "Market refresh completed", "A market-data refresh completed.", result.EndedAt, auditID); err != nil {
		return domain.RefreshResult{}, err
	}
	if options.receipt != nil {
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return domain.RefreshResult{}, marshalErr
		}
		if err := updateMarketRefreshObservedTx(ctx, tx, *options.receipt, result.RefreshID, encoded); err != nil {
			return domain.RefreshResult{}, err
		}
	}
	s.marketRefreshCommitFailure("before_observation_tx_commit")
	if err := tx.Commit(); err != nil {
		return domain.RefreshResult{}, err
	}
	if hookErr := s.marketRefreshFailure("after_observation_commit"); hookErr != nil {
		return domain.RefreshResult{}, hookErr
	}
	return result, nil
}

func (s *Store) refreshTargets(ctx context.Context, portfolioID string) ([]refreshTarget, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT l.instrument_id,i.symbol,l.currency FROM lots l JOIN instruments i ON i.id=l.instrument_id WHERE l.portfolio_id=? AND l.lifecycle='active' ORDER BY l.instrument_id`, portfolioID)
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
		assignments:      map[marketdata.Provider]map[string]domain.ProviderMapping{},
		missing:          map[marketdata.Provider]int{},
		lookupError:      map[marketdata.Provider]int{},
		mappingAmbiguous: map[marketdata.Provider]int{},
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
		var latest domain.ProviderMapping
		var latestProvider marketdata.Provider
		found := false
		ambiguous := false
		for _, provider := range quoteProviders {
			mapping, err := s.activeMapping(ctx, target, provider, asOf)
			if err == nil {
				if !found {
					latest = mapping
					latestProvider = provider
					found = true
					continue
				}
				latestActive, parseErr := time.Parse(time.RFC3339Nano, latest.ActiveFrom)
				mappingActive, mappingParseErr := time.Parse(time.RFC3339Nano, mapping.ActiveFrom)
				if parseErr != nil || mappingParseErr != nil {
					return refreshMappingPlan{}, fmt.Errorf("invalid active mapping timestamp for %s: %w", target.InstrumentID, errors.Join(parseErr, mappingParseErr))
				}
				switch {
				case mappingActive.After(latestActive):
					latest = mapping
					latestProvider = provider
					ambiguous = false
				case mappingActive.Equal(latestActive):
					ambiguous = true
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				plan.lookupError[provider]++
			}
		}
		if !found {
			// A missing instrument belongs to one status only, rather than being
			// counted once for every selected provider.
			plan.missing[quoteProviders[0]]++
		} else if ambiguous {
			// A cross-provider tie is an explicit mapping error. Do not assign
			// the instrument to whichever provider happens to be iterated first.
			plan.mappingAmbiguous[quoteProviders[0]]++
		} else {
			plan.assignments[latestProvider][target.InstrumentID] = latest
		}
	}
	return plan, nil
}

func (s *Store) refreshProvider(ctx context.Context, provider marketdata.Provider, options RefreshOptions, targets []refreshTarget, plan refreshMappingPlan, observations *refreshObservations) domain.ProviderRefreshStatus {
	status := domain.ProviderRefreshStatus{Provider: provider.String(), Status: "complete"}
	status.MissingMapping = plan.missing[provider]
	if ambiguousMappings := plan.mappingAmbiguous[provider]; ambiguousMappings > 0 {
		status.FailedCount += ambiguousMappings
		status.Status = "incomplete"
		status.Errors = append(status.Errors, "mapping_ambiguous")
	}
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
	if client.Name() != provider {
		requested := len(quoteRequests)
		if provider == marketdata.ProviderOpenExchangeRates {
			requested = len(fxRequests)
		}
		if requested > 0 {
			status.RequestCount = requested
			status.FailedCount += requested
			status.Status = "incomplete"
			status.Errors = append(status.Errors, "provider_error")
		}
		return status
	}
	if provider == marketdata.ProviderOpenExchangeRates {
		if len(fxRequests) == 0 {
			return status
		}
		if batch, ok := client.(marketdata.BatchFXClient); ok {
			status.RequestCount = 1
			rates, failures := batch.FXBatch(ctx, fxRequests)
			expectedFX := make(map[string]bool, len(fxRequests))
			for _, request := range fxRequests {
				expectedFX[strings.ToUpper(request.BaseCurrency)+"/"+strings.ToUpper(request.QuoteCurrency)] = true
			}
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
				if storeErr := validateRefreshFX(s, request, provider, rate); storeErr != nil {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, refreshValidationErrorClass(storeErr))
				} else {
					if fxObservationSeen(observations.fx, rate.ID) {
						status.FailedCount++
						status.Status = "incomplete"
						status.Errors = append(status.Errors, "duplicate_observation")
						continue
					}
					status.SucceededCount++
					status.ObservationIDs = append(status.ObservationIDs, rate.ID)
					status.FXObservations = append(status.FXObservations, domain.FXObservation{ID: rate.ID, BaseCurrency: rate.BaseCurrency, QuoteCurrency: rate.QuoteCurrency, Source: rate.Source})
					observations.fx = append(observations.fx, rate)
				}
			}
			for key := range rates {
				if !expectedFX[key] {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, "extra_observation")
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
			if storeErr := validateRefreshFX(s, request, provider, rate); storeErr != nil {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, refreshValidationErrorClass(storeErr))
			} else {
				if fxObservationSeen(observations.fx, rate.ID) {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, "duplicate_observation")
					continue
				}
				status.SucceededCount++
				status.ObservationIDs = append(status.ObservationIDs, rate.ID)
				status.FXObservations = append(status.FXObservations, domain.FXObservation{ID: rate.ID, BaseCurrency: rate.BaseCurrency, QuoteCurrency: rate.QuoteCurrency, Source: rate.Source})
				observations.fx = append(observations.fx, rate)
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
		expectedQuotes := make(map[string]bool, len(quoteRequests))
		for _, request := range quoteRequests {
			expectedQuotes[request.InstrumentID] = true
		}
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
			if storeErr := validateRefreshQuote(s, request, provider, quote); storeErr != nil {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, refreshValidationErrorClass(storeErr))
			} else {
				if quoteObservationSeen(observations.quotes, quote.ID) {
					status.FailedCount++
					status.Status = "incomplete"
					status.Errors = append(status.Errors, "duplicate_observation")
					continue
				}
				status.SucceededCount++
				status.ObservationIDs = append(status.ObservationIDs, quote.ID)
				status.QuoteObservations = append(status.QuoteObservations, domain.QuoteObservation{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Source: quote.Source})
				observations.quotes = append(observations.quotes, quote)
			}
		}
		for key := range quotes {
			if !expectedQuotes[key] {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, "extra_observation")
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
		if storeErr := validateRefreshQuote(s, request, provider, quote); storeErr != nil {
			status.FailedCount++
			status.Status = "incomplete"
			status.Errors = append(status.Errors, refreshValidationErrorClass(storeErr))
		} else {
			if quoteObservationSeen(observations.quotes, quote.ID) {
				status.FailedCount++
				status.Status = "incomplete"
				status.Errors = append(status.Errors, "duplicate_observation")
				continue
			}
			status.SucceededCount++
			status.ObservationIDs = append(status.ObservationIDs, quote.ID)
			status.QuoteObservations = append(status.QuoteObservations, domain.QuoteObservation{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Source: quote.Source})
			observations.quotes = append(observations.quotes, quote)
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

func validateRefreshQuote(s *Store, request marketdata.QuoteRequest, provider marketdata.Provider, quote domain.Quote) error {
	if quote.InstrumentID != request.InstrumentID || quote.Symbol != request.Symbol || !strings.EqualFold(quote.Currency, request.Currency) || quote.Source != provider.String() {
		return fmt.Errorf("%w: quote identity mismatch", errRefreshProviderContract)
	}
	price, err := domain.ParseDecimal(quote.Price)
	if err != nil || price.Sign() <= 0 || strings.TrimSpace(quote.ID) == "" || strings.TrimSpace(quote.InstrumentID) == "" {
		return errors.New("invalid provider quote")
	}
	if err := validateObservationTimes(s.now(), quote.MarketAt, quote.FetchedAt); err != nil {
		return err
	}
	marketAt, _ := domain.ValidateObservationTimestamp(quote.MarketAt)
	if !request.AsOf.IsZero() && marketAt.After(request.AsOf.UTC().Add(domain.DefaultFutureTolerance)) {
		return errors.New("market timestamp is after refresh as-of")
	}
	return nil
}

func validateRefreshFX(s *Store, request marketdata.FXRequest, provider marketdata.Provider, fx domain.FXRate) error {
	if !strings.EqualFold(fx.BaseCurrency, request.BaseCurrency) || !strings.EqualFold(fx.QuoteCurrency, request.QuoteCurrency) || fx.Source != provider.String() {
		return fmt.Errorf("%w: FX identity mismatch", errRefreshProviderContract)
	}
	rate, err := domain.ParseDecimal(fx.Rate)
	if err != nil || rate.Sign() <= 0 || strings.TrimSpace(fx.ID) == "" || fx.BaseCurrency == "" || fx.QuoteCurrency == "" {
		return errors.New("invalid provider FX rate")
	}
	if err := validateObservationTimes(s.now(), fx.MarketAt, fx.FetchedAt); err != nil {
		return err
	}
	marketAt, _ := domain.ValidateObservationTimestamp(fx.MarketAt)
	if !request.AsOf.IsZero() && marketAt.After(request.AsOf.UTC().Add(domain.DefaultFutureTolerance)) {
		return errors.New("market timestamp is after refresh as-of")
	}
	return nil
}

func refreshValidationErrorClass(err error) string {
	if errors.Is(err, errRefreshProviderContract) {
		return "provider_error"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "future") || strings.Contains(message, "timestamp") {
		return "invalid_timestamp"
	}
	if strings.Contains(message, "currency") {
		return "currency_mismatch"
	}
	if strings.Contains(message, "invalid provider") {
		return "invalid_value"
	}
	return "persistence"
}

func (s *Store) persistRefreshRunTx(ctx context.Context, tx *sql.Tx, refreshID, portfolioID string, status domain.ProviderRefreshStatus, started time.Time) error {
	ended := s.now()
	classification := ""
	if len(status.Errors) > 0 {
		classification = status.Errors[0]
	}
	id := deterministicID("refresh-run", refreshID, status.Provider)
	_, err := tx.ExecContext(ctx, `INSERT INTO refresh_runs(id,portfolio_id,provider,status,started_at,ended_at,request_count,succeeded_count,failed_count,missing_mapping_count,error_classification) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, portfolioID, status.Provider, status.Status, started.Format(time.RFC3339Nano), ended.Format(time.RFC3339Nano), status.RequestCount, status.SucceededCount, status.FailedCount, status.MissingMapping, classification)
	return err
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

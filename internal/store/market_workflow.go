package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

var marketCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
var marketSymbol = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)

func (s *Store) MarketDataSetup(ctx context.Context, portfolioID string, providers []domain.ProviderReadiness) (domain.MarketDataSetup, error) {
	portfolioID = strings.TrimSpace(portfolioID)
	if portfolioID == "" {
		return domain.MarketDataSetup{}, errors.New("portfolio is required")
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id=?", portfolioID).Scan(&exists); err != nil {
		return domain.MarketDataSetup{}, err
	}
	if exists == 0 {
		return domain.MarketDataSetup{}, ErrPortfolioNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT i.id,i.symbol,COALESCE(i.description,''),i.currency
		FROM lots l JOIN instruments i ON i.id=l.instrument_id WHERE l.portfolio_id=? AND l.lifecycle='active' ORDER BY i.symbol,i.currency,i.id`, portfolioID)
	if err != nil {
		return domain.MarketDataSetup{}, err
	}
	items := []domain.MarketInstrumentView{}
	for rows.Next() {
		item := domain.MarketInstrumentView{Mappings: make([]domain.MarketMappingView, 0)}
		if err := rows.Scan(&item.InstrumentID, &item.Symbol, &item.Description, &item.Currency); err != nil {
			rows.Close()
			return domain.MarketDataSetup{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.MarketDataSetup{}, err
	}
	rows.Close()
	setup := domain.MarketDataSetup{PortfolioID: portfolioID, Providers: providers, Instruments: []domain.MarketInstrumentView{}}
	for _, item := range items {
		mappingRows, queryErr := s.DB.QueryContext(ctx, `SELECT provider,provider_symbol,quote_currency,active_from,provenance
			FROM instrument_provider_mappings m WHERE m.instrument_id=? AND m.active_from<=?
			AND m.active_from=(SELECT MAX(m2.active_from) FROM instrument_provider_mappings m2 WHERE m2.instrument_id=m.instrument_id AND m2.provider=m.provider AND m2.active_from<=?)
			ORDER BY provider`, item.InstrumentID, s.now().Format(time.RFC3339Nano), s.now().Format(time.RFC3339Nano))
		if queryErr != nil {
			return domain.MarketDataSetup{}, queryErr
		}
		for mappingRows.Next() {
			var mapping domain.MarketMappingView
			if err := mappingRows.Scan(&mapping.Provider, &mapping.ProviderSymbol, &mapping.QuoteCurrency, &mapping.ActiveFrom, &mapping.Provenance); err != nil {
				mappingRows.Close()
				return domain.MarketDataSetup{}, err
			}
			item.Mappings = append(item.Mappings, mapping)
		}
		if err := mappingRows.Err(); err != nil {
			mappingRows.Close()
			return domain.MarketDataSetup{}, err
		}
		mappingRows.Close()
		setup.Instruments = append(setup.Instruments, item)
	}
	etagInput, _ := json.Marshal(struct {
		PortfolioID string                        `json:"portfolio_id"`
		Instruments []domain.MarketInstrumentView `json:"instruments"`
	}{setup.PortfolioID, setup.Instruments})
	digest := sha256.Sum256(etagInput)
	setup.ETag = `"` + hex.EncodeToString(digest[:]) + `"`
	return setup, nil
}

func validateMappingBatch(items []domain.MappingBatchItem) error {
	if len(items) == 0 || len(items) > 256 {
		return errors.New("mapping batch must contain between 1 and 256 entries")
	}
	seenInstrument := map[string]bool{}
	seenTarget := map[string]string{}
	for _, item := range items {
		item.InstrumentID = strings.TrimSpace(item.InstrumentID)
		item.Provider = strings.TrimSpace(item.Provider)
		item.ProviderSymbol = strings.TrimSpace(item.ProviderSymbol)
		item.QuoteCurrency = strings.ToUpper(strings.TrimSpace(item.QuoteCurrency))
		item.Provenance = strings.TrimSpace(item.Provenance)
		provider, err := marketdata.ParseProvider(item.Provider)
		if err != nil || provider == marketdata.ProviderManual || provider == marketdata.ProviderOpenExchangeRates {
			return fmt.Errorf("unsupported mapping provider %q", item.Provider)
		}
		if item.InstrumentID == "" || item.ProviderSymbol == "" || !marketSymbol.MatchString(item.ProviderSymbol) {
			return fmt.Errorf("malformed mapping symbol")
		}
		if !marketCurrency.MatchString(item.QuoteCurrency) {
			return fmt.Errorf("malformed quote currency")
		}
		if item.Provenance == "" {
			return errors.New("mapping provenance is required")
		}
		if !item.ConfirmUnverified {
			return errors.New("exact provider identifier must be explicitly confirmed")
		}

		if seenInstrument[item.InstrumentID] {
			return fmt.Errorf("duplicate instrument %s", item.InstrumentID)
		}
		seenInstrument[item.InstrumentID] = true
		target := item.Provider + "\x00" + strings.ToLower(item.ProviderSymbol) + "\x00" + item.QuoteCurrency
		if previous, ok := seenTarget[target]; ok && previous != item.InstrumentID {
			return errors.New("provider target is assigned to more than one instrument")
		}
		seenTarget[target] = item.InstrumentID
	}
	return nil
}

func (s *Store) AddProviderMappingsBatch(ctx context.Context, portfolioID, expectedETag string, items []domain.MappingBatchItem) (domain.MappingBatchResult, error) {
	return s.AddProviderMappingsBatchAs(ctx, portfolioID, expectedETag, items, MutationActor{Category: "administrative", ID: "administrative-mapping"})
}

// AddProviderMappingsBatchAs preserves the exact actor for machine-authorized
// mapping writes while retaining the legacy administrative actor for existing
// callers of AddProviderMappingsBatch.
func (s *Store) AddProviderMappingsBatchAs(ctx context.Context, portfolioID, expectedETag string, items []domain.MappingBatchItem, actor MutationActor) (domain.MappingBatchResult, error) {
	if err := validateMappingBatch(items); err != nil {
		return domain.MappingBatchResult{}, err
	}
	setup, err := s.MarketDataSetup(ctx, portfolioID, nil)
	if err != nil {
		return domain.MappingBatchResult{}, err
	}
	if expectedETag == "" || expectedETag != setup.ETag {
		return domain.MappingBatchResult{}, fmt.Errorf("mapping setup is stale")
	}
	activeIDs := make(map[string]struct{}, len(setup.Instruments))
	for _, instrument := range setup.Instruments {
		activeIDs[instrument.InstrumentID] = struct{}{}
	}
	if len(items) != len(activeIDs) {
		return domain.MappingBatchResult{}, fmt.Errorf("mapping batch must cover every active instrument")
	}
	for _, item := range items {
		if _, ok := activeIDs[strings.TrimSpace(item.InstrumentID)]; !ok {
			return domain.MappingBatchResult{}, fmt.Errorf("instrument is not in the active setup")
		}
	}
	for _, item := range items {
		var instrumentCurrency string
		if err := s.DB.QueryRowContext(ctx, `SELECT i.currency FROM instruments i JOIN lots l ON l.instrument_id=i.id WHERE i.id=? AND l.portfolio_id=? AND l.lifecycle='active' LIMIT 1`, strings.TrimSpace(item.InstrumentID), portfolioID).Scan(&instrumentCurrency); err != nil {
			if err == sql.ErrNoRows {
				return domain.MappingBatchResult{}, fmt.Errorf("instrument is not in this portfolio")
			}
			return domain.MappingBatchResult{}, err
		}
		if strings.ToUpper(instrumentCurrency) != strings.ToUpper(strings.TrimSpace(item.QuoteCurrency)) {
			return domain.MappingBatchResult{}, fmt.Errorf("mapping quote currency does not match instrument currency")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.MappingBatchResult{}, err
	}
	defer tx.Rollback()
	now := s.now().Format(time.RFC3339Nano)
	result := domain.MappingBatchResult{Status: "saved", Outcomes: make([]domain.MappingBatchOutcome, 0, len(items))}
	for _, input := range items {
		input.InstrumentID = strings.TrimSpace(input.InstrumentID)
		input.Provider = strings.TrimSpace(input.Provider)
		input.ProviderSymbol = strings.TrimSpace(input.ProviderSymbol)
		input.QuoteCurrency = strings.ToUpper(strings.TrimSpace(input.QuoteCurrency))
		input.Provenance = strings.TrimSpace(input.Provenance)
		provider, _ := marketdata.ParseProvider(strings.TrimSpace(input.Provider))
		active := strings.TrimSpace(input.ActiveFrom)
		if active == "" {
			active = now
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, active)
		if parseErr != nil {
			return domain.MappingBatchResult{}, fmt.Errorf("invalid active-from timestamp")
		}
		active = parsed.UTC().Format(time.RFC3339Nano)
		var existingInstrument string
		lookupErr := tx.QueryRowContext(ctx, `SELECT instrument_id FROM instrument_provider_mappings WHERE provider=? AND provider_symbol=? AND quote_currency=? LIMIT 1`, provider.String(), input.ProviderSymbol, input.QuoteCurrency).Scan(&existingInstrument)
		if lookupErr == nil && existingInstrument != input.InstrumentID {
			return domain.MappingBatchResult{}, errors.New("provider target is already assigned to another instrument")
		}
		if lookupErr != nil && lookupErr != sql.ErrNoRows {
			return domain.MappingBatchResult{}, lookupErr
		}
		id := deterministicID("mapping", input.InstrumentID, provider.String(), strings.TrimSpace(input.ProviderSymbol), strings.ToUpper(strings.TrimSpace(input.QuoteCurrency)), active)
		mapping := domain.ProviderMapping{ID: id, InstrumentID: strings.TrimSpace(input.InstrumentID), Provider: provider.String(), ProviderSymbol: strings.TrimSpace(input.ProviderSymbol), QuoteCurrency: strings.ToUpper(strings.TrimSpace(input.QuoteCurrency)), ActiveFrom: active, Provenance: strings.TrimSpace(input.Provenance), CreatedAt: now}
		payload, _ := json.Marshal(mapping)
		insert, execErr := tx.ExecContext(ctx, `INSERT INTO instrument_provider_mappings(id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, mapping.ID, mapping.InstrumentID, mapping.Provider, mapping.ProviderSymbol, mapping.QuoteCurrency, mapping.ActiveFrom, mapping.Provenance, mapping.CreatedAt)
		if execErr != nil {
			return domain.MappingBatchResult{}, fmt.Errorf("persist mapping batch: %w", execErr)
		}
		inserted, _ := insert.RowsAffected()
		if inserted > 0 {
			if _, execErr = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, deterministicID("audit", "mapping", mapping.ID), actor.ID, "market.mapping.batch_saved", "provider_mapping", mapping.ID, payload, now); execErr != nil {
				return domain.MappingBatchResult{}, execErr
			}
			portfolioIDs, execErr := portfoliosForInstrument(ctx, tx, mapping.InstrumentID)
			if execErr != nil {
				return domain.MappingBatchResult{}, execErr
			}
			if execErr = s.recordActivityForPortfolios(ctx, tx, portfolioIDs, actor.Category, "market.mapping.batch_saved", "provider_mapping", mapping.ID, now, deterministicID("audit", "mapping", mapping.ID)); execErr != nil {
				return domain.MappingBatchResult{}, execErr
			}
		}
		result.Outcomes = append(result.Outcomes, domain.MappingBatchOutcome{InstrumentID: mapping.InstrumentID, Status: "saved", Mapping: &mapping})
	}
	if err := tx.Commit(); err != nil {
		return domain.MappingBatchResult{}, err
	}
	updated, err := s.MarketDataSetup(ctx, portfolioID, nil)
	if err != nil {
		return domain.MappingBatchResult{}, err
	}
	result.ETag = updated.ETag
	return result, nil
}

func MarketProviders(keys map[marketdata.Provider]bool) []domain.ProviderReadiness {
	providers := []domain.ProviderReadiness{
		{Name: marketdata.ProviderFinnhub.String(), Configured: keys[marketdata.ProviderFinnhub]},
		{Name: marketdata.ProviderAlphaVantage.String(), Configured: keys[marketdata.ProviderAlphaVantage]},
		{Name: marketdata.ProviderCoinGecko.String(), Configured: true, Public: true},
		{Name: marketdata.ProviderOpenExchangeRates.String(), Configured: keys[marketdata.ProviderOpenExchangeRates]},
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	return providers
}

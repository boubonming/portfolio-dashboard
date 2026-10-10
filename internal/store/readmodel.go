package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var ErrPortfolioNotFound = errors.New("portfolio not found")

var ErrInvalidReportingCurrency = errors.New("invalid reporting currency")

type sourceHolding struct {
	lotID, symbol, description, quantity, currency string
	unitCost, costBasis, broker, acquisitionDate   sql.NullString
	sourceRow                                      int
}

// PortfolioOverview returns a read-only, portfolio-scoped view. A requested
// currency is calculated from the latest immutable snapshot input; no snapshot
// is written as a side effect of serving the API.
func (s *Store) PortfolioOverview(ctx context.Context, portfolioID, reportingCurrency string) (domain.PortfolioOverview, error) {
	if strings.TrimSpace(portfolioID) == "" {
		return domain.PortfolioOverview{}, ErrPortfolioNotFound
	}
	currency, err := normalizeReportingCurrency(reportingCurrency)
	if err != nil {
		return domain.PortfolioOverview{}, err
	}

	var view domain.PortfolioOverview
	var defaultCurrency string
	if err := s.DB.QueryRowContext(ctx, `SELECT id,name,reporting_currency FROM portfolios WHERE id = ?`, portfolioID).Scan(&view.Portfolio.ID, &view.Portfolio.Name, &defaultCurrency); err != nil {
		if err == sql.ErrNoRows {
			return domain.PortfolioOverview{}, ErrPortfolioNotFound
		}
		return domain.PortfolioOverview{}, err
	}
	view.Portfolio.DefaultCurrency = defaultCurrency
	view.ReportingCurrency = currency
	view.Holdings, err = s.loadSourceHoldings(ctx, portfolioID)
	if err != nil {
		return domain.PortfolioOverview{}, err
	}
	view.Snapshot = domain.SnapshotView{State: "no_snapshot", Subtotals: []domain.CurrencySubtotal{}}
	view.Allocation = domain.AllocationView{
		State:    "unavailable",
		Coverage: domain.AllocationCoverage{TotalLots: len(view.Holdings)},
	}

	var encoded []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT payload FROM snapshots WHERE portfolio_id = ? ORDER BY created_at DESC LIMIT 1`, portfolioID).Scan(&encoded); err != nil {
		if err == sql.ErrNoRows {
			for i := range view.Holdings {
				view.Holdings[i].DataQuality = append(view.Holdings[i].DataQuality, "no_snapshot")
				addSourceFlags(&view.Holdings[i])
			}
			return view, nil
		}
		return domain.PortfolioOverview{}, err
	}

	var payload snapshotPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return domain.PortfolioOverview{}, err
	}
	input := payload.Input
	input.ReportingCurrency = currency
	legacyFreshnessMetadata := payload.MaxAge == nil
	if payload.MaxAge != nil {
		input.MaxAge = *payload.MaxAge
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err == nil {
		input.AsOf = asOf
	}
	valuation := payload.Valuation
	if currency != payload.Valuation.ReportingCurrency {
		if legacyFreshnessMetadata {
			// Legacy payloads do not say which freshness threshold was used when
			// the immutable valuation was created. Recalculating an alternate
			// currency with a guessed threshold could turn stale data complete.
			// Keep the persisted valuation for same-currency reads, but explicitly
			// withhold alternate reporting values and the aggregate.
			valuation.Complete = false
			valuation.ReportingTotal = ""
			valuation.MissingDependencies = appendUnique(valuation.MissingDependencies, "snapshot:max_age")
			for i := range valuation.Lots {
				valuation.Lots[i].ReportingValue = nil
				valuation.Lots[i].FXIDs = nil
			}
		} else {
			valuation, err = domain.Calculate(input)
			if err != nil {
				return domain.PortfolioOverview{}, err
			}
		}
	}
	view.Snapshot = snapshotView(payload, valuation)
	view.Allocation = domain.BuildAllocation(input, valuation)
	if legacyFreshnessMetadata {
		// The persisted valuation remains available for the existing table, but
		// its freshness threshold is not reproducible. Never turn that legacy
		// payload into a normalized allocation distribution.
		view.Allocation.State = "partial"
		view.Allocation.ByInstrument = nil
		view.Allocation.BySourceCurrency = nil
		view.Allocation.Coverage.MissingDependencies++
	}

	values := make(map[string]domain.LotValuation, len(valuation.Lots))
	for _, lot := range valuation.Lots {
		values[lot.LotID] = lot
	}
	quotes := latestQuoteByInstrument(input.Quotes)
	inputLots := make(map[string]domain.ValuationLot, len(input.Lots))
	for _, lot := range input.Lots {
		inputLots[lot.ID] = lot
	}
	for i := range view.Holdings {
		holding := &view.Holdings[i]
		lot, inSnapshot := inputLots[holding.LotID]
		if !inSnapshot {
			holding.DataQuality = append(holding.DataQuality, "not_in_snapshot")
			addSourceFlags(holding)
			continue
		}
		if value, ok := values[holding.LotID]; ok {
			if value.Price != "" {
				holding.LatestPrice = stringPointer(value.Price)
			}
			if value.MarketValue != "" {
				holding.LatestValue = stringPointer(value.MarketValue)
			}
			holding.ReportingValue = value.ReportingValue
			holding.Freshness = value.Freshness
			if value.CostBasis != nil {
				holding.CostBasis = value.CostBasis
			}
		}
		if quote, ok := quotes[lot.InstrumentID]; ok {
			holding.QuoteSource = quote.Source
		}
		appendDependencyFlags(holding, valuation, lot, input, currency)
		addSourceFlags(holding)
	}
	return view, nil
}

func normalizeReportingCurrency(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		value = "MYR"
	}
	if len(value) != 3 {
		return "", ErrInvalidReportingCurrency
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return "", ErrInvalidReportingCurrency
		}
	}
	return value, nil
}

func (s *Store) loadSourceHoldings(ctx context.Context, portfolioID string) ([]domain.HoldingView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT l.id,i.symbol,COALESCE(i.description,''),l.quantity,l.currency,l.unit_cost,l.cost_basis,a.broker,l.acquisition_date,COALESCE(l.source_row,0) FROM lots l JOIN instruments i ON i.id = l.instrument_id LEFT JOIN accounts a ON a.id = l.account_id AND a.portfolio_id = l.portfolio_id WHERE l.portfolio_id = ? ORDER BY l.source_row,l.id`, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	holdings := []domain.HoldingView{}
	for rows.Next() {
		var source sourceHolding
		if err := rows.Scan(&source.lotID, &source.symbol, &source.description, &source.quantity, &source.currency, &source.unitCost, &source.costBasis, &source.broker, &source.acquisitionDate, &source.sourceRow); err != nil {
			return nil, err
		}
		holding := domain.HoldingView{LotID: source.lotID, Symbol: source.symbol, Description: source.description, Quantity: source.quantity, Currency: source.currency, Freshness: domain.Freshness{Status: domain.Missing}, DataQuality: []string{}}
		if source.unitCost.Valid {
			holding.UnitCost = stringPointer(source.unitCost.String)
		}
		if source.costBasis.Valid {
			holding.CostBasis = stringPointer(source.costBasis.String)
		}
		if source.broker.Valid && strings.TrimSpace(source.broker.String) != "" {
			holding.Broker = stringPointer(source.broker.String)
		}
		if source.acquisitionDate.Valid && strings.TrimSpace(source.acquisitionDate.String) != "" {
			holding.AcquisitionDate = stringPointer(source.acquisitionDate.String)
		}
		holding.MissingBroker = holding.Broker == nil
		holding.MissingAcquisitionDate = holding.AcquisitionDate == nil
		holding.SourceRow = source.sourceRow
		holdings = append(holdings, holding)
	}
	return holdings, rows.Err()
}

func snapshotView(payload snapshotPayload, valuation domain.Valuation) domain.SnapshotView {
	state := "incomplete"
	if valuation.Complete {
		state = "complete"
	}
	return domain.SnapshotView{State: state, SnapshotID: payload.SnapshotID, CalculationVersion: payload.CalculationVersion, CreatedAt: payload.CreatedAt, AsOf: payload.AsOf, Complete: valuation.Complete, ReportingTotal: valuation.ReportingTotal, MissingDependencies: valuation.MissingDependencies, StaleDependencies: valuation.StaleDependencies, InvalidDependencies: valuation.InvalidDependencies, SourceTimestamps: valuation.SourceTimestamps, Subtotals: valuation.Subtotals}
}

func latestQuoteByInstrument(quotes []domain.Quote) map[string]domain.Quote {
	latest := make(map[string]domain.Quote)
	for _, quote := range quotes {
		current, ok := latest[quote.InstrumentID]
		if !ok || quote.MarketAt > current.MarketAt || (quote.MarketAt == current.MarketAt && quote.FetchedAt > current.FetchedAt) {
			latest[quote.InstrumentID] = quote
		}
	}
	return latest
}

func appendDependencyFlags(holding *domain.HoldingView, valuation domain.Valuation, lot domain.ValuationLot, input domain.ValuationInput, reportingCurrency string) {
	for _, dependency := range valuation.MissingDependencies {
		if dependencyAppliesToLot(dependency, lot, input, reportingCurrency) {
			holding.DataQuality = appendUnique(holding.DataQuality, "missing_dependency")
		}
	}
	for _, dependency := range valuation.StaleDependencies {
		if dependencyAppliesToLot(dependency, lot, input, reportingCurrency) {
			holding.DataQuality = appendUnique(holding.DataQuality, "stale_dependency")
		}
	}
	for _, dependency := range valuation.InvalidDependencies {
		if dependencyAppliesToLot(dependency, lot, input, reportingCurrency) {
			holding.DataQuality = appendUnique(holding.DataQuality, "invalid_dependency")
		}
	}
}

func dependencyAppliesToLot(dependency string, lot domain.ValuationLot, input domain.ValuationInput, reportingCurrency string) bool {
	if strings.HasPrefix(dependency, "lot:") || strings.HasPrefix(dependency, "cost:") {
		return strings.HasPrefix(dependency, "lot:"+lot.ID+":") || dependency == "cost:"+lot.ID
	}
	if strings.HasPrefix(dependency, "quote:") {
		identifier := strings.TrimPrefix(dependency, "quote:")
		if identifier == lot.InstrumentID {
			return true
		}
		for _, quote := range input.Quotes {
			if quote.ID == identifier {
				return quote.InstrumentID == lot.InstrumentID
			}
		}
		return false
	}
	if strings.Contains(dependency, " fx:") {
		pair := strings.SplitN(dependency, " fx:", 2)[1]
		if strings.Contains(pair, "/") {
			return fxPairApplies(pair, lot.Currency, reportingCurrency)
		}
		for _, fx := range input.FXRates {
			if fx.ID == pair {
				return fxPairApplies(fx.BaseCurrency+"/"+fx.QuoteCurrency, lot.Currency, reportingCurrency)
			}
		}
	}
	return false
}

func fxPairApplies(pair, lotCurrency, reportingCurrency string) bool {
	parts := strings.Split(pair, "/")
	if len(parts) != 2 {
		return false
	}
	from, to := strings.ToUpper(strings.TrimSpace(parts[0])), strings.ToUpper(strings.TrimSpace(parts[1]))
	lotCurrency, reportingCurrency = strings.ToUpper(strings.TrimSpace(lotCurrency)), strings.ToUpper(strings.TrimSpace(reportingCurrency))
	return (from == lotCurrency && to == reportingCurrency) || (from == reportingCurrency && to == lotCurrency)
}

func addSourceFlags(holding *domain.HoldingView) {
	if holding.MissingBroker {
		holding.DataQuality = appendUnique(holding.DataQuality, "missing_broker")
	}
	if holding.MissingAcquisitionDate {
		holding.DataQuality = appendUnique(holding.DataQuality, "missing_acquisition_date")
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stringPointer(value string) *string { return &value }

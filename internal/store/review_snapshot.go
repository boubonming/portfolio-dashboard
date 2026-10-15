package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var (
	ErrSnapshotNotFound = errors.New("snapshot not found")
	ErrSnapshotCorrupt  = errors.New("snapshot is corrupt")
)

var canonicalDecimal = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
var canonicalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)

type persistedSnapshotHolding struct {
	holdingID    string
	instrumentID string
	symbol       string
	name         string
}

func (s *Store) ReviewSnapshot(ctx context.Context, portfolioID, snapshotID string, latest bool) (domain.ReviewSnapshot, error) {
	if strings.TrimSpace(portfolioID) == "" || strings.TrimSpace(snapshotID) != snapshotID || strings.ContainsAny(portfolioID+snapshotID, "/\\%\x00\r\n") {
		return domain.ReviewSnapshot{}, ErrSnapshotNotFound
	}
	var portfolioExists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id=?", portfolioID).Scan(&portfolioExists); err != nil {
		return domain.ReviewSnapshot{}, err
	}
	if portfolioExists == 0 {
		return domain.ReviewSnapshot{}, ErrPortfolioNotFound
	}

	var encoded []byte
	var storedID, calculationVersion string
	query := `SELECT id,payload,calculation_version FROM snapshots WHERE portfolio_id=? AND id=?`
	args := []any{portfolioID, snapshotID}
	if latest {
		query = `SELECT id,payload,calculation_version FROM snapshots WHERE portfolio_id=? ORDER BY created_at DESC,id DESC LIMIT 1`
		args = []any{portfolioID}
	}
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&storedID, &encoded, &calculationVersion); err != nil {
		if err == sql.ErrNoRows {
			if latest {
				return noReviewSnapshot(portfolioID), nil
			}
			return domain.ReviewSnapshot{}, ErrSnapshotNotFound
		}
		return domain.ReviewSnapshot{}, err
	}
	return reconstructReviewSnapshot(ctx, s.DB, portfolioID, storedID, calculationVersion, encoded)
}

func noReviewSnapshot(portfolioID string) domain.ReviewSnapshot {
	return domain.ReviewSnapshot{PortfolioID: portfolioID, State: "no_snapshot", Complete: false, MissingDependencies: []string{}, StaleDependencies: []string{}, InvalidDependencies: []string{}, Holdings: []domain.ReviewHoldingContext{}}
}

func reconstructReviewSnapshot(ctx context.Context, db *sql.DB, portfolioID, storedID, storedVersion string, encoded []byte) (domain.ReviewSnapshot, error) {
	var payload snapshotPayload
	if err := jsonUnmarshal(encoded, &payload); err != nil {
		return domain.ReviewSnapshot{}, fmt.Errorf("%w: payload: %v", ErrSnapshotCorrupt, err)
	}
	if payload.PortfolioID != portfolioID || payload.SnapshotID != storedID || payload.CalculationVersion != storedVersion || !validID(payload.PortfolioID, 128) || !validID(payload.SnapshotID, 128) || !validCalculationVersion(payload.CalculationVersion) || !validCanonicalTime(payload.CreatedAt) || !validCanonicalTime(payload.AsOf) {
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}
	var reviewVersion string
	if err := db.QueryRowContext(ctx, "SELECT calculation_version FROM review_snapshots WHERE portfolio_id=? AND snapshot_id=?", portfolioID, storedID).Scan(&reviewVersion); err != nil || reviewVersion != storedVersion {
		if err == sql.ErrNoRows {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if err != nil {
			return domain.ReviewSnapshot{}, err
		}
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}
	if len(payload.Valuation.Lots) != len(payload.Input.Lots) || len(payload.Valuation.MissingDependencies) > 100 || len(payload.Valuation.StaleDependencies) > 100 || len(payload.Valuation.InvalidDependencies) > 100 {
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}
	seenDependencies := make(map[string]struct{})
	for _, dependency := range append(append(append([]string{}, payload.Valuation.MissingDependencies...), payload.Valuation.StaleDependencies...), payload.Valuation.InvalidDependencies...) {
		if !validIdentifier(dependency, 128) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if _, duplicate := seenDependencies[dependency]; duplicate {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		seenDependencies[dependency] = struct{}{}
	}
	if !validCurrency(payload.Valuation.ReportingCurrency) || (payload.Valuation.Complete && !validDecimal(payload.Valuation.ReportingTotal)) {
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}
	for _, subtotal := range payload.Valuation.Subtotals {
		if !validCurrency(subtotal.Currency) || !validDecimal(subtotal.MarketValue) || !validDecimalOptional(subtotal.CostBasis) || !validDecimalOptional(subtotal.UnrealisedPnL) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
	}
	if payload.Valuation.Complete && (len(seenDependencies) != 0 || payload.Valuation.ReportingTotal == "") {
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}

	persisted, err := loadPersistedSnapshotHoldings(ctx, db, portfolioID, storedID, len(payload.Input.Lots))
	if err != nil {
		return domain.ReviewSnapshot{}, err
	}
	if len(persisted) != len(payload.Input.Lots) {
		return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
	}
	seenPayloadHoldings := make(map[string]struct{}, len(payload.Input.Lots))
	for _, lot := range payload.Input.Lots {
		if _, duplicate := seenPayloadHoldings[lot.ID]; duplicate {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		seenPayloadHoldings[lot.ID] = struct{}{}
		row, ok := persisted[lot.ID]
		if !ok || row.instrumentID != lot.InstrumentID {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
	}

	result := domain.ReviewSnapshot{PortfolioID: portfolioID, SnapshotID: storedID, CalculationVersion: storedVersion, CreatedAt: payload.CreatedAt, AsOf: payload.AsOf, Complete: payload.Valuation.Complete, MissingDependencies: append([]string{}, payload.Valuation.MissingDependencies...), StaleDependencies: append([]string{}, payload.Valuation.StaleDependencies...), InvalidDependencies: append([]string{}, payload.Valuation.InvalidDependencies...), Holdings: []domain.ReviewHoldingContext{}}
	if result.Complete {
		result.State = "complete"
	} else {
		result.State = "incomplete"
	}
	result.Holdings = make([]domain.ReviewHoldingContext, 0, len(payload.Input.Lots))
	quotes := make(map[string]domain.Quote, len(payload.Input.Quotes))
	for _, quote := range payload.Input.Quotes {
		if !validID(quote.ID, 128) || !validID(quote.InstrumentID, 128) || !validDecimal(quote.Price) || !validCurrency(quote.Currency) || !validCanonicalTime(quote.MarketAt) || strings.TrimSpace(quote.Source) == "" || len(quote.Source) > 128 {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if _, duplicate := quotes[quote.ID]; duplicate {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		quotes[quote.ID] = quote
	}
	for index, inputLot := range payload.Input.Lots {
		valuedLot := payload.Valuation.Lots[index]
		if !validID(inputLot.ID, 128) || valuedLot.LotID != inputLot.ID || valuedLot.Symbol != inputLot.Symbol || valuedLot.InstrumentID != inputLot.InstrumentID || valuedLot.Currency != inputLot.Currency || valuedLot.Quantity != inputLot.Quantity || !validDecimal(inputLot.Quantity) || !validID(inputLot.InstrumentID, 128) || !validSymbol(inputLot.Symbol) || !validCurrency(inputLot.Currency) || !validDecimalPointer(inputLot.UnitCost) || !validDecimalPointer(inputLot.CostBasis) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		row := persisted[inputLot.ID]
		if row.symbol != inputLot.Symbol {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		name := row.name
		if strings.TrimSpace(name) == "" {
			name = inputLot.Symbol
		}
		if !validIdentifier(name, 256) || !validDecimal(valuedLot.Quantity) || !validDecimalOptional(valuedLot.Price) || !validDecimalOptional(valuedLot.MarketValue) || !validDecimalPointer(valuedLot.ReportingValue) || !validDecimalPointer(valuedLot.CostBasis) || !validDecimalPointer(valuedLot.UnrealisedPnL) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if result.Complete && (valuedLot.ReportingValue == nil || valuedLot.Freshness.Status != domain.Fresh) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if result.Complete && (valuedLot.Price == "" || valuedLot.MarketValue == "" || valuedLot.QuoteID == "") {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if valuedLot.Freshness.Status != domain.Fresh && (valuedLot.Price != "" || valuedLot.MarketValue != "" || valuedLot.ReportingValue != nil) {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		item := domain.ReviewHoldingContext{PortfolioID: portfolioID, SnapshotID: storedID, HoldingID: inputLot.ID, InstrumentID: inputLot.InstrumentID, Symbol: inputLot.Symbol, Name: name, Quantity: inputLot.Quantity, Currency: inputLot.Currency, UnitCost: inputLot.UnitCost, CostBasis: valuedLot.CostBasis, LatestPrice: stringPtrIfNonempty(valuedLot.Price), LatestValue: stringPtrIfNonempty(valuedLot.MarketValue), ReportingValue: valuedLot.ReportingValue, FreshnessState: valuedLot.Freshness.Status}
		if item.FreshnessState == "" {
			item.FreshnessState = domain.Invalid
		}
		if item.FreshnessState != domain.Fresh && item.FreshnessState != domain.Stale && item.FreshnessState != domain.Missing && item.FreshnessState != domain.Future && item.FreshnessState != domain.Invalid {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if valuedLot.QuoteID != "" {
			quote, ok := quotes[valuedLot.QuoteID]
			if !ok || quote.InstrumentID != inputLot.InstrumentID || quote.Currency != inputLot.Currency || valuedLot.Freshness.Status == domain.Missing {
				return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
			}
			item.QuoteSource, item.MarketAt = quote.Source, quote.MarketAt
		} else if valuedLot.Freshness.Status == domain.Fresh {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		age, validAge := freshnessAgeSeconds(valuedLot.Freshness.Age)
		if !validAge {
			return domain.ReviewSnapshot{}, ErrSnapshotCorrupt
		}
		if age != "" {
			item.FreshnessAgeSeconds = &age
		}
		result.Holdings = append(result.Holdings, item)
	}
	return result, nil
}

func loadPersistedSnapshotHoldings(ctx context.Context, db *sql.DB, portfolioID, snapshotID string, expected int) (map[string]persistedSnapshotHolding, error) {
	var rawCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshot_holdings WHERE portfolio_id=? AND snapshot_id=?", portfolioID, snapshotID).Scan(&rawCount); err != nil {
		return nil, err
	}
	if rawCount != expected {
		return nil, ErrSnapshotCorrupt
	}
	rows, err := db.QueryContext(ctx, `
		SELECT sh.holding_id,sh.instrument_id,i.symbol,COALESCE(i.description,'')
		FROM snapshot_holdings sh
		JOIN lots l ON l.id=sh.holding_id AND l.portfolio_id=sh.portfolio_id AND l.instrument_id=sh.instrument_id
		JOIN instruments i ON i.id=sh.instrument_id
		WHERE sh.portfolio_id=? AND sh.snapshot_id=?
		ORDER BY sh.holding_id`, portfolioID, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]persistedSnapshotHolding)
	for rows.Next() {
		var row persistedSnapshotHolding
		if err := rows.Scan(&row.holdingID, &row.instrumentID, &row.symbol, &row.name); err != nil {
			return nil, err
		}
		if _, duplicate := result[row.holdingID]; duplicate {
			return nil, ErrSnapshotCorrupt
		}
		result[row.holdingID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// jsonUnmarshal is a small seam that keeps reconstruction's error handling
// explicit without introducing a second numeric representation.
func jsonUnmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}

func validDecimal(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && canonicalDecimal.MatchString(value)
}
func validDecimalPointer(value *string) bool { return value == nil || validDecimal(*value) }
func validDecimalOptional(value string) bool { return value == "" || validDecimal(value) }
func validCurrency(value string) bool {
	return len(value) == 3 && value == strings.ToUpper(value) && value >= "AAA" && value <= "ZZZ"
}
func validIdentifier(value string, max int) bool {
	return len(value) >= 1 && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
func validID(value string, max int) bool {
	return len(value) >= 1 && len(value) <= max && canonicalID.MatchString(value)
}
func validSymbol(value string) bool {
	return len(value) >= 1 && len(value) <= 64 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
func validCalculationVersion(value string) bool {
	return validID(value, 128)
}
func validCanonicalTime(value string) bool {
	if len(value) != 30 || !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000000000Z", value)
	return err == nil && parsed.UTC().Format("2006-01-02T15:04:05.000000000Z") == value
}
func canonicalSnapshotTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
func stringPtrIfNonempty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func freshnessAgeSeconds(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 || duration.String() != value {
		return "", false
	}
	seconds := fmt.Sprintf("%d.%09d", duration/time.Second, duration%time.Second)
	seconds = strings.TrimRight(strings.TrimRight(seconds, "0"), ".")
	if seconds == "" {
		return "0", true
	}
	return seconds, true
}

// Keep this helper available for callers that compare dependency output while
// guaranteeing deterministic order on malformed legacy payloads.
func sortedCopy(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

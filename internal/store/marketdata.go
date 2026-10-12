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
)

const futureTimestampTolerance = domain.DefaultFutureTolerance

var ErrObservationIDConflict = errors.New("observation id conflicts with existing immutable row")

type QuoteInput struct {
	ID           string
	PortfolioIDs []string
	InstrumentID string
	Symbol       string
	Currency     string
	Price        string
	Source       string
	MarketAt     string
	FetchedAt    string
	Basis        string
	Provenance   string
}

type FXInput struct {
	ID            string
	PortfolioIDs  []string
	BaseCurrency  string
	QuoteCurrency string
	Rate          string
	Source        string
	MarketAt      string
	FetchedAt     string
	Basis         string
	Provenance    string
}

func quotePersistenceEqual(left, right domain.Quote) bool {
	return left.ID == right.ID && left.InstrumentID == right.InstrumentID && left.Price == right.Price &&
		left.Currency == right.Currency && left.Source == right.Source && left.MarketAt == right.MarketAt &&
		left.FetchedAt == right.FetchedAt && left.Basis == right.Basis && left.Provenance == right.Provenance
}

func fxPersistenceEqual(left, right domain.FXRate) bool {
	return left.ID == right.ID && left.BaseCurrency == right.BaseCurrency && left.QuoteCurrency == right.QuoteCurrency &&
		left.Rate == right.Rate && left.Source == right.Source && left.MarketAt == right.MarketAt &&
		left.FetchedAt == right.FetchedAt && left.Basis == right.Basis && left.Provenance == right.Provenance
}

func readExistingQuote(ctx context.Context, tx *sql.Tx, id string) (domain.Quote, error) {
	var quote domain.Quote
	err := tx.QueryRowContext(ctx, `SELECT id,instrument_id,price,currency,source,market_at,fetched_at,basis,provenance FROM quotes WHERE id = ?`, id).
		Scan(&quote.ID, &quote.InstrumentID, &quote.Price, &quote.Currency, &quote.Source, &quote.MarketAt, &quote.FetchedAt, &quote.Basis, &quote.Provenance)
	return quote, err
}

func readExistingFX(ctx context.Context, tx *sql.Tx, id string) (domain.FXRate, error) {
	var fx domain.FXRate
	err := tx.QueryRowContext(ctx, `SELECT id,base_currency,quote_currency,rate,source,market_at,fetched_at,basis,provenance FROM fx_rates WHERE id = ?`, id).
		Scan(&fx.ID, &fx.BaseCurrency, &fx.QuoteCurrency, &fx.Rate, &fx.Source, &fx.MarketAt, &fx.FetchedAt, &fx.Basis, &fx.Provenance)
	return fx, err
}

func normalizeObservation(value string, allowEmpty bool, now time.Time) (string, error) {
	if strings.TrimSpace(value) == "" && allowEmpty {
		value = now.UTC().Format(activityTimestampFormat)
	}
	parsed, err := domain.ValidateObservationTimestamp(value)
	if err != nil {
		return "", fmt.Errorf("invalid timestamp %q", value)
	}
	return parsed.UTC().Format(activityTimestampFormat), nil
}

func validateObservationTimes(now time.Time, marketAt, fetchedAt string) error {
	market, marketErr := domain.ValidateObservationTimestamp(marketAt)
	fetched, fetchedErr := domain.ValidateObservationTimestamp(fetchedAt)
	if marketErr != nil || fetchedErr != nil {
		return errors.Join(marketErr, fetchedErr)
	}
	if market.After(fetched) {
		return fmt.Errorf("market timestamp must not be after fetched timestamp")
	}
	if market.After(now.UTC().Add(futureTimestampTolerance)) {
		return fmt.Errorf("market timestamp is in the future beyond %s tolerance", futureTimestampTolerance)
	}
	if fetched.After(now.UTC().Add(futureTimestampTolerance)) {
		return fmt.Errorf("fetched timestamp is in the future beyond %s tolerance", futureTimestampTolerance)
	}
	return nil
}

func (s *Store) AddQuote(ctx context.Context, input QuoteInput) (domain.Quote, error) {
	price, err := domain.ParseDecimal(input.Price)
	if err != nil || price.Sign() <= 0 {
		return domain.Quote{}, fmt.Errorf("invalid positive quote price: %q", input.Price)
	}
	if strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.Basis) == "" || strings.TrimSpace(input.Provenance) == "" {
		return domain.Quote{}, fmt.Errorf("quote source, basis and provenance are required")
	}
	now := s.now()
	marketAt, err := normalizeObservation(input.MarketAt, false, now)
	if err != nil {
		return domain.Quote{}, err
	}
	fetchedAt, err := normalizeObservation(input.FetchedAt, true, now)
	if err != nil {
		return domain.Quote{}, err
	}
	if err = validateObservationTimes(now, marketAt, fetchedAt); err != nil {
		return domain.Quote{}, err
	}
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	instrumentID := strings.TrimSpace(input.InstrumentID)
	if instrumentID == "" {
		if strings.TrimSpace(input.Symbol) == "" || currency == "" {
			return domain.Quote{}, fmt.Errorf("instrument identity is required")
		}
		if err := s.DB.QueryRowContext(ctx, "SELECT id FROM instruments WHERE symbol = ? AND currency = ?", input.Symbol, currency).Scan(&instrumentID); err != nil {
			if err == sql.ErrNoRows {
				return domain.Quote{}, fmt.Errorf("unknown instrument %s/%s", input.Symbol, currency)
			}
			return domain.Quote{}, err
		}
	}
	var symbol, instrumentCurrency string
	if err := s.DB.QueryRowContext(ctx, "SELECT symbol, currency FROM instruments WHERE id = ?", instrumentID).Scan(&symbol, &instrumentCurrency); err == sql.ErrNoRows {
		return domain.Quote{}, fmt.Errorf("unknown instrument %s", instrumentID)
	} else if err != nil {
		return domain.Quote{}, err
	}
	if currency == "" {
		currency = instrumentCurrency
	}
	if instrumentCurrency != currency {
		return domain.Quote{}, fmt.Errorf("quote currency %s does not match instrument currency %s", currency, instrumentCurrency)
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = deterministicID("quote", instrumentID, price.String(), currency, input.Source, marketAt, fetchedAt, input.Basis, input.Provenance)
	}
	quote := domain.Quote{ID: id, InstrumentID: instrumentID, Symbol: symbol, Price: price.String(), Currency: currency, Source: strings.TrimSpace(input.Source), MarketAt: marketAt, FetchedAt: fetchedAt, Basis: strings.TrimSpace(input.Basis), Provenance: strings.TrimSpace(input.Provenance)}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Quote{}, err
	}
	defer tx.Rollback()
	portfolioIDs, err := portfoliosForInstrument(ctx, tx, quote.InstrumentID)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("find quote portfolios: %w", err)
	}
	if err := validatePortfolioAssociations(input.PortfolioIDs, portfolioIDs); err != nil {
		return domain.Quote{}, err
	}
	result, insertErr := tx.ExecContext(ctx, `INSERT INTO quotes(id,instrument_id,price,currency,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, quote.ID, quote.InstrumentID, quote.Price, quote.Currency, quote.Source, quote.MarketAt, quote.FetchedAt, quote.Basis, quote.Provenance)
	if insertErr != nil {
		return domain.Quote{}, fmt.Errorf("persist quote: %w", insertErr)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		existing, readErr := readExistingQuote(ctx, tx, quote.ID)
		if readErr != nil {
			return domain.Quote{}, fmt.Errorf("read existing quote: %w", readErr)
		}
		if !quotePersistenceEqual(quote, existing) {
			return domain.Quote{}, fmt.Errorf("%w: quote %s", ErrObservationIDConflict, quote.ID)
		}
		if err = tx.Commit(); err != nil {
			return domain.Quote{}, err
		}
		return quote, nil
	}
	payload, _ := json.Marshal(quote)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, deterministicID("audit", "quote", quote.ID), "administrative-quote", "market.quote.added", "quote", quote.ID, payload, fetchedAt); err != nil {
		return domain.Quote{}, fmt.Errorf("audit quote: %w", err)
	}
	auditID := deterministicID("audit", "quote", quote.ID)
	if err := s.recordActivityForPortfolios(ctx, tx, portfolioIDs, "administrative", "market.quote.added", "quote", quote.ID, fetchedAt, auditID); err != nil {
		return domain.Quote{}, fmt.Errorf("activity quote: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.Quote{}, err
	}
	return quote, nil
}

func (s *Store) AddFXRate(ctx context.Context, input FXInput) (domain.FXRate, error) {
	rate, err := domain.ParseDecimal(input.Rate)
	if err != nil || rate.Sign() <= 0 {
		return domain.FXRate{}, fmt.Errorf("invalid positive FX rate: %q", input.Rate)
	}
	base, quote := strings.ToUpper(strings.TrimSpace(input.BaseCurrency)), strings.ToUpper(strings.TrimSpace(input.QuoteCurrency))
	if base == "" || quote == "" {
		return domain.FXRate{}, fmt.Errorf("FX pair currencies are required")
	}
	if base == quote {
		return domain.FXRate{}, fmt.Errorf("FX pair must have distinct currencies")
	}
	if strings.TrimSpace(input.Source) == "" || strings.TrimSpace(input.Basis) == "" || strings.TrimSpace(input.Provenance) == "" {
		return domain.FXRate{}, fmt.Errorf("FX source, basis and provenance are required")
	}
	now := s.now()
	marketAt, err := normalizeObservation(input.MarketAt, false, now)
	if err != nil {
		return domain.FXRate{}, err
	}
	fetchedAt, err := normalizeObservation(input.FetchedAt, true, now)
	if err != nil {
		return domain.FXRate{}, err
	}
	if err = validateObservationTimes(now, marketAt, fetchedAt); err != nil {
		return domain.FXRate{}, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = deterministicID("fx", base, quote, rate.String(), input.Source, marketAt, fetchedAt, input.Basis, input.Provenance)
	}
	fx := domain.FXRate{ID: id, BaseCurrency: base, QuoteCurrency: quote, Rate: rate.String(), Source: strings.TrimSpace(input.Source), MarketAt: marketAt, FetchedAt: fetchedAt, Basis: strings.TrimSpace(input.Basis), Provenance: strings.TrimSpace(input.Provenance)}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.FXRate{}, err
	}
	defer tx.Rollback()
	portfolioIDs, err := portfoliosForFX(ctx, tx, fx.BaseCurrency, fx.QuoteCurrency)
	if err != nil {
		return domain.FXRate{}, fmt.Errorf("find FX portfolios: %w", err)
	}
	if err := validatePortfolioAssociations(input.PortfolioIDs, portfolioIDs); err != nil {
		return domain.FXRate{}, err
	}
	if existing, readErr := readExistingFX(ctx, tx, fx.ID); readErr == nil {
		if !fxPersistenceEqual(fx, existing) {
			return domain.FXRate{}, fmt.Errorf("%w: FX rate %s", ErrObservationIDConflict, fx.ID)
		}
		return fx, tx.Commit()
	} else if readErr != sql.ErrNoRows {
		return domain.FXRate{}, fmt.Errorf("read existing FX rate: %w", readErr)
	}
	var reverseCount int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fx_rates WHERE base_currency = ? AND quote_currency = ?`, fx.QuoteCurrency, fx.BaseCurrency).Scan(&reverseCount); err != nil {
		return domain.FXRate{}, fmt.Errorf("check FX pair orientation: %w", err)
	}
	if reverseCount != 0 {
		return domain.FXRate{}, fmt.Errorf("FX pair orientation is already established as %s/%s", fx.QuoteCurrency, fx.BaseCurrency)
	}
	result, insertErr := tx.ExecContext(ctx, `INSERT INTO fx_rates(id,base_currency,quote_currency,rate,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, fx.ID, fx.BaseCurrency, fx.QuoteCurrency, fx.Rate, fx.Source, fx.MarketAt, fx.FetchedAt, fx.Basis, fx.Provenance)
	if insertErr != nil {
		return domain.FXRate{}, fmt.Errorf("persist FX rate: %w", insertErr)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		existing, readErr := readExistingFX(ctx, tx, fx.ID)
		if readErr != nil {
			return domain.FXRate{}, fmt.Errorf("read existing FX rate: %w", readErr)
		}
		if !fxPersistenceEqual(fx, existing) {
			return domain.FXRate{}, fmt.Errorf("%w: FX rate %s", ErrObservationIDConflict, fx.ID)
		}
		if err = tx.Commit(); err != nil {
			return domain.FXRate{}, err
		}
		return fx, nil
	}
	payload, _ := json.Marshal(fx)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, deterministicID("audit", "fx", fx.ID), "administrative-fx", "market.fx.added", "fx_rate", fx.ID, payload, fetchedAt); err != nil {
		return domain.FXRate{}, fmt.Errorf("audit FX rate: %w", err)
	}
	auditID := deterministicID("audit", "fx", fx.ID)
	if err := s.recordActivityForPortfolios(ctx, tx, portfolioIDs, "administrative", "market.fx.added", "fx_rate", fx.ID, fetchedAt, auditID); err != nil {
		return domain.FXRate{}, fmt.Errorf("activity FX: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return domain.FXRate{}, err
	}
	return fx, nil
}

// persistQuoteTx and persistFXTx are used by refresh apply transactions. They
// deliberately do not open or commit a transaction; all source, audit, and
// activity rows therefore share one atomic boundary.
func (s *Store) persistQuoteTx(ctx context.Context, tx *sql.Tx, quote domain.Quote, provided []string) error {
	portfolioIDs, err := portfoliosForInstrument(ctx, tx, quote.InstrumentID)
	if err != nil {
		return fmt.Errorf("find quote portfolios: %w", err)
	}
	activityIDs := portfolioIDs
	if len(provided) > 0 {
		if err := validatePortfolioAssociationsSubset(provided, portfolioIDs); err != nil {
			return err
		}
		activityIDs = provided
	}
	if len(activityIDs) == 0 {
		return errors.New("no portfolio association")
	}
	if err := validatePortfolioAssociationsSubset(activityIDs, portfolioIDs); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO quotes(id,instrument_id,price,currency,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, quote.ID, quote.InstrumentID, quote.Price, quote.Currency, quote.Source, quote.MarketAt, quote.FetchedAt, quote.Basis, quote.Provenance)
	if err != nil {
		return fmt.Errorf("persist quote: %w", err)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		existing, readErr := readExistingQuote(ctx, tx, quote.ID)
		if readErr != nil {
			return fmt.Errorf("read existing quote: %w", readErr)
		}
		if !quotePersistenceEqual(quote, existing) {
			return fmt.Errorf("%w: quote %s", ErrObservationIDConflict, quote.ID)
		}
		return nil
	}
	payload, _ := json.Marshal(quote)
	auditID := deterministicID("audit", "quote", quote.ID)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, auditID, "administrative-quote", "market.quote.added", "quote", quote.ID, payload, quote.FetchedAt); err != nil {
		return fmt.Errorf("audit quote: %w", err)
	}
	if err := s.recordActivityForPortfolios(ctx, tx, activityIDs, "administrative", "market.quote.added", "quote", quote.ID, quote.FetchedAt, auditID); err != nil {
		return fmt.Errorf("activity quote: %w", err)
	}
	return nil
}

func (s *Store) persistFXTx(ctx context.Context, tx *sql.Tx, fx domain.FXRate, provided []string) error {
	portfolioIDs, err := portfoliosForFX(ctx, tx, fx.BaseCurrency, fx.QuoteCurrency)
	if err != nil {
		return fmt.Errorf("find FX portfolios: %w", err)
	}
	activityIDs := portfolioIDs
	if len(provided) > 0 {
		if err := validatePortfolioAssociationsSubset(provided, portfolioIDs); err != nil {
			return err
		}
		activityIDs = provided
	}
	if len(activityIDs) == 0 {
		return errors.New("no portfolio association")
	}
	if err := validatePortfolioAssociationsSubset(activityIDs, portfolioIDs); err != nil {
		return err
	}
	if existing, readErr := readExistingFX(ctx, tx, fx.ID); readErr == nil {
		if !fxPersistenceEqual(fx, existing) {
			return fmt.Errorf("%w: FX rate %s", ErrObservationIDConflict, fx.ID)
		}
		return nil
	} else if readErr != sql.ErrNoRows {
		return fmt.Errorf("read existing FX rate: %w", readErr)
	}
	var reverseCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fx_rates WHERE base_currency=? AND quote_currency=?`, fx.QuoteCurrency, fx.BaseCurrency).Scan(&reverseCount); err != nil {
		return err
	}
	if reverseCount != 0 {
		return fmt.Errorf("FX pair orientation is already established as %s/%s", fx.QuoteCurrency, fx.BaseCurrency)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO fx_rates(id,base_currency,quote_currency,rate,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, fx.ID, fx.BaseCurrency, fx.QuoteCurrency, fx.Rate, fx.Source, fx.MarketAt, fx.FetchedAt, fx.Basis, fx.Provenance)
	if err != nil {
		return fmt.Errorf("persist FX rate: %w", err)
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		existing, readErr := readExistingFX(ctx, tx, fx.ID)
		if readErr != nil {
			return fmt.Errorf("read existing FX rate: %w", readErr)
		}
		if !fxPersistenceEqual(fx, existing) {
			return fmt.Errorf("%w: FX rate %s", ErrObservationIDConflict, fx.ID)
		}
		return nil
	}
	payload, _ := json.Marshal(fx)
	auditID := deterministicID("audit", "fx", fx.ID)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, auditID, "administrative-fx", "market.fx.added", "fx_rate", fx.ID, payload, fx.FetchedAt); err != nil {
		return fmt.Errorf("audit FX rate: %w", err)
	}
	if err := s.recordActivityForPortfolios(ctx, tx, activityIDs, "administrative", "market.fx.added", "fx_rate", fx.ID, fx.FetchedAt, auditID); err != nil {
		return fmt.Errorf("activity FX: %w", err)
	}
	return nil
}

func (s *Store) loadValuationInput(ctx context.Context, portfolioID, reportingCurrency string, asOf time.Time, maxAge time.Duration) (domain.ValuationInput, error) {
	input := domain.ValuationInput{ReportingCurrency: reportingCurrency, AsOf: asOf, MaxAge: maxAge, Lots: []domain.ValuationLot{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT l.id,COALESCE((SELECT id FROM import_runs ir WHERE ir.source_sha256=l.source_sha256 AND ir.status='applied' ORDER BY ir.created_at DESC LIMIT 1),''),l.source_sha256,l.instrument_id,i.symbol,l.currency,l.quantity,l.unit_cost,l.cost_basis FROM lots l JOIN instruments i ON i.id=l.instrument_id WHERE l.portfolio_id = ? ORDER BY l.id`, portfolioID)
	if err != nil {
		return input, err
	}
	defer rows.Close()
	for rows.Next() {
		var lot domain.ValuationLot
		var unit, cost sql.NullString
		if err := rows.Scan(&lot.ID, &lot.ImportRunID, &lot.SourceSHA256, &lot.InstrumentID, &lot.Symbol, &lot.Currency, &lot.Quantity, &unit, &cost); err != nil {
			return input, err
		}
		if unit.Valid {
			lot.UnitCost = &unit.String
		}
		if cost.Valid {
			lot.CostBasis = &cost.String
		}
		input.Lots = append(input.Lots, lot)
	}
	if err := rows.Err(); err != nil {
		return input, err
	}
	quoteRows, err := s.DB.QueryContext(ctx, `SELECT q.id,q.instrument_id,i.symbol,q.price,q.currency,q.source,q.market_at,q.fetched_at,q.basis,q.provenance FROM quotes q JOIN instruments i ON i.id=q.instrument_id WHERE EXISTS (SELECT 1 FROM lots l WHERE l.portfolio_id = ? AND l.instrument_id = q.instrument_id) ORDER BY q.market_at DESC,q.fetched_at DESC`, portfolioID)
	if err != nil {
		return input, err
	}
	defer quoteRows.Close()
	for quoteRows.Next() {
		var q domain.Quote
		if err := quoteRows.Scan(&q.ID, &q.InstrumentID, &q.Symbol, &q.Price, &q.Currency, &q.Source, &q.MarketAt, &q.FetchedAt, &q.Basis, &q.Provenance); err != nil {
			return input, err
		}
		input.Quotes = append(input.Quotes, q)
	}
	if err := quoteRows.Err(); err != nil {
		return input, err
	}
	fxRows, err := s.DB.QueryContext(ctx, `SELECT id,base_currency,quote_currency,rate,source,market_at,fetched_at,basis,provenance FROM fx_rates ORDER BY market_at DESC,fetched_at DESC`)
	if err != nil {
		return input, err
	}
	defer fxRows.Close()
	for fxRows.Next() {
		var fx domain.FXRate
		if err := fxRows.Scan(&fx.ID, &fx.BaseCurrency, &fx.QuoteCurrency, &fx.Rate, &fx.Source, &fx.MarketAt, &fx.FetchedAt, &fx.Basis, &fx.Provenance); err != nil {
			return input, err
		}
		input.FXRates = append(input.FXRates, fx)
	}
	return input, fxRows.Err()
}

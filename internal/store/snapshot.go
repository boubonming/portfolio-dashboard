package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

type SnapshotRequest struct {
	PortfolioID        string
	ReportingCurrency  string
	CalculationVersion string
	AsOf               time.Time
	MaxAge             time.Duration
	receipt            *marketRefreshReceiptBinding
}

type snapshotPayload struct {
	SnapshotID         string                `json:"snapshot_id"`
	PortfolioID        string                `json:"portfolio_id"`
	CalculationVersion string                `json:"calculation_version"`
	CreatedAt          string                `json:"created_at"`
	AsOf               string                `json:"as_of"`
	MaxAge             time.Duration         `json:"max_age"`
	Input              domain.ValuationInput `json:"input"`
	Valuation          domain.Valuation      `json:"valuation"`
}

func (s *Store) CreateSnapshot(ctx context.Context, request SnapshotRequest) (domain.SnapshotStatus, error) {
	if request.PortfolioID == "" {
		request.PortfolioID = defaultPortfolioID
	}
	if request.ReportingCurrency == "" {
		request.ReportingCurrency = "MYR"
	}
	if request.CalculationVersion == "" {
		request.CalculationVersion = domain.CalculationVersion
	}
	if request.AsOf.IsZero() {
		request.AsOf = time.Now().UTC()
	} else {
		request.AsOf = request.AsOf.UTC()
	}
	if request.MaxAge <= 0 {
		request.MaxAge = domain.DefaultFreshness
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id = ?", request.PortfolioID).Scan(&exists); err != nil {
		return domain.SnapshotStatus{}, err
	}
	if exists == 0 {
		return domain.SnapshotStatus{}, fmt.Errorf("unknown portfolio %s", request.PortfolioID)
	}
	input, err := s.loadValuationInput(ctx, request.PortfolioID, request.ReportingCurrency, request.AsOf, request.MaxAge)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	if len(input.Lots) > maxSnapshotHoldings {
		return domain.SnapshotStatus{}, ErrSnapshotCorrupt
	}
	valuation, err := domain.Calculate(input)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	if !canonicalSnapshotLots(input.Lots, valuation.Lots) {
		return domain.SnapshotStatus{}, ErrSnapshotCorrupt
	}
	if len(input.Lots) > maxSnapshotHoldings {
		return domain.SnapshotStatus{}, ErrSnapshotCorrupt
	}
	digest, err := snapshotInputDigest(request.CalculationVersion, request.MaxAge, input)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	inputHash := hex.EncodeToString(digest[:])
	snapshotID := deterministicID("snapshot", request.PortfolioID, request.CalculationVersion, inputHash)
	createdAt := canonicalSnapshotTime(time.Now().UTC())
	payload := snapshotPayload{SnapshotID: snapshotID, PortfolioID: request.PortfolioID, CalculationVersion: request.CalculationVersion, CreatedAt: createdAt, AsOf: canonicalSnapshotTime(request.AsOf), MaxAge: request.MaxAge, Input: input, Valuation: valuation}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	defer tx.Rollback()
	createdSnapshot := true
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at,input_hash) VALUES(?,?,?,?,?,?)`, snapshotID, request.PortfolioID, request.CalculationVersion, encoded, createdAt, inputHash)
	if err != nil {
		if !isUniqueError(err) {
			return domain.SnapshotStatus{}, fmt.Errorf("persist snapshot: %w", err)
		}
		var existing []byte
		if readErr := tx.QueryRowContext(ctx, "SELECT payload FROM snapshots WHERE id = ?", snapshotID).Scan(&existing); readErr != nil {
			return domain.SnapshotStatus{}, readErr
		}
		encoded = existing
		createdSnapshot = false
	}
	if createdSnapshot {
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_snapshots(portfolio_id,snapshot_id,calculation_version) VALUES(?,?,?)`, request.PortfolioID, snapshotID, request.CalculationVersion); err != nil {
			return domain.SnapshotStatus{}, fmt.Errorf("persist review snapshot: %w", err)
		}
		for _, lot := range input.Lots {
			if _, err = tx.ExecContext(ctx, `INSERT INTO snapshot_holdings(portfolio_id,snapshot_id,holding_id,instrument_id) VALUES(?,?,?,?)`, request.PortfolioID, snapshotID, lot.ID, lot.InstrumentID); err != nil {
				return domain.SnapshotStatus{}, fmt.Errorf("persist snapshot holding: %w", err)
			}
		}
		auditID := deterministicID("audit", "snapshot", snapshotID)
		auditPayload, _ := json.Marshal(map[string]any{"complete": valuation.Complete, "calculation_version": request.CalculationVersion})
		auditActor, activityActor := "system-snapshot", "system"
		if request.receipt != nil {
			auditActor, activityActor = request.receipt.ActorID, request.receipt.ActorCategory
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, auditID, auditActor, "portfolio.snapshot.created", "snapshot", snapshotID, auditPayload, createdAt); err != nil {
			return domain.SnapshotStatus{}, fmt.Errorf("audit snapshot: %w", err)
		}
		if err = recordActivity(ctx, tx, request.PortfolioID, activityActor, "portfolio.snapshot.created", "portfolio", "snapshot", snapshotID, "Immutable snapshot created", "An immutable valuation snapshot was created.", createdAt, auditID); err != nil {
			return domain.SnapshotStatus{}, fmt.Errorf("activity snapshot: %w", err)
		}
	}
	auditID := deterministicID("audit", "snapshot", snapshotID)
	status, statusErr := statusFromPayload(encoded)
	if statusErr != nil {
		return domain.SnapshotStatus{}, statusErr
	}
	if request.receipt != nil {
		result := domain.MarketRefreshResult{
			Refresh:   request.receipt.Refresh,
			Snapshot:  status,
			Complete:  request.receipt.Refresh.Status == "complete" && status.Complete,
			Freshness: "quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero",
			Missing:   append([]string(nil), request.receipt.Missing...),
		}
		response, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return domain.SnapshotStatus{}, marshalErr
		}
		if err := s.commitMarketRefreshReceiptTx(ctx, tx, *request.receipt, result, response, snapshotID, auditID, deterministicID("activity", auditID, request.PortfolioID)); err != nil {
			return domain.SnapshotStatus{}, err
		}
	}
	if hookErr := s.marketRefreshFailure("before_snapshot_commit"); hookErr != nil {
		return domain.SnapshotStatus{}, hookErr
	}
	s.marketRefreshCommitFailure("before_snapshot_tx_commit")
	if err = tx.Commit(); err != nil {
		return domain.SnapshotStatus{}, err
	}
	if hookErr := s.marketRefreshFailure("after_snapshot_commit"); hookErr != nil {
		return domain.SnapshotStatus{}, hookErr
	}
	return status, nil
}

// snapshotInputDigest is the single canonical witness calculation for the
// immutable input persisted in a snapshot. Keep its inputs and JSON shape in
// sync with CreateSnapshot and reconstruction verification.
func snapshotInputDigest(calculationVersion string, maxAge time.Duration, input domain.ValuationInput) ([sha256.Size]byte, error) {
	hashInput, err := json.Marshal(struct {
		Version string                `json:"version"`
		MaxAge  time.Duration         `json:"max_age"`
		Input   domain.ValuationInput `json:"input"`
	}{calculationVersion, maxAge, input})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(hashInput), nil
}

func isUniqueError(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE") || contains(err.Error(), "constraint failed"))
}

func durationPointer(value time.Duration) *time.Duration { return &value }

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func statusFromPayload(encoded []byte) (domain.SnapshotStatus, error) {
	var payload snapshotPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return domain.SnapshotStatus{}, err
	}
	v := payload.Valuation
	currencies := make([]string, 0, len(v.Subtotals))
	for _, subtotal := range v.Subtotals {
		currencies = append(currencies, subtotal.Currency)
	}
	quoteByID := make(map[string]domain.Quote, len(payload.Input.Quotes))
	for _, quote := range payload.Input.Quotes {
		if existing, ok := quoteByID[quote.ID]; ok && !quotePersistenceEqual(existing, quote) {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		quoteByID[quote.ID] = quote
	}
	fxByID := make(map[string]domain.FXRate, len(payload.Input.FXRates))
	for _, fx := range payload.Input.FXRates {
		if existing, ok := fxByID[fx.ID]; ok && !fxPersistenceEqual(existing, fx) {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		fxByID[fx.ID] = fx
	}
	quoteObservations := make([]domain.QuoteObservation, 0, len(v.QuoteIDs))
	seenQuoteIDs := make(map[string]bool, len(v.QuoteIDs))
	for _, id := range v.QuoteIDs {
		if seenQuoteIDs[id] {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		quote, ok := quoteByID[id]
		if !ok {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		seenQuoteIDs[id] = true
		quoteObservations = append(quoteObservations, domain.QuoteObservation{ID: quote.ID, InstrumentID: quote.InstrumentID, Symbol: quote.Symbol, Currency: quote.Currency, Source: quote.Source})
	}
	fxObservations := make([]domain.FXObservation, 0, len(v.FXIDs))
	seenFXIDs := make(map[string]bool, len(v.FXIDs))
	for _, id := range v.FXIDs {
		if seenFXIDs[id] {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		fx, ok := fxByID[id]
		if !ok {
			return domain.SnapshotStatus{}, ErrSnapshotCorrupt
		}
		seenFXIDs[id] = true
		fxObservations = append(fxObservations, domain.FXObservation{ID: fx.ID, BaseCurrency: fx.BaseCurrency, QuoteCurrency: fx.QuoteCurrency, Source: fx.Source})
	}
	return domain.SnapshotStatus{SnapshotID: payload.SnapshotID, PortfolioID: payload.PortfolioID, CalculationVersion: payload.CalculationVersion, CreatedAt: payload.CreatedAt, AsOf: payload.AsOf, Complete: v.Complete, ReportingCurrency: v.ReportingCurrency, ReportingTotal: v.ReportingTotal, Currencies: currencies, Subtotals: v.Subtotals, MissingCount: len(v.MissingDependencies), StaleCount: len(v.StaleDependencies), FutureCount: len(v.FutureDependencies), InvalidCount: len(v.InvalidDependencies), MissingDependencies: v.MissingDependencies, StaleDependencies: v.StaleDependencies, FutureDependencies: v.FutureDependencies, InvalidDependencies: v.InvalidDependencies, QuoteIDs: v.QuoteIDs, FXIDs: v.FXIDs, QuoteObservations: quoteObservations, FXObservations: fxObservations, SourceTimestamps: v.SourceTimestamps, PresentationTimezone: v.PresentationTimezone}, nil
}

func (s *Store) SnapshotStatus(ctx context.Context, snapshotID string) (domain.SnapshotStatus, error) {
	var encoded []byte
	query := "SELECT payload FROM snapshots WHERE id = ?"
	if snapshotID == "" {
		query = "SELECT payload FROM snapshots ORDER BY created_at DESC LIMIT 1"
		if err := s.DB.QueryRowContext(ctx, query).Scan(&encoded); err != nil {
			if err == sql.ErrNoRows {
				return domain.SnapshotStatus{}, fmt.Errorf("no snapshots found")
			}
			return domain.SnapshotStatus{}, err
		}
	} else if err := s.DB.QueryRowContext(ctx, query, snapshotID).Scan(&encoded); err != nil {
		return domain.SnapshotStatus{}, err
	}
	return statusFromPayload(encoded)
}

// SnapshotShow returns aggregate status by default. Full persisted lot values
// are only returned when includeLots is explicitly true.
func (s *Store) SnapshotShow(ctx context.Context, snapshotID string, includeLots bool) (any, error) {
	var encoded []byte
	if err := s.DB.QueryRowContext(ctx, "SELECT payload FROM snapshots WHERE id = ?", snapshotID).Scan(&encoded); err != nil {
		return nil, err
	}
	if !includeLots {
		return statusFromPayload(encoded)
	}
	var payload snapshotPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

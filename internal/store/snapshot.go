package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

type SnapshotRequest struct {
	PortfolioID        string
	ReportingCurrency  string
	CalculationVersion string
	AsOf               time.Time
	MaxAge             time.Duration
}

type snapshotPayload struct {
	SnapshotID         string                `json:"snapshot_id"`
	PortfolioID        string                `json:"portfolio_id"`
	CalculationVersion string                `json:"calculation_version"`
	CreatedAt          string                `json:"created_at"`
	AsOf               string                `json:"as_of"`
	SourceHashes       []string              `json:"source_hashes"`
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
	valuation, err := domain.Calculate(input)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	hashInput, err := json.Marshal(struct {
		Version string                `json:"version"`
		Input   domain.ValuationInput `json:"input"`
	}{request.CalculationVersion, input})
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	digest := sha256.Sum256(hashInput)
	inputHash := hex.EncodeToString(digest[:])
	snapshotID := deterministicID("snapshot", request.PortfolioID, request.CalculationVersion, inputHash)
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	sourceHashes := []string{}
	seenSource := map[string]bool{}
	for _, lot := range input.Lots {
		if lot.SourceSHA256 != "" && !seenSource[lot.SourceSHA256] {
			sourceHashes = append(sourceHashes, lot.SourceSHA256)
			seenSource[lot.SourceSHA256] = true
		}
	}
	sort.Strings(sourceHashes)
	payload := snapshotPayload{SnapshotID: snapshotID, PortfolioID: request.PortfolioID, CalculationVersion: request.CalculationVersion, CreatedAt: createdAt, AsOf: request.AsOf.Format(time.RFC3339Nano), SourceHashes: sourceHashes, Input: input, Valuation: valuation}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.SnapshotStatus{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at,input_hash) VALUES(?,?,?,?,?,?)`, snapshotID, request.PortfolioID, request.CalculationVersion, encoded, createdAt, inputHash)
	if err != nil {
		if !isUniqueError(err) {
			return domain.SnapshotStatus{}, fmt.Errorf("persist snapshot: %w", err)
		}
		var existing []byte
		if readErr := tx.QueryRowContext(ctx, "SELECT payload FROM snapshots WHERE id = ?", snapshotID).Scan(&existing); readErr != nil {
			return domain.SnapshotStatus{}, readErr
		}
		return statusFromPayload(existing)
	}
	if err = tx.Commit(); err != nil {
		return domain.SnapshotStatus{}, err
	}
	return statusFromPayload(encoded)
}

func isUniqueError(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE") || contains(err.Error(), "constraint failed"))
}
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
	return domain.SnapshotStatus{SnapshotID: payload.SnapshotID, PortfolioID: payload.PortfolioID, CalculationVersion: payload.CalculationVersion, CreatedAt: payload.CreatedAt, AsOf: payload.AsOf, Complete: v.Complete, ReportingCurrency: v.ReportingCurrency, ReportingTotal: v.ReportingTotal, Currencies: currencies, Subtotals: v.Subtotals, MissingCount: len(v.MissingDependencies), StaleCount: len(v.StaleDependencies), InvalidCount: len(v.InvalidDependencies), QuoteIDs: v.QuoteIDs, FXIDs: v.FXIDs, SourceTimestamps: v.SourceTimestamps, PresentationTimezone: v.PresentationTimezone}, nil
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

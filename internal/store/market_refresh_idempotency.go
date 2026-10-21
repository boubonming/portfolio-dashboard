package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var (
	ErrMarketRefreshIdempotencyConflict = errors.New("market refresh idempotency conflict")
	ErrMarketRefreshInProgress          = errors.New("market refresh is already in progress")
)

type MarketRefreshIdempotencyOptions struct {
	ActorCategory      string
	ActorID            string
	PortfolioID        string
	IdempotencyKey     string
	RequestFingerprint string
	Refresh            RefreshOptions
	Snapshot           SnapshotRequest
	Missing            []string
}

type marketRefreshReceipt struct {
	responsePayload []byte
	state           string
	phase           string
	refreshPayload  []byte
	snapshotPayload []byte
}

// RefreshMarketDataIdempotent uses a durable phase record around the provider
// effect and snapshot effect. A retry after a process restart resumes from the
// last committed phase and never repeats an effect whose observation payload
// was recorded. Failed rows remain as recovery records rather than being
// deleted, while committed receipts are immutable at the database boundary.
func (s *Store) RefreshMarketDataIdempotent(ctx context.Context, options MarketRefreshIdempotencyOptions) (domain.MarketRefreshResult, bool, error) {
	if err := validateMarketRefreshIdempotency(options); err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	s.marketRefreshMu.Lock()
	defer s.marketRefreshMu.Unlock()

	receipt, err := s.readOrReserveMarketRefresh(ctx, options)
	if err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	if receipt.state == "committed" {
		var result domain.MarketRefreshResult
		if err := json.Unmarshal(receipt.responsePayload, &result); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode market refresh receipt: %w", err)
		}
		return result, true, nil
	}

	var refresh domain.RefreshResult
	if len(receipt.refreshPayload) != 0 {
		if err := json.Unmarshal(receipt.refreshPayload, &refresh); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode persisted refresh phase: %w", err)
		}
	} else {
		refresh, err = s.Refresh(ctx, options.Refresh)
		if err != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, err)
			return domain.MarketRefreshResult{}, false, err
		}
		encodedRefresh, marshalErr := json.Marshal(refresh)
		if marshalErr != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, marshalErr)
			return domain.MarketRefreshResult{}, false, marshalErr
		}
		if err := s.markMarketRefreshObserved(ctx, options, refresh.RefreshID, encodedRefresh); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
	}

	var snapshot domain.SnapshotStatus
	if len(receipt.snapshotPayload) != 0 {
		if err := json.Unmarshal(receipt.snapshotPayload, &snapshot); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode persisted snapshot phase: %w", err)
		}
	} else {
		snapshot, err = s.CreateSnapshot(ctx, options.Snapshot)
		if err != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, err)
			return domain.MarketRefreshResult{}, false, err
		}
		encodedSnapshot, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, marshalErr)
			return domain.MarketRefreshResult{}, false, marshalErr
		}
		if err := s.markMarketRefreshSnapshotted(ctx, options, snapshot.SnapshotID, encodedSnapshot); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
	}

	result := domain.MarketRefreshResult{
		Refresh:   refresh,
		Snapshot:  snapshot,
		Complete:  refresh.Status == "complete" && snapshot.Complete,
		Freshness: "quotes and FX must be no older than the configured 36h freshness window; incomplete data never becomes zero",
		Missing:   append([]string(nil), options.Missing...),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		_ = s.recordMarketRefreshFailure(ctx, options, err)
		return domain.MarketRefreshResult{}, false, err
	}
	if err := s.commitMarketRefreshReceipt(ctx, options, result, encoded); err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	return result, false, nil
}

func validateMarketRefreshIdempotency(options MarketRefreshIdempotencyOptions) error {
	if options.ActorCategory != "browser_user" && options.ActorCategory != "machine" {
		return errors.New("invalid market refresh actor category")
	}
	if options.ActorID == "" || len(options.ActorID) > 128 || !actorIDPattern.MatchString(options.ActorID) {
		return errors.New("invalid market refresh actor")
	}
	if options.PortfolioID == "" || len(options.IdempotencyKey) < 8 || len(options.IdempotencyKey) > 128 || len(options.RequestFingerprint) != 64 {
		return errors.New("invalid market refresh idempotency namespace")
	}
	return nil
}

func (s *Store) readOrReserveMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions) (marketRefreshReceipt, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return marketRefreshReceipt{}, err
	}
	defer tx.Rollback()
	var fingerprint, state, phase, payload string
	var refreshPayload, snapshotPayload, errorMessage sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,state,phase,response_payload,refresh_payload,snapshot_payload,error_message
		FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&fingerprint, &state, &phase, &payload, &refreshPayload, &snapshotPayload, &errorMessage)
	if err == nil {
		if fingerprint != options.RequestFingerprint {
			return marketRefreshReceipt{}, ErrMarketRefreshIdempotencyConflict
		}
		return marketRefreshReceipt{responsePayload: []byte(payload), state: state, phase: phase, refreshPayload: nullBytes(refreshPayload), snapshotPayload: nullBytes(snapshotPayload)}, nil
	}
	if err != sql.ErrNoRows {
		return marketRefreshReceipt{}, err
	}
	created := s.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO market_refresh_idempotency
		(actor_category,actor_id,portfolio_id,operation,idempotency_key,request_fingerprint,state,phase,response_payload,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, "pending", "reserved", `{}`, created); err != nil {
		if !isUniqueError(err) {
			return marketRefreshReceipt{}, err
		}
		_ = tx.Rollback()
		return s.readExistingMarketRefresh(ctx, options)
	}
	if err := tx.Commit(); err != nil {
		return marketRefreshReceipt{}, err
	}
	return marketRefreshReceipt{state: "pending", phase: "reserved"}, nil
}

func (s *Store) readExistingMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions) (marketRefreshReceipt, error) {
	var fingerprint, state, phase, payload string
	var refreshPayload, snapshotPayload, errorMessage sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT request_fingerprint,state,phase,response_payload,refresh_payload,snapshot_payload,error_message
		FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&fingerprint, &state, &phase, &payload, &refreshPayload, &snapshotPayload, &errorMessage)
	if err != nil {
		return marketRefreshReceipt{}, err
	}
	if fingerprint != options.RequestFingerprint {
		return marketRefreshReceipt{}, ErrMarketRefreshIdempotencyConflict
	}
	return marketRefreshReceipt{responsePayload: []byte(payload), state: state, phase: phase, refreshPayload: nullBytes(refreshPayload), snapshotPayload: nullBytes(snapshotPayload)}, nil
}

func nullBytes(value sql.NullString) []byte {
	if !value.Valid || value.String == "" {
		return nil
	}
	return []byte(value.String)
}

func (s *Store) markMarketRefreshObserved(ctx context.Context, options MarketRefreshIdempotencyOptions, refreshID string, payload []byte) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='observed',refresh_id=?,refresh_payload=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending'`,
		refreshID, payload, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint)
	return err
}

func (s *Store) markMarketRefreshSnapshotted(ctx context.Context, options MarketRefreshIdempotencyOptions, snapshotID string, payload []byte) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='snapshotted',snapshot_id=?,snapshot_payload=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending'`,
		snapshotID, payload, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint)
	return err
}

func (s *Store) recordMarketRefreshFailure(ctx context.Context, options MarketRefreshIdempotencyOptions, cause error) error {
	message := cause.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='failed',error_message=?
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending'`,
		message, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint)
	return err
}

func (s *Store) commitMarketRefreshReceipt(ctx context.Context, options MarketRefreshIdempotencyOptions, result domain.MarketRefreshResult, encoded []byte) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Format(time.RFC3339Nano)
	auditID := deterministicID("audit", "refresh", result.Refresh.RefreshID)
	activityID := deterministicID("activity", auditID, options.PortfolioID)
	update, err := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET state='committed',phase='committed',response_payload=?,refresh_id=?,snapshot_id=?,audit_event_id=?,activity_event_id=?,committed_at=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending'`,
		encoded, result.Refresh.RefreshID, result.Snapshot.SnapshotID, auditID, activityID, now,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint)
	if err != nil {
		return err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return ErrMarketRefreshInProgress
	}
	return tx.Commit()
}

var actorIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

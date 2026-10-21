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

var (
	ErrMarketRefreshIdempotencyConflict = errors.New("market refresh idempotency conflict")
	ErrMarketRefreshInProgress          = errors.New("market refresh is already in progress")
)

// MarketRefreshIdempotencyOptions describes the authority namespace and the
// complete effect set owned by one refresh receipt. The browser endpoint uses
// actorCategory=browser_user and actorID=local-owner; the authorization
// middleware remains the authority gate before this durable replay lookup.
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

// RefreshMarketDataIdempotent reserves the receipt before any provider call,
// then commits the response identity only after both refresh observations and
// the snapshot have committed. A process-local mutex only serializes the
// short state-machine transition; replay authority and response durability
// come from SQLite, so restarts cannot silently repeat a committed operation.
func (s *Store) RefreshMarketDataIdempotent(ctx context.Context, options MarketRefreshIdempotencyOptions) (domain.MarketRefreshResult, bool, error) {
	if err := validateMarketRefreshIdempotency(options); err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	s.marketRefreshMu.Lock()
	defer s.marketRefreshMu.Unlock()

	stored, state, err := s.readOrReserveMarketRefresh(ctx, options)
	if err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	if state == "committed" {
		var result domain.MarketRefreshResult
		if err := json.Unmarshal(stored, &result); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode market refresh receipt: %w", err)
		}
		return result, true, nil
	}
	if state == "pending" {
		return domain.MarketRefreshResult{}, false, ErrMarketRefreshInProgress
	}

	refresh, err := s.Refresh(ctx, options.Refresh)
	if err != nil {
		// No successful effect can be claimed when Refresh itself failed. Remove
		// the reservation so a caller can retry a transient provider failure.
		_ = s.deleteMarketRefreshReservation(ctx, options)
		return domain.MarketRefreshResult{}, false, err
	}
	snapshot, err := s.CreateSnapshot(ctx, options.Snapshot)
	if err != nil {
		// Refresh may already have committed its own observation transaction.
		// Keep the pending receipt rather than claiming success or re-running
		// provider calls against a partially completed operation.
		return domain.MarketRefreshResult{}, false, err
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
	if options.ActorID == "" || len(options.ActorID) > 128 || strings.ContainsAny(options.ActorID, "\r\n") {
		return errors.New("invalid market refresh actor")
	}
	if options.PortfolioID == "" || len(options.IdempotencyKey) < 8 || len(options.IdempotencyKey) > 128 || len(options.RequestFingerprint) != 64 {
		return errors.New("invalid market refresh idempotency namespace")
	}
	return nil
}

func (s *Store) readOrReserveMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions) ([]byte, string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var fingerprint, state, payload string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,state,response_payload
		FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&fingerprint, &state, &payload)
	if err == nil {
		if fingerprint != options.RequestFingerprint {
			return nil, "", ErrMarketRefreshIdempotencyConflict
		}
		return []byte(payload), state, nil
	}
	if err != sql.ErrNoRows {
		return nil, "", err
	}
	created := s.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO market_refresh_idempotency
		(actor_category,actor_id,portfolio_id,operation,idempotency_key,request_fingerprint,state,response_payload,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, "pending", `{}`, created); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return nil, "reserved", nil
}

func (s *Store) deleteMarketRefreshReservation(ctx context.Context, options MarketRefreshIdempotencyOptions) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM market_refresh_idempotency
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND state='pending'`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey)
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
	update, err := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET state='committed',response_payload=?,refresh_id=?,snapshot_id=?,audit_event_id=?,activity_event_id=?,committed_at=?
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

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var (
	ErrMarketRefreshIdempotencyConflict = errors.New("market refresh idempotency conflict")
	ErrMarketRefreshInProgress          = errors.New("market refresh is already in progress")
	ErrMarketRefreshCorrupt             = errors.New("market refresh receipt is corrupt")
)

const (
	marketRefreshLeaseDuration = 30 * time.Second
	marketRefreshWaitDuration  = 15 * time.Second
)

type MarketRefreshIdempotencyOptions struct {
	ActorCategory      string
	ActorID            string
	Permissions        []string
	PortfolioID        string
	IdempotencyKey     string
	RequestFingerprint string
	Refresh            RefreshOptions
	Snapshot           SnapshotRequest
	Missing            []string
	ownerToken         string
}

type marketRefreshReceipt struct {
	responsePayload []byte
	state           string
	phase           string
	refreshID       string
	snapshotID      string
	refreshPayload  []byte
	snapshotPayload []byte
	ownerToken      string
	leaseUntil      string
}

type marketRefreshReceiptBinding struct {
	ActorCategory      string
	ActorID            string
	Permissions        []string
	PortfolioID        string
	IdempotencyKey     string
	RequestFingerprint string
	Refresh            domain.RefreshResult
	Missing            []string
	ownerToken         string
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
	ownerToken, err := newMarketRefreshOwnerToken()
	if err != nil {
		return domain.MarketRefreshResult{}, false, err
	}
	options.ownerToken = ownerToken
	var receipt marketRefreshReceipt
	waitDeadline := time.Now().Add(marketRefreshWaitDuration)
	for {
		var owner bool
		receipt, owner, err = s.acquireOrReadMarketRefresh(ctx, options, ownerToken)
		if err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
		if receipt.state == "committed" || owner {
			break
		}
		if time.Now().After(waitDeadline) {
			return domain.MarketRefreshResult{}, false, ErrMarketRefreshInProgress
		}
		wait := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !wait.Stop() {
				<-wait.C
			}
			return domain.MarketRefreshResult{}, false, ctx.Err()
		case <-wait.C:
		}
	}
	if receipt.state == "committed" {
		var result domain.MarketRefreshResult
		if err := json.Unmarshal(receipt.responsePayload, &result); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode market refresh receipt: %w", err)
		}
		return result, true, nil
	}
	stopLease := make(chan struct{})
	leaseDone := make(chan struct{})
	go func() {
		defer close(leaseDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.renewMarketRefreshLease(ctx, options, ownerToken)
			case <-stopLease:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		close(stopLease)
		<-leaseDone
	}()
	if receipt.phase == "reserved" {
		if hookErr := s.marketRefreshFailure("after_reservation"); hookErr != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, hookErr)
			return domain.MarketRefreshResult{}, false, hookErr
		}
	}

	var refresh domain.RefreshResult
	if len(receipt.refreshPayload) != 0 {
		if err := json.Unmarshal(receipt.refreshPayload, &refresh); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode persisted refresh phase: %w", err)
		}
	} else {
		options.Refresh.RefreshID = receipt.refreshID
		options.Refresh.receipt = &marketRefreshReceiptBinding{ActorCategory: options.ActorCategory, ActorID: options.ActorID, Permissions: append([]string(nil), options.Permissions...), PortfolioID: options.PortfolioID, IdempotencyKey: options.IdempotencyKey, RequestFingerprint: options.RequestFingerprint, ownerToken: ownerToken}
		if err := s.renewMarketRefreshLease(ctx, options, ownerToken); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
		refresh, err = s.Refresh(ctx, options.Refresh)
		if err != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, err)
			return domain.MarketRefreshResult{}, false, err
		}
		// Refresh persists its observations and this phase transition in one
		// transaction. There is deliberately no second receipt commit here.
	}

	var snapshot domain.SnapshotStatus
	if len(receipt.snapshotPayload) != 0 {
		if err := json.Unmarshal(receipt.snapshotPayload, &snapshot); err != nil {
			return domain.MarketRefreshResult{}, false, fmt.Errorf("decode persisted snapshot phase: %w", err)
		}
	} else {
		options.Snapshot.receipt = &marketRefreshReceiptBinding{ActorCategory: options.ActorCategory, ActorID: options.ActorID, Permissions: append([]string(nil), options.Permissions...), PortfolioID: options.PortfolioID, IdempotencyKey: options.IdempotencyKey, RequestFingerprint: options.RequestFingerprint, ownerToken: ownerToken, Refresh: refresh, Missing: options.Missing}
		if err := s.renewMarketRefreshLease(ctx, options, ownerToken); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
		snapshot, err = s.CreateSnapshot(ctx, options.Snapshot)
		if err != nil {
			_ = s.recordMarketRefreshFailure(ctx, options, err)
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
	// New operations commit the final receipt in CreateSnapshot's transaction.
	// Keep this fallback for receipts created by an older schema revision that
	// already has a persisted snapshot phase but no committed replay payload.
	if receipt.snapshotPayload != nil && receipt.state != "committed" {
		if err := s.commitMarketRefreshReceipt(ctx, options, result, encoded); err != nil {
			return domain.MarketRefreshResult{}, false, err
		}
	}
	return result, false, nil
}

func validateMarketRefreshIdempotency(options MarketRefreshIdempotencyOptions) error {
	if options.ActorCategory != "browser_user" && options.ActorCategory != "machine" && options.ActorCategory != "scheduler" && options.ActorCategory != "cli" {
		return errors.New("invalid market refresh actor category")
	}
	if options.ActorID == "" || len(options.ActorID) > 128 || !actorIDPattern.MatchString(options.ActorID) {
		return errors.New("invalid market refresh actor")
	}
	if options.ActorCategory == "machine" && (len(options.Permissions) != 2 || options.Permissions[0] != "portfolio:read" || options.Permissions[1] != "market_data:refresh") {
		return errors.New("invalid market refresh machine permissions")
	}
	if options.ActorCategory == "scheduler" && (options.ActorID != "market-refresh-scheduler" || len(options.Permissions) != 1 || options.Permissions[0] != "market_data:refresh") {
		return errors.New("invalid market refresh scheduler actor")
	}
	if options.ActorCategory == "cli" && (options.ActorID != "market-refresh-cli" || len(options.Permissions) != 1 || options.Permissions[0] != "market_data:refresh") {
		return errors.New("invalid market refresh cli actor")
	}
	if options.PortfolioID == "" || len(options.IdempotencyKey) < 8 || len(options.IdempotencyKey) > 128 || len(options.RequestFingerprint) != 64 {
		return errors.New("invalid market refresh idempotency namespace")
	}
	return nil
}

func newMarketRefreshOwnerToken() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate market refresh owner token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func marketRefreshLeaseExpired(value string, now time.Time) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	lease, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || !lease.After(now.UTC())
}

func formatMarketRefreshTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func marketRefreshSnapshotExists(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, snapshotID string) bool {
	var count int
	return query.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshots WHERE id=?", snapshotID).Scan(&count) == nil && count == 1
}

// readOrReserveMarketRefresh is retained as a small test and migration helper.
// The composed workflow uses acquireOrReadMarketRefresh, which reports durable
// ownership separately and never treats a live owner as an invitation to run.
func (s *Store) readOrReserveMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions) (marketRefreshReceipt, error) {
	ownerToken, err := newMarketRefreshOwnerToken()
	if err != nil {
		return marketRefreshReceipt{}, err
	}
	receipt, _, err := s.acquireOrReadMarketRefresh(ctx, options, ownerToken)
	return receipt, err
}

func (s *Store) acquireOrReadMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions, ownerToken string) (marketRefreshReceipt, bool, error) {
	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		receipt, owner, err := s.readOrReserveMarketRefreshOnce(ctx, options, ownerToken)
		// SQLite can report a unique conflict against a concurrent writer
		// before that writer's committed row is visible to this connection.
		// Treat the transient no-row observation as contention and retry.
		if errors.Is(err, sql.ErrNoRows) && attempt < maxAttempts-1 {
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return marketRefreshReceipt{}, false, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if err == nil || !isSQLiteBusy(err) || attempt == maxAttempts-1 {
			if attempt == maxAttempts-1 && isSQLiteBusy(err) {
				return marketRefreshReceipt{}, false, ErrMarketRefreshInProgress
			}
			return receipt, owner, err
		}
		backoff := time.Duration(attempt+1) * 10 * time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return marketRefreshReceipt{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return marketRefreshReceipt{}, false, ctx.Err()
}

func (s *Store) readOrReserveMarketRefreshOnce(ctx context.Context, options MarketRefreshIdempotencyOptions, ownerToken string) (marketRefreshReceipt, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return marketRefreshReceipt{}, false, err
	}
	defer tx.Rollback()
	var fingerprint, state, phase, payload string
	var refreshID, snapshotID, refreshPayload, snapshotPayload, errorMessage, existingOwner, leaseUntil sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,state,phase,response_payload,refresh_id,snapshot_id,refresh_payload,snapshot_payload,error_message
		FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&fingerprint, &state, &phase, &payload, &refreshID, &snapshotID, &refreshPayload, &snapshotPayload, &errorMessage)
	if err == nil {
		if fingerprint != options.RequestFingerprint {
			return marketRefreshReceipt{}, false, ErrMarketRefreshIdempotencyConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT owner_token,lease_until FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&existingOwner, &leaseUntil); err != nil {
			return marketRefreshReceipt{}, false, err
		}
		receipt := marketRefreshReceipt{responsePayload: []byte(payload), state: state, phase: phase, refreshID: nullStringValue(refreshID), snapshotID: nullStringValue(snapshotID), refreshPayload: nullBytes(refreshPayload), snapshotPayload: nullBytes(snapshotPayload), ownerToken: nullStringValue(existingOwner), leaseUntil: nullStringValue(leaseUntil)}
		if state == "committed" {
			if receipt.snapshotID == "" || !marketRefreshSnapshotExists(ctx, tx, receipt.snapshotID) {
				return marketRefreshReceipt{}, false, ErrMarketRefreshCorrupt
			}
			return receipt, false, nil
		}
		if receipt.ownerToken == ownerToken {
			return receipt, true, nil
		}
		if !marketRefreshLeaseExpired(receipt.leaseUntil, s.now()) {
			return receipt, false, nil
		}
		newLease := formatMarketRefreshTimestamp(s.now().Add(marketRefreshLeaseDuration))
		update, updateErr := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET owner_token=?,lease_until=?,phase=CASE WHEN phase='failed' AND refresh_payload IS NULL THEN 'reserved' WHEN phase='failed' AND refresh_payload IS NOT NULL THEN 'observed' ELSE phase END WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND state='pending' AND (lease_until IS NULL OR lease_until<=?)`, ownerToken, newLease, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, formatMarketRefreshTimestamp(s.now()))
		if updateErr != nil {
			return marketRefreshReceipt{}, false, updateErr
		}
		affected, _ := update.RowsAffected()
		if affected != 1 {
			if commitErr := tx.Commit(); commitErr != nil {
				return marketRefreshReceipt{}, false, commitErr
			}
			return receipt, false, nil
		}
		if err := tx.Commit(); err != nil {
			return marketRefreshReceipt{}, false, err
		}
		receipt.ownerToken, receipt.leaseUntil = ownerToken, newLease
		return receipt, true, nil
	}
	if err != sql.ErrNoRows {
		return marketRefreshReceipt{}, false, err
	}
	created := formatMarketRefreshTimestamp(s.now())
	leaseUntilText := formatMarketRefreshTimestamp(s.now().Add(marketRefreshLeaseDuration))
	if _, err := tx.ExecContext(ctx, `INSERT INTO market_refresh_idempotency
		(actor_category,actor_id,portfolio_id,operation,idempotency_key,request_fingerprint,state,phase,response_payload,refresh_id,permission_market_data_refresh,created_at,owner_token,lease_until)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, "pending", "reserved", `{}`, deterministicID("refresh", options.ActorCategory, options.ActorID, options.PortfolioID, options.IdempotencyKey), boolInt(options.ActorCategory == "machine" || options.ActorCategory == "scheduler" || options.ActorCategory == "cli"), created, ownerToken, leaseUntilText); err != nil {
		if !isUniqueError(err) {
			return marketRefreshReceipt{}, false, err
		}
		_ = tx.Rollback()
		return s.readExistingMarketRefresh(ctx, options, ownerToken)
	}
	if err := tx.Commit(); err != nil {
		return marketRefreshReceipt{}, false, err
	}
	return marketRefreshReceipt{state: "pending", phase: "reserved", refreshID: deterministicID("refresh", options.ActorCategory, options.ActorID, options.PortfolioID, options.IdempotencyKey), ownerToken: ownerToken, leaseUntil: leaseUntilText}, true, nil
}

func (s *Store) readExistingMarketRefresh(ctx context.Context, options MarketRefreshIdempotencyOptions, ownerToken string) (marketRefreshReceipt, bool, error) {
	var fingerprint, state, phase, payload string
	var refreshID, snapshotID, refreshPayload, snapshotPayload, errorMessage, existingOwner, leaseUntil sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT request_fingerprint,state,phase,response_payload,refresh_id,snapshot_id,refresh_payload,snapshot_payload,error_message
		FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&fingerprint, &state, &phase, &payload, &refreshID, &snapshotID, &refreshPayload, &snapshotPayload, &errorMessage)
	if err != nil {
		return marketRefreshReceipt{}, false, err
	}
	if fingerprint != options.RequestFingerprint {
		return marketRefreshReceipt{}, false, ErrMarketRefreshIdempotencyConflict
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT owner_token,lease_until FROM market_refresh_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey).Scan(&existingOwner, &leaseUntil); err != nil {
		return marketRefreshReceipt{}, false, err
	}
	receipt := marketRefreshReceipt{responsePayload: []byte(payload), state: state, phase: phase, refreshID: nullStringValue(refreshID), snapshotID: nullStringValue(snapshotID), refreshPayload: nullBytes(refreshPayload), snapshotPayload: nullBytes(snapshotPayload), ownerToken: nullStringValue(existingOwner), leaseUntil: nullStringValue(leaseUntil)}
	if state == "committed" && (receipt.snapshotID == "" || !marketRefreshSnapshotExists(ctx, s.DB, receipt.snapshotID)) {
		return marketRefreshReceipt{}, false, ErrMarketRefreshCorrupt
	}
	return receipt, state != "committed" && (receipt.ownerToken == ownerToken || marketRefreshLeaseExpired(receipt.leaseUntil, s.now())), nil
}

func (s *Store) renewMarketRefreshLease(ctx context.Context, options MarketRefreshIdempotencyOptions, ownerToken string) error {
	leaseUntil := formatMarketRefreshTimestamp(s.now().Add(marketRefreshLeaseDuration))
	result, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET lease_until=?
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?
		AND request_fingerprint=? AND state='pending' AND owner_token=? AND lease_until>?`, leaseUntil,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, ownerToken, formatMarketRefreshTimestamp(s.now()))
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return ErrMarketRefreshInProgress
	}
	return nil
}

func nullBytes(value sql.NullString) []byte {
	if !value.Valid || value.String == "" {
		return nil
	}
	return []byte(value.String)
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func (s *Store) markMarketRefreshObserved(ctx context.Context, options MarketRefreshIdempotencyOptions, refreshID string, payload []byte) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='observed',refresh_id=?,refresh_payload=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND owner_token=?`,
		refreshID, payload, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, options.ownerToken)
	return err
}

func (s *Store) markMarketRefreshSnapshotted(ctx context.Context, options MarketRefreshIdempotencyOptions, snapshotID string, payload []byte) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='snapshotted',snapshot_id=?,snapshot_payload=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND owner_token=?`,
		snapshotID, payload, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, options.ownerToken)
	return err
}

func updateMarketRefreshObservedTx(ctx context.Context, tx *sql.Tx, binding marketRefreshReceiptBinding, refreshID string, payload []byte) error {
	update, err := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='observed',refresh_id=?,refresh_payload=?,error_message=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND phase='reserved' AND owner_token=?`,
		refreshID, payload, binding.ActorCategory, binding.ActorID, binding.PortfolioID, "refresh", binding.IdempotencyKey, binding.RequestFingerprint, binding.ownerToken)
	if err != nil {
		return err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return ErrMarketRefreshInProgress
	}
	return nil
}

func (s *Store) recordMarketRefreshFailure(ctx context.Context, options MarketRefreshIdempotencyOptions, cause error) error {
	message := cause.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE market_refresh_idempotency SET phase='failed',error_message=?,owner_token=NULL,lease_until=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND owner_token=?`,
		message, options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, options.ownerToken)
	return err
}

func (s *Store) commitMarketRefreshReceipt(ctx context.Context, options MarketRefreshIdempotencyOptions, result domain.MarketRefreshResult, encoded []byte) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatMarketRefreshTimestamp(s.now())
	auditID := deterministicID("audit", "refresh", result.Refresh.RefreshID)
	activityID := deterministicID("activity", auditID, options.PortfolioID)
	snapshotPayload, marshalErr := json.Marshal(result.Snapshot)
	if marshalErr != nil {
		return marshalErr
	}
	update, err := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET state='committed',phase='committed',response_payload=?,refresh_id=?,snapshot_id=?,snapshot_payload=?,audit_event_id=?,activity_event_id=?,committed_at=?,error_message=NULL,owner_token=NULL,lease_until=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND owner_token=?`,
		encoded, result.Refresh.RefreshID, result.Snapshot.SnapshotID, snapshotPayload, auditID, activityID, now,
		options.ActorCategory, options.ActorID, options.PortfolioID, "refresh", options.IdempotencyKey, options.RequestFingerprint, options.ownerToken)
	if err != nil {
		return err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return ErrMarketRefreshInProgress
	}
	return tx.Commit()
}

func (s *Store) commitMarketRefreshReceiptTx(ctx context.Context, tx *sql.Tx, binding marketRefreshReceiptBinding, result domain.MarketRefreshResult, encoded []byte, snapshotID, auditID, activityID string) error {
	now := formatMarketRefreshTimestamp(s.now())
	snapshotPayload, marshalErr := json.Marshal(result.Snapshot)
	if marshalErr != nil {
		return marshalErr
	}
	update, err := tx.ExecContext(ctx, `UPDATE market_refresh_idempotency SET state='committed',phase='committed',response_payload=?,refresh_id=?,snapshot_id=?,snapshot_payload=?,audit_event_id=?,activity_event_id=?,committed_at=?,error_message=NULL,owner_token=NULL,lease_until=NULL
		WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=? AND request_fingerprint=? AND state='pending' AND phase='observed' AND owner_token=?`,
		encoded, result.Refresh.RefreshID, snapshotID, snapshotPayload, auditID, activityID, now,
		binding.ActorCategory, binding.ActorID, binding.PortfolioID, "refresh", binding.IdempotencyKey, binding.RequestFingerprint, binding.ownerToken)
	if err != nil {
		return err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return ErrMarketRefreshInProgress
	}
	return nil
}

var actorIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

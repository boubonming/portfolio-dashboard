package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var (
	ErrInvalidHoldingMutation = errors.New("invalid holding mutation")
	ErrStaleRevision          = errors.New("stale revision")
	ErrIdempotencyConflict    = errors.New("idempotency conflict")
	ErrHoldingNotActive       = errors.New("holding is not active")
	ErrInstrumentNotFound     = errors.New("instrument not found")
	ErrInstrumentConflict     = errors.New("instrument conflict")
	ErrAccountNotFound        = errors.New("account not found")
	ErrMutationUnauthorized   = errors.New("mutation unauthorized")
)

var mutationActorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)

type MutationActor struct {
	Category    string
	ID          string
	Permissions []string
}

type InlineInstrument struct {
	Symbol      string
	Description string
	Currency    string
	AssetType   string
	Exchange    string
}

type HoldingCreateParams struct {
	PortfolioID        string
	IfMatchRevision    int
	IdempotencyKey     string
	RequestFingerprint string
	Actor              MutationActor
	InstrumentID       string
	InlineInstrument   *InlineInstrument
	Quantity           string
	UnitCost           string
	CostBasis          string
	Currency           string
	AccountID          *string
	AcquisitionDate    *string
}

type HoldingRemoveParams struct {
	PortfolioID        string
	HoldingID          string
	IfMatchRevision    int
	IdempotencyKey     string
	RequestFingerprint string
	Actor              MutationActor
}

type HoldingMutationResult struct {
	Receipt  domain.HoldingMutationReceipt
	Replayed bool
}

func (s *Store) CreateHolding(ctx context.Context, params HoldingCreateParams) (HoldingMutationResult, error) {
	if err := validateMutationParams(params.PortfolioID, params.IdempotencyKey, params.RequestFingerprint, params.Actor); err != nil {
		return HoldingMutationResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	defer tx.Rollback()
	if err := verifyMutationActor(ctx, tx, params.Actor, params.PortfolioID); err != nil {
		return HoldingMutationResult{}, err
	}
	if result, found, err := replayHoldingMutation(ctx, tx, params.Actor, params.PortfolioID, "create", params.IdempotencyKey, params.RequestFingerprint); err != nil {
		return HoldingMutationResult{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return HoldingMutationResult{}, err
		}
		result.Replayed = true
		return result, nil
	}
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT holdings_revision FROM portfolios WHERE id=?`, params.PortfolioID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HoldingMutationResult{}, ErrPortfolioNotFound
		}
		return HoldingMutationResult{}, err
	}
	if revision != params.IfMatchRevision {
		return HoldingMutationResult{}, ErrStaleRevision
	}
	instrumentID, symbol, description, instrumentCurrency, err := s.resolveMutationInstrument(ctx, tx, params)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	if params.Currency != instrumentCurrency {
		return HoldingMutationResult{}, fmt.Errorf("%w: currency mismatch", ErrInvalidHoldingMutation)
	}
	if params.AccountID != nil {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=? AND portfolio_id=?`, *params.AccountID, params.PortfolioID).Scan(&count); err != nil {
			return HoldingMutationResult{}, err
		}
		if count == 0 {
			return HoldingMutationResult{}, ErrAccountNotFound
		}
	}
	now := canonicalSnapshotTime(s.now())
	lotID, err := mutationID("lot")
	if err != nil {
		return HoldingMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,acquisition_date,created_at,lifecycle,created_at_order_key) VALUES(?,?,?,?,?,?,?,?,?,?, 'active',?)`, lotID, params.PortfolioID, instrumentID, nullableString(params.AccountID), params.Quantity, params.UnitCost, params.CostBasis, params.Currency, nullableString(params.AcquisitionDate), now, now); err != nil {
		return HoldingMutationResult{}, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE portfolios SET holdings_revision=holdings_revision+1 WHERE id=? AND holdings_revision=?`, params.PortfolioID, revision)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	changed, err := update.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return HoldingMutationResult{}, err
		}
		return HoldingMutationResult{}, ErrStaleRevision
	}
	newRevision := revision + 1
	receipt, err := s.finishHoldingMutation(ctx, tx, "create", params.PortfolioID, lotID, instrumentID, symbol, description, params.Quantity, params.UnitCost, params.CostBasis, params.Currency, params.AccountID, params.AcquisitionDate, now, nil, params.Actor, params.IdempotencyKey, newRevision, params.RequestFingerprint)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return HoldingMutationResult{}, err
	}
	return HoldingMutationResult{Receipt: receipt}, nil
}

func (s *Store) RemoveHolding(ctx context.Context, params HoldingRemoveParams) (HoldingMutationResult, error) {
	if err := validateMutationParams(params.PortfolioID, params.IdempotencyKey, params.RequestFingerprint, params.Actor); err != nil {
		return HoldingMutationResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	defer tx.Rollback()
	if err := verifyMutationActor(ctx, tx, params.Actor, params.PortfolioID); err != nil {
		return HoldingMutationResult{}, err
	}
	if result, found, err := replayHoldingMutation(ctx, tx, params.Actor, params.PortfolioID, "remove", params.IdempotencyKey, params.RequestFingerprint); err != nil {
		return HoldingMutationResult{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return HoldingMutationResult{}, err
		}
		result.Replayed = true
		return result, nil
	}
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT holdings_revision FROM portfolios WHERE id=?`, params.PortfolioID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HoldingMutationResult{}, ErrPortfolioNotFound
		}
		return HoldingMutationResult{}, err
	}
	if revision != params.IfMatchRevision {
		return HoldingMutationResult{}, ErrStaleRevision
	}
	var instrumentID, symbol, description, quantity, unitCost, costBasis, currency, createdAt, orderKey string
	var accountID, acquisitionDate sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT l.instrument_id,i.symbol,COALESCE(i.description,''),l.quantity,COALESCE(l.unit_cost,''),COALESCE(l.cost_basis,''),l.currency,l.account_id,l.acquisition_date,l.created_at,l.created_at_order_key FROM lots l JOIN instruments i ON i.id=l.instrument_id WHERE l.id=? AND l.portfolio_id=? AND l.lifecycle='active'`, params.HoldingID, params.PortfolioID).Scan(&instrumentID, &symbol, &description, &quantity, &unitCost, &costBasis, &currency, &accountID, &acquisitionDate, &createdAt, &orderKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			var lifecycle string
			err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM lots WHERE id=? AND portfolio_id=?`, params.HoldingID, params.PortfolioID).Scan(&lifecycle)
			if err == nil && lifecycle == "removed" {
				return HoldingMutationResult{}, ErrHoldingNotActive
			}
			return HoldingMutationResult{}, ErrHoldingNotFound
		}
		return HoldingMutationResult{}, err
	}
	now := canonicalSnapshotTime(s.now())
	if orderKey == "" || now < orderKey {
		return HoldingMutationResult{}, fmt.Errorf("%w: legacy holding timestamp cannot be archived", ErrInvalidHoldingMutation)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE lots SET lifecycle='removed',removed_at=?,removed_by_category=?,removed_by_id=? WHERE id=? AND portfolio_id=? AND lifecycle='active'`, now, params.Actor.Category, params.Actor.ID, params.HoldingID, params.PortfolioID); err != nil {
		return HoldingMutationResult{}, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE portfolios SET holdings_revision=holdings_revision+1 WHERE id=? AND holdings_revision=?`, params.PortfolioID, revision)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	changed, err := update.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return HoldingMutationResult{}, err
		}
		return HoldingMutationResult{}, ErrStaleRevision
	}
	newRevision := revision + 1
	var accountPtr, datePtr, removedPtr *string
	if accountID.Valid {
		accountPtr = &accountID.String
	}
	if acquisitionDate.Valid {
		datePtr = &acquisitionDate.String
	}
	removedPtr = &now
	receipt, err := s.finishHoldingMutation(ctx, tx, "remove", params.PortfolioID, params.HoldingID, instrumentID, symbol, description, quantity, unitCost, costBasis, currency, accountPtr, datePtr, createdAt, removedPtr, params.Actor, params.IdempotencyKey, newRevision, params.RequestFingerprint)
	if err != nil {
		return HoldingMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return HoldingMutationResult{}, err
	}
	return HoldingMutationResult{Receipt: receipt}, nil
}

func validateMutationParams(portfolioID, key, fingerprint string, actor MutationActor) error {
	if !mutationActorID.MatchString(portfolioID) || len(portfolioID) > 128 || len(key) < 16 || len(key) > 128 || !mutationActorID.MatchString(key) || len(fingerprint) != sha256.Size*2 {
		return ErrInvalidHoldingMutation
	}
	if actor.Category != "browser_user" && actor.Category != "machine" || len(actor.ID) < 1 || len(actor.ID) > 128 || !mutationActorID.MatchString(actor.ID) {
		return ErrInvalidHoldingMutation
	}
	if actor.Category == "browser_user" && (actor.ID != "local-owner" || len(actor.Permissions) != 1 || actor.Permissions[0] != "portfolio:read") {
		return ErrInvalidHoldingMutation
	}
	if actor.Category == "machine" && (len(actor.Permissions) != 2 || actor.Permissions[0] != "portfolio:read" || actor.Permissions[1] != "holdings:write") {
		return ErrInvalidHoldingMutation
	}
	return nil
}

func verifyMutationActor(ctx context.Context, tx *sql.Tx, actor MutationActor, portfolioID string) error {
	if actor.Category == "browser_user" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_principals WHERE id=?`, actor.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 || actor.ID != "local-owner" {
			return ErrMutationUnauthorized
		}
		return nil
	}
	var active, read, write int
	if err := tx.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_holdings_write FROM machine_principals WHERE id=? AND portfolio_id=?`, actor.ID, portfolioID).Scan(&active, &read, &write); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMutationUnauthorized
		}
		return err
	}
	if active != 1 || read != 1 || write != 1 {
		return ErrMutationUnauthorized
	}
	return nil
}

func (s *Store) resolveMutationInstrument(ctx context.Context, tx *sql.Tx, params HoldingCreateParams) (string, string, string, string, error) {
	if (params.InstrumentID == "") == (params.InlineInstrument == nil) {
		return "", "", "", "", fmt.Errorf("%w: instrument must be exactly one form", ErrInvalidHoldingMutation)
	}
	if params.InlineInstrument != nil {
		inline := params.InlineInstrument
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM instruments WHERE symbol=? AND currency=? LIMIT 1`, inline.Symbol, inline.Currency).Scan(&existing)
		if err == nil {
			return "", "", "", "", ErrInstrumentConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", "", err
		}
		id, err := mutationID("instrument")
		if err != nil {
			return "", "", "", "", err
		}
		createdAt := canonicalSnapshotTime(s.now())
		if _, err := tx.ExecContext(ctx, `INSERT INTO instruments(id,symbol,description,currency,asset_type,exchange,created_at) VALUES(?,?,?,?,?,?,?)`, id, inline.Symbol, nullableText(inline.Description), inline.Currency, nullableText(inline.AssetType), nullableText(inline.Exchange), createdAt); err != nil {
			return "", "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, params.PortfolioID, id, createdAt); err != nil {
			return "", "", "", "", err
		}
		return id, inline.Symbol, inline.Description, inline.Currency, nil
	}
	var symbol, description, currency string
	if err := tx.QueryRowContext(ctx, `SELECT i.symbol,COALESCE(i.description,''),i.currency FROM instruments i JOIN portfolio_instruments p ON p.instrument_id=i.id WHERE p.portfolio_id=? AND p.instrument_id=?`, params.PortfolioID, params.InstrumentID).Scan(&symbol, &description, &currency); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", "", ErrInstrumentNotFound
		}
		return "", "", "", "", err
	}
	return params.InstrumentID, symbol, description, currency, nil
}

func (s *Store) finishHoldingMutation(ctx context.Context, tx *sql.Tx, operation, portfolioID, holdingID, instrumentID, symbol, description, quantity, unitCost, costBasis, currency string, accountID, acquisitionDate *string, createdAt string, removedAt *string, actor MutationActor, key string, revision int, fingerprint string) (domain.HoldingMutationReceipt, error) {
	auditID, err := mutationID("audit")
	if err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	activityID, err := mutationID("activity")
	if err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	occurredAt := createdAt
	if operation == "remove" && removedAt != nil {
		occurredAt = *removedAt
	}
	retainUntil := canonicalSnapshotTime(s.now().AddDate(7, 0, 0))
	action := "holding_created"
	if operation == "remove" {
		action = "holding_removed"
	}
	payloadBytes, err := json.Marshal(map[string]any{"actor_category": actor.Category, "actor_id": actor.ID, "holding_id": holdingID, "operation": operation, "occurred_at": occurredAt, "portfolio_id": portfolioID, "permissions": actor.Permissions})
	if err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, actor.ID, action, "holding", holdingID, string(payloadBytes), occurredAt, retainUntil); err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, auditID, portfolioID, retainUntil); err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	summary := "Holding created"
	if operation == "remove" {
		summary = "Holding archived"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,sort_time,source_audit_event_id,retain_until) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, activityID, portfolioID, actor.Category, action, "holding", "holding", holdingID, summary, "", occurredAt, occurredAt, auditID, retainUntil); err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	account := accountID
	date := acquisitionDate
	receipt := domain.HoldingMutationReceipt{Operation: operation, Result: "applied", Holding: domain.HoldingRecord{PortfolioID: portfolioID, HoldingID: holdingID, InstrumentID: instrumentID, Symbol: symbol, Description: description, Quantity: quantity, UnitCost: unitCost, CostBasis: costBasis, Currency: currency, AccountID: account, AcquisitionDate: date, Lifecycle: "active", CreatedAt: createdAt, RemovedAt: removedAt}, PortfolioRevision: revision, SnapshotState: "no_snapshot", Actor: domain.HoldingMutationActor{Category: actor.Category, ID: actor.ID, Permissions: append([]string(nil), actor.Permissions...)}, AuditEventID: auditID, ActivityEventID: activityID, IdempotencyKey: key}
	if operation == "remove" {
		receipt.Holding.Lifecycle = "removed"
	}
	var snapshotID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM snapshots WHERE portfolio_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, portfolioID).Scan(&snapshotID); err == nil {
		receipt.SnapshotState = "stale"
		receipt.CurrentSnapshotID = &snapshotID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.HoldingMutationReceipt{}, err
	}
	response, err := json.Marshal(receipt)
	if err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO holding_mutation_idempotency(actor_category,actor_id,portfolio_id,operation,idempotency_key,request_fingerprint,holding_id,audit_event_id,activity_event_id,permission_portfolio_read,permission_holdings_write,response_payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, actor.Category, actor.ID, portfolioID, operation, key, fingerprint, holdingID, auditID, activityID, 1, boolInt(actor.Category == "machine"), string(response), occurredAt, retainUntil); err != nil {
		return domain.HoldingMutationReceipt{}, err
	}
	return receipt, nil
}

func replayHoldingMutation(ctx context.Context, tx *sql.Tx, actor MutationActor, portfolioID, operation, key, fingerprint string) (HoldingMutationResult, bool, error) {
	var storedFingerprint, payload string
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint,response_payload FROM holding_mutation_idempotency WHERE actor_category=? AND actor_id=? AND portfolio_id=? AND operation=? AND idempotency_key=?`, actor.Category, actor.ID, portfolioID, operation, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return HoldingMutationResult{}, false, nil
	}
	if err != nil {
		return HoldingMutationResult{}, false, err
	}
	if storedFingerprint != fingerprint {
		return HoldingMutationResult{}, false, ErrIdempotencyConflict
	}
	var receipt domain.HoldingMutationReceipt
	if err := json.Unmarshal([]byte(payload), &receipt); err != nil {
		return HoldingMutationResult{}, false, err
	}
	return HoldingMutationResult{Receipt: receipt}, true, nil
}

func mutationID(prefix string) (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func fingerprintHoldingRequest(method, path, operation, etag string, body []byte) string {
	value := method + "\n" + path + "\n" + operation + "\n" + etag + "\n" + string(body)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

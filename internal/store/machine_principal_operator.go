package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var (
	ErrMachinePrincipalNotFound = errors.New("machine principal not found")
	ErrMachinePrincipalInactive = errors.New("machine principal is inactive")
	ErrMachinePrincipalInvalid  = errors.New("machine principal is not eligible")
)

type MachinePrincipalGrant struct {
	PrincipalID   string `json:"principal_id"`
	PortfolioID   string `json:"portfolio_id"`
	Active        bool   `json:"active"`
	PortfolioRead bool   `json:"portfolio_read"`
	ReviewsWrite  bool   `json:"reviews_write"`
	HoldingsWrite bool   `json:"holdings_write"`
}

// GrantMachineHoldings is the explicit operator transition from a persisted
// read-only principal to a holdings writer. It never creates or reactivates a
// principal and records no secret material.
func (s *Store) GrantMachineHoldings(ctx context.Context, principalID, portfolioID string) (bool, error) {
	return s.setMachineHoldingsGrant(ctx, principalID, portfolioID, true)
}

// RevokeMachineHoldings removes only the persisted holdings-write grant. The
// configured runtime intersection remains fail-closed after a restart.
func (s *Store) RevokeMachineHoldings(ctx context.Context, principalID, portfolioID string) (bool, error) {
	return s.setMachineHoldingsGrant(ctx, principalID, portfolioID, false)
}

func (s *Store) InspectMachinePrincipal(ctx context.Context, principalID, portfolioID string) (MachinePrincipalGrant, error) {
	if !validMachinePrincipalField(principalID) || !validMachinePrincipalField(portfolioID) {
		return MachinePrincipalGrant{}, ErrMachinePrincipalInvalid
	}
	var active, read, reviews, holdings int
	err := s.DB.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_reviews_write,permission_holdings_write FROM machine_principals WHERE id=? AND portfolio_id=?`, principalID, portfolioID).Scan(&active, &read, &reviews, &holdings)
	if errors.Is(err, sql.ErrNoRows) {
		return MachinePrincipalGrant{}, ErrMachinePrincipalNotFound
	}
	if err != nil {
		return MachinePrincipalGrant{}, err
	}
	return MachinePrincipalGrant{PrincipalID: principalID, PortfolioID: portfolioID, Active: active == 1, PortfolioRead: read == 1, ReviewsWrite: reviews == 1, HoldingsWrite: holdings == 1}, nil
}

func (s *Store) setMachineHoldingsGrant(ctx context.Context, principalID, portfolioID string, enabled bool) (bool, error) {
	if !validMachinePrincipalField(principalID) || !validMachinePrincipalField(portfolioID) {
		return false, ErrMachinePrincipalInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var active, read, current int
	if err := tx.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_holdings_write FROM machine_principals WHERE id=? AND portfolio_id=?`, principalID, portfolioID).Scan(&active, &read, &current); errors.Is(err, sql.ErrNoRows) {
		return false, ErrMachinePrincipalNotFound
	} else if err != nil {
		return false, err
	}
	if active != 1 {
		return false, ErrMachinePrincipalInactive
	}
	if read != 1 {
		return false, ErrMachinePrincipalInvalid
	}
	want := boolInt(enabled)
	changed := current != want
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE machine_principals SET permission_holdings_write=? WHERE id=? AND portfolio_id=? AND active=1 AND permission_portfolio_read=1`, want, principalID, portfolioID); err != nil {
			return false, err
		}
	}
	occurredAt := canonicalSnapshotTime(s.now())
	retainUntil := canonicalSnapshotTime(s.now().AddDate(7, 0, 0))
	payload, err := json.Marshal(map[string]any{"principal_id": principalID, "portfolio_id": portfolioID, "holdings_write": enabled, "result": map[bool]string{true: "granted", false: "revoked"}[changed]})
	if !changed {
		payload, err = json.Marshal(map[string]any{"principal_id": principalID, "portfolio_id": portfolioID, "holdings_write": enabled, "result": map[bool]string{true: "already_granted", false: "already_revoked"}[enabled]})
	}
	if err != nil {
		return false, err
	}
	auditID, err := mutationID("machine-grant")
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, "operator", map[bool]string{true: "machine_holdings_grant", false: "machine_holdings_revoke"}[enabled], "machine_principal", principalID, string(payload), occurredAt, retainUntil); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, auditID, portfolioID, retainUntil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

func validMachinePrincipalField(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && mutationActorID.MatchString(value)
}

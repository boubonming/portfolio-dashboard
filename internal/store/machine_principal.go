package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
)

// RegisterMachinePrincipal creates the non-secret database identity. Existing
// rows are intentionally preserved so an operator can revoke a principal in
// SQLite without a process restart or configuration write re-enabling it.
func (s *Store) RegisterMachinePrincipal(ctx context.Context, config auth.MachineConfig) error {
	if !config.Enabled() {
		return nil
	}
	return s.RegisterMachinePrincipalIdentity(ctx, config.Principal())
}

func (s *Store) RegisterMachinePrincipalIdentity(ctx context.Context, principal auth.MachinePrincipal) error {
	var exists int
	if principal.PrincipalID == "" || principal.PortfolioID == "" {
		return nil
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id=?", principal.PortfolioID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		// The configured bearer remains disabled by the missing binding. This is
		// an authorization failure, not a startup error, and avoids leaking
		// whether a deployment has imported a particular portfolio.
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,active,created_at) VALUES(?,?,?,?,?,?)`, principal.PrincipalID, principal.PortfolioID, boolInt(principal.PortfolioRead), 1, 1, canonicalSnapshotTime(time.Now().UTC()))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// MachinePrincipalAllowed retains the database-only helper for callers that
// do not have the runtime binding. HTTP authorization must use
// MachinePrincipalAllowedForRuntime below.
func (s *Store) MachinePrincipalAllowed(ctx context.Context, principalID, portfolioID string, runtimePortfolioRead ...bool) (bool, error) {
	runtimeRead := true
	if len(runtimePortfolioRead) > 0 {
		runtimeRead = runtimePortfolioRead[0]
	}
	return s.MachinePrincipalAllowedForRuntime(ctx, principalID, portfolioID, portfolioID, runtimeRead)
}

// MachinePrincipalAllowedForRuntime applies all runtime identity bounds before
// querying the database. In particular, a restart that rebinds a principal to
// another portfolio must not retain access through an old preserved database row.
func (s *Store) MachinePrincipalAllowedForRuntime(ctx context.Context, principalID, requestedPortfolioID, configuredPortfolioID string, runtimePortfolioRead bool) (bool, error) {
	if requestedPortfolioID != configuredPortfolioID || !runtimePortfolioRead {
		return false, nil
	}
	var active, permission int
	err := s.DB.QueryRowContext(ctx, `SELECT active,permission_portfolio_read FROM machine_principals WHERE id=? AND portfolio_id=?`, principalID, requestedPortfolioID).Scan(&active, &permission)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active == 1 && permission == 1, nil
}

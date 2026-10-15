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

// MachinePrincipalAllowed applies the runtime permission as an additional
// bound when supplied. A disabled runtime grant must not be able to use a
// stale enabled database row, while a database revocation remains authoritative.
func (s *Store) MachinePrincipalAllowed(ctx context.Context, principalID, portfolioID string, runtimePortfolioRead ...bool) (bool, error) {
	if len(runtimePortfolioRead) > 0 && !runtimePortfolioRead[0] {
		return false, nil
	}
	var active, permission int
	err := s.DB.QueryRowContext(ctx, `SELECT active,permission_portfolio_read FROM machine_principals WHERE id=? AND portfolio_id=?`, principalID, portfolioID).Scan(&active, &permission)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active == 1 && permission == 1, nil
}

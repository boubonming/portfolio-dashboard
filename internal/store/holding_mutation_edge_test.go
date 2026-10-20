package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestHoldingMutationStaleAndFingerprintConflictHaveNoSideEffects(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding-edge.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actor := MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}
	params := HoldingCreateParams{PortfolioID: "portfolio-default", IfMatchRevision: 99, IdempotencyKey: "stale-key-00000001", RequestFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Actor: actor, InstrumentID: "missing", Quantity: "1", UnitCost: "1", CostBasis: "1", Currency: "USD"}
	if _, err := s.CreateHolding(context.Background(), params); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale error=%v", err)
	}
	var count, revision int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM holding_mutation_idempotency`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id=?`, params.PortfolioID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if count != 0 || revision != 0 {
		t.Fatalf("stale side effects count=%d revision=%d", count, revision)
	}
}

package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHoldingMutationCreateReplayAndArchive(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := canonicalSnapshotTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-test", "Test", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "instrument-test", "TEST", "Test instrument", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-test", "instrument-test", now); err != nil {
		t.Fatal(err)
	}

	params := HoldingCreateParams{
		PortfolioID: "portfolio-test", IfMatchRevision: 0, IdempotencyKey: "create-key-0000001",
		RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Actor:              MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}},
		InstrumentID:       "instrument-test", Quantity: "12.5000", UnitCost: "10.00", CostBasis: "125.0000", Currency: "USD",
	}
	first, err := s.CreateHolding(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt.Result != "applied" || first.Receipt.PortfolioRevision != 1 || first.Receipt.Holding.Lifecycle != "active" {
		t.Fatalf("first receipt = %+v", first.Receipt)
	}
	replay, err := s.CreateHolding(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || !reflect.DeepEqual(replay.Receipt, first.Receipt) {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	var lots, audits, activities, receipts, revision int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`, "portfolio-test").Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='holding_created'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE action='holding_created'`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM holding_mutation_idempotency`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id=?`, "portfolio-test").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if lots != 1 || audits != 1 || activities != 1 || receipts != 1 || revision != 1 {
		t.Fatalf("counts lots=%d audits=%d activities=%d receipts=%d revision=%d", lots, audits, activities, receipts, revision)
	}

	remove, err := s.RemoveHolding(context.Background(), HoldingRemoveParams{
		PortfolioID: "portfolio-test", HoldingID: first.Receipt.Holding.HoldingID, IfMatchRevision: 1,
		IdempotencyKey: "remove-key-0000001", RequestFingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Actor: params.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if remove.Receipt.Holding.Lifecycle != "removed" || remove.Receipt.PortfolioRevision != 2 {
		t.Fatalf("remove receipt = %+v", remove.Receipt)
	}
}

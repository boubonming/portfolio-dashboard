package store

import (
	"context"
	"errors"
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

func TestHoldingMutationSeparateConnectionsRaceAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holding-race.sqlite")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), path)
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()
	now := canonicalSnapshotTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, statement := range []string{
		`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('portfolio-race','Race','USD','` + now + `','` + now + `')`,
		`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES('instrument-race','RACE','Race','USD','` + now + `')`,
		`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('portfolio-race','instrument-race','` + now + `')`,
	} {
		if _, err := first.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	actor := MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}
	create := func(key, fingerprint string, revision int) HoldingCreateParams {
		return HoldingCreateParams{PortfolioID: "portfolio-race", IfMatchRevision: revision, IdempotencyKey: key, RequestFingerprint: fingerprint, Actor: actor, InstrumentID: "instrument-race", Quantity: "1", UnitCost: "1", CostBasis: "1", Currency: "USD"}
	}
	type outcome struct {
		result HoldingMutationResult
		err    error
	}
	race := func(runFirst, runSecond func() (HoldingMutationResult, error)) (outcome, outcome) {
		start := make(chan struct{})
		results := make(chan outcome, 2)
		go func() { <-start; result, err := runFirst(); results <- outcome{result: result, err: err} }()
		go func() { <-start; result, err := runSecond(); results <- outcome{result: result, err: err} }()
		close(start)
		return (<-results), (<-results)
	}
	one, two := race(
		func() (HoldingMutationResult, error) {
			return first.CreateHolding(context.Background(), create("race-create-key-0001", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0))
		},
		func() (HoldingMutationResult, error) {
			return second.CreateHolding(context.Background(), create("race-create-key-0001", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0))
		},
	)
	if one.err != nil || two.err != nil || one.result.Replayed == two.result.Replayed {
		t.Fatalf("same-key create outcomes=%+v %+v", one, two)
	}
	var revision, lots, receipts int
	if err := first.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id='portfolio-race'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := first.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-race'`).Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if err := first.DB.QueryRow(`SELECT COUNT(*) FROM holding_mutation_idempotency WHERE portfolio_id='portfolio-race'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || lots != 1 || receipts != 1 {
		t.Fatalf("same-key create counts revision=%d lots=%d receipts=%d", revision, lots, receipts)
	}
	firstHoldingID := one.result.Receipt.Holding.HoldingID
	if one.result.Replayed {
		firstHoldingID = two.result.Receipt.Holding.HoldingID
	}

	three, four := race(
		func() (HoldingMutationResult, error) {
			return first.CreateHolding(context.Background(), create("race-create-key-0002", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1))
		},
		func() (HoldingMutationResult, error) {
			return second.CreateHolding(context.Background(), create("race-create-key-0003", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", 1))
		},
	)
	if (three.err == nil) == (four.err == nil) || (three.err != nil && !errors.Is(three.err, ErrStaleRevision)) || (four.err != nil && !errors.Is(four.err, ErrStaleRevision)) {
		t.Fatalf("different-key create outcomes=%+v %+v", three, four)
	}
	created := three.result
	if three.err != nil {
		created = four.result
	}
	holdingID := created.Receipt.Holding.HoldingID
	remove := func(holdingID, key, fingerprint string, revision int) HoldingRemoveParams {
		return HoldingRemoveParams{PortfolioID: "portfolio-race", HoldingID: holdingID, IfMatchRevision: revision, IdempotencyKey: key, RequestFingerprint: fingerprint, Actor: actor}
	}
	five, six := race(
		func() (HoldingMutationResult, error) {
			return first.RemoveHolding(context.Background(), remove(firstHoldingID, "race-remove-key-0001", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", 2))
		},
		func() (HoldingMutationResult, error) {
			return second.RemoveHolding(context.Background(), remove(firstHoldingID, "race-remove-key-0001", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", 2))
		},
	)
	if five.err != nil || six.err != nil || five.result.Replayed == six.result.Replayed {
		t.Fatalf("same-key remove outcomes=%+v %+v", five, six)
	}
	seven, eight := race(
		func() (HoldingMutationResult, error) {
			return first.RemoveHolding(context.Background(), remove(holdingID, "race-remove-key-0002", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", 3))
		},
		func() (HoldingMutationResult, error) {
			return second.RemoveHolding(context.Background(), remove(holdingID, "race-remove-key-0003", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", 3))
		},
	)
	if (seven.err == nil) == (eight.err == nil) || (seven.err != nil && !errors.Is(seven.err, ErrStaleRevision)) || (eight.err != nil && !errors.Is(eight.err, ErrStaleRevision)) {
		t.Fatalf("different-key remove outcomes=%+v (%v) %+v (%v)", seven, seven.err, eight, eight.err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id='portfolio-race'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 4 {
		t.Fatalf("reopened revision=%d", revision)
	}
}

func TestHoldingMutationRollbackInjectionLeavesNoPartialRows(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		trigger string
		inline  bool
	}{
		{"instrument association", `CREATE TRIGGER fail_holding_instrument BEFORE INSERT ON portfolio_instruments BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, true},
		{"lot", `CREATE TRIGGER fail_holding_lot BEFORE INSERT ON lots BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
		{"revision", `CREATE TRIGGER fail_holding_revision BEFORE UPDATE ON portfolios WHEN NEW.holdings_revision <> OLD.holdings_revision BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
		{"audit", `CREATE TRIGGER fail_holding_audit BEFORE INSERT ON audit_events WHEN NEW.action='holding_created' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
		{"audit association", `CREATE TRIGGER fail_holding_audit_portfolio BEFORE INSERT ON audit_event_portfolios BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
		{"activity", `CREATE TRIGGER fail_holding_activity BEFORE INSERT ON activity_events WHEN NEW.action='holding_created' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
		{"receipt", `CREATE TRIGGER fail_holding_receipt BEFORE INSERT ON holding_mutation_idempotency BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openHoldingMutationFixture(t)
			defer s.Close()
			if _, err := s.DB.Exec(testCase.trigger); err != nil {
				t.Fatal(err)
			}
			params := HoldingCreateParams{
				PortfolioID: "portfolio-fixture", IfMatchRevision: 0, IdempotencyKey: "rollback-key-000001", RequestFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Actor: MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}, InstrumentID: "instrument-fixture", Quantity: "1", UnitCost: "1", CostBasis: "1", Currency: "USD",
			}
			if testCase.inline {
				params.InstrumentID = ""
				params.InlineInstrument = &InlineInstrument{Symbol: "INLINE", Description: "Inline", Currency: "USD"}
			}
			if _, err := s.CreateHolding(context.Background(), params); err == nil {
				t.Fatal("expected injected failure")
			}
			var lots, instruments, associations, audits, activities, receipts, revision int
			for _, query := range []struct {
				destination *int
				sql         string
			}{
				{&lots, `SELECT COUNT(*) FROM lots WHERE portfolio_id='portfolio-fixture'`},
				{&instruments, `SELECT COUNT(*) FROM instruments WHERE symbol='INLINE'`},
				{&associations, `SELECT COUNT(*) FROM portfolio_instruments WHERE portfolio_id='portfolio-fixture' AND instrument_id IN (SELECT id FROM instruments WHERE symbol='INLINE')`},
				{&audits, `SELECT COUNT(*) FROM audit_events WHERE action='holding_created'`},
				{&activities, `SELECT COUNT(*) FROM activity_events WHERE action='holding_created'`},
				{&receipts, `SELECT COUNT(*) FROM holding_mutation_idempotency WHERE portfolio_id='portfolio-fixture'`},
				{&revision, `SELECT holdings_revision FROM portfolios WHERE id='portfolio-fixture'`},
			} {
				if err := s.DB.QueryRow(query.sql).Scan(query.destination); err != nil {
					t.Fatal(err)
				}
			}
			if lots != 0 || instruments != 0 || associations != 0 || audits != 0 || activities != 0 || receipts != 0 || revision != 0 {
				t.Fatalf("partial mutation rows lots=%d instruments=%d associations=%d audits=%d activities=%d receipts=%d revision=%d", lots, instruments, associations, audits, activities, receipts, revision)
			}
		})
	}
}

func openHoldingMutationFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "holding-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := canonicalSnapshotTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, statement := range []string{
		`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('portfolio-fixture','Fixture','USD','` + now + `','` + now + `')`,
		`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES('instrument-fixture','FIXTURE','Fixture','USD','` + now + `')`,
		`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES('portfolio-fixture','instrument-fixture','` + now + `')`,
	} {
		if _, err := s.DB.Exec(statement); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	return s
}

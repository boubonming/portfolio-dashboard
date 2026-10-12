package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func thesisFixture(t *testing.T) *Store {
	t.Helper()
	return thesisFixtureAt(t, filepath.Join(t.TempDir(), "thesis.sqlite"))
}

func thesisFixtureAt(t *testing.T, databasePath string) *Store {
	t.Helper()
	s, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"portfolio-a", "portfolio-b"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, id, id, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-shared", "SHARED", "USD", now); err != nil {
		t.Fatal(err)
	}
	for _, lot := range []struct{ id, portfolio string }{{"holding-a", "portfolio-a"}, {"holding-b", "portfolio-b"}} {
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?)`, lot.id, lot.portfolio, "instrument-shared", "1", "USD", now); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func thesisInput(portfolio, holding, summary string, revision int64) domain.ThesisUpdate {
	return domain.ThesisUpdate{PortfolioID: portfolio, HoldingID: holding, Summary: summary, IntendedHoldingPeriod: "long term", AllocationLimit: func() *string { value := "100.00"; return &value }(), ReasonsToHold: []string{"quality"}, Risks: []string{"market"}, ReasonsToSell: []string{"thesis broken"}, ConditionsToChange: []string{"new facts"}, Sources: []domain.ThesisSource{{Label: "Research", URL: "https://example.test/research", PublicationDate: func() *string { value := "2026-01-02"; return &value }()}}, ExpectedRevision: revision}
}

func TestThesisSaveHistoryConflictAndIsolation(t *testing.T) {
	s := thesisFixture(t)
	defer s.Close()
	ctx := context.Background()
	first, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a", "first", 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.AllocationLimit == nil || *first.AllocationLimit != "100" {
		t.Fatalf("first = %+v", first)
	}
	second, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a", "second", 1))
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 {
		t.Fatalf("second revision = %d", second.Revision)
	}
	if _, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a", "third", 1)); !errors.Is(err, ErrThesisConflict) {
		t.Fatalf("stale error = %v", err)
	}
	retry, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a", "second", 1))
	if err != nil {
		t.Fatal(err)
	}
	if retry.Revision != 2 {
		t.Fatalf("retry revision = %d", retry.Revision)
	}
	history, err := s.ThesisHistory(ctx, "portfolio-a", "holding-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Revision != 1 || history[1].Revision != 2 {
		t.Fatalf("history = %+v", history)
	}
	if _, err := s.GetThesis(ctx, "portfolio-b", "holding-b"); !errors.Is(err, ErrThesisNotFound) {
		t.Fatalf("cross portfolio read = %v", err)
	}
	if _, err := s.SaveThesis(ctx, thesisInput("portfolio-b", "holding-b", "other", 0)); err != nil {
		t.Fatal(err)
	}
	var activities int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE action='portfolio.thesis.updated'`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 3 {
		t.Fatalf("activity count = %d", activities)
	}
	var payload string
	if err := s.DB.QueryRow(`SELECT payload FROM audit_events WHERE action='portfolio.thesis.updated' LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(payload)) || len(payload) > 256 {
		t.Fatalf("unsafe audit payload = %q", payload)
	}
}

func TestThesisValidationRejectsUnsafeBoundsAndSources(t *testing.T) {
	s := thesisFixture(t)
	defer s.Close()
	for _, value := range []string{"-0.01", "100.01", "1.234", "01", "1e2"} {
		input := thesisInput("portfolio-a", "holding-a", "value", 0)
		input.AllocationLimit = &value
		if _, err := s.SaveThesis(context.Background(), input); !errors.Is(err, ErrInvalidThesis) {
			t.Errorf("allocation %q error = %v", value, err)
		}
	}
	input := thesisInput("portfolio-a", "holding-a", "value", 0)
	input.Sources[0].URL = "javascript:alert(1)"
	if _, err := s.SaveThesis(context.Background(), input); !errors.Is(err, ErrInvalidThesis) {
		t.Fatalf("unsafe URL error = %v", err)
	}
}

func TestThesisAllowsSameInstrumentLotsAndDelayedHistoricalRetry(t *testing.T) {
	s := thesisFixture(t)
	defer s.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?)`, "holding-a2", "portfolio-a", "instrument-shared", "2", "USD", now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	firstInput := thesisInput("portfolio-a", "holding-a", "first accepted", 0)
	first, err := s.SaveThesis(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a", "later revision", 1)); err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveThesis(ctx, thesisInput("portfolio-a", "holding-a2", "same instrument", 0))
	if err != nil {
		t.Fatal(err)
	}
	if second.InstrumentID != first.InstrumentID || second.HoldingID == first.HoldingID {
		t.Fatalf("same-instrument holdings were not independent: first=%+v second=%+v", first, second)
	}
	retry, err := s.SaveThesis(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Revision != 1 || retry.Summary != "first accepted" {
		t.Fatalf("historical retry returned %+v", retry)
	}
	var revisions, audits, activities int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM thesis_revisions WHERE thesis_id=?`, first.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='portfolio.thesis.updated'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE action='portfolio.thesis.updated'`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 || audits != 3 || activities != 3 {
		t.Fatalf("retry created side effects: revisions=%d audits=%d activities=%d", revisions, audits, activities)
	}
}

func TestThesisEmptyArraysRemainJSONArrays(t *testing.T) {
	s := thesisFixture(t)
	defer s.Close()
	input := thesisInput("portfolio-a", "holding-a", "empty arrays", 0)
	input.ReasonsToHold = []string{}
	input.Risks = []string{}
	input.ReasonsToSell = []string{}
	input.ConditionsToChange = []string{}
	input.Sources = []domain.ThesisSource{}
	if _, err := s.SaveThesis(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetThesis(context.Background(), "portfolio-a", "holding-a")
	if err != nil {
		t.Fatal(err)
	}
	if current.ReasonsToHold == nil || current.Risks == nil || current.ReasonsToSell == nil || current.ConditionsToChange == nil || current.Sources == nil {
		t.Fatalf("empty arrays became nil: %+v", current)
	}
	history, err := s.ThesisHistory(context.Background(), "portfolio-a", "holding-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].UpdatedAt == "" || history[0].ReasonsToHold == nil || history[0].Sources == nil {
		t.Fatalf("history wire data invalid: %+v", history)
	}
}

func TestThesisSeparateStoresSerializeCreateAndUpdateRaces(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "race.sqlite")
	firstStore := thesisFixtureAt(t, databasePath)
	secondStore, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	defer secondStore.Close()

	type outcome struct {
		thesis domain.Thesis
		err    error
	}
	runRace := func(first, second domain.ThesisUpdate) [2]outcome {
		start := make(chan struct{})
		results := make(chan outcome, 2)
		var wait sync.WaitGroup
		for _, input := range []struct {
			store *Store
			input domain.ThesisUpdate
		}{{firstStore, first}, {secondStore, second}} {
			wait.Add(1)
			go func(s *Store, request domain.ThesisUpdate) {
				defer wait.Done()
				<-start
				thesis, saveErr := s.SaveThesis(context.Background(), request)
				results <- outcome{thesis: thesis, err: saveErr}
			}(input.store, input.input)
		}
		close(start)
		wait.Wait()
		close(results)
		var all [2]outcome
		index := 0
		for result := range results {
			all[index] = result
			index++
		}
		return all
	}

	created := runRace(
		thesisInput("portfolio-a", "holding-a", "race create one", 0),
		thesisInput("portfolio-a", "holding-a", "race create two", 0),
	)
	var accepted domain.Thesis
	conflicts := 0
	for _, result := range created {
		if result.err == nil {
			accepted = result.thesis
		} else if errors.Is(result.err, ErrThesisConflict) {
			conflicts++
		} else {
			t.Fatalf("create race error = %v", result.err)
		}
	}
	if accepted.Revision != 1 || conflicts != 1 {
		t.Fatalf("create race accepted=%+v conflicts=%d", accepted, conflicts)
	}

	updated := runRace(
		thesisInput("portfolio-a", "holding-a", "race update one", 1),
		thesisInput("portfolio-a", "holding-a", "race update two", 1),
	)
	conflicts = 0
	for _, result := range updated {
		if result.err == nil {
			if result.thesis.Revision != 2 {
				t.Fatalf("update race revision = %d", result.thesis.Revision)
			}
		} else if errors.Is(result.err, ErrThesisConflict) {
			conflicts++
		} else {
			t.Fatalf("update race error = %v", result.err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("update race conflicts=%d", conflicts)
	}
	for _, table := range []string{"portfolio_theses", "thesis_revisions", "audit_events", "activity_events"} {
		var count int
		if err := firstStore.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 2
		if table == "portfolio_theses" {
			want = 1
		}
		if count != want {
			t.Errorf("%s rows=%d, want %d", table, count, want)
		}
	}
}

func TestThesisCorruptStoredArraysFailClosed(t *testing.T) {
	s := thesisFixture(t)
	defer s.Close()
	input := thesisInput("portfolio-a", "holding-a", "stored corruption", 0)
	if _, err := s.SaveThesis(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE portfolio_theses SET reasons_to_hold='{"not":"an array"}' WHERE id=?`, deterministicID("thesis", "portfolio-a", "holding-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetThesis(context.Background(), "portfolio-a", "holding-a"); !errors.Is(err, ErrInvalidThesis) {
		t.Fatalf("corrupt current error = %v", err)
	}
}

func TestThesisAuditAndActivityFailuresRollBackMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		create string
	}{
		{name: "audit", create: `CREATE TRIGGER fail_thesis_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`},
		{name: "activity", create: `CREATE TRIGGER fail_thesis_activity BEFORE INSERT ON activity_events WHEN NEW.action='portfolio.thesis.updated' BEGIN SELECT RAISE(ABORT, 'forced activity failure'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := thesisFixture(t)
			defer s.Close()
			if _, err := s.DB.Exec(test.create); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SaveThesis(context.Background(), thesisInput("portfolio-a", "holding-a", "must roll back", 0)); err == nil {
				t.Fatal("SaveThesis unexpectedly succeeded")
			}
			for _, table := range []string{"portfolio_theses", "thesis_revisions", "audit_events", "activity_events"} {
				var count int
				if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Errorf("%s rows=%d after rollback", table, count)
				}
			}
		})
	}
}

func TestThesisSchemaVerificationRejectsCorruption(t *testing.T) {
	corruptions := []struct {
		name   string
		mutate func(*sql.DB) error
		assert func(*sql.DB) error
	}{
		{name: "portfolio table", mutate: execSchemaCorruption(`PRAGMA foreign_keys=OFF; DROP TABLE portfolio_theses`), assert: assertSchemaObjectAbsent("table", "portfolio_theses")},
		{name: "revision table", mutate: execSchemaCorruption(`PRAGMA foreign_keys=OFF; DROP TABLE thesis_revisions`), assert: assertSchemaObjectAbsent("table", "thesis_revisions")},
		{name: "portfolio index", mutate: execSchemaCorruption(`DROP INDEX idx_portfolio_theses_portfolio`), assert: assertSchemaObjectAbsent("index", "idx_portfolio_theses_portfolio")},
		{name: "instrument index", mutate: execSchemaCorruption(`DROP INDEX idx_portfolio_theses_instrument`), assert: assertSchemaObjectAbsent("index", "idx_portfolio_theses_instrument")},
		{name: "history index direction", mutate: execSchemaCorruption(`DROP INDEX idx_thesis_revisions_history; CREATE INDEX idx_thesis_revisions_history ON thesis_revisions(thesis_id, revision ASC)`), assert: assertIndexDefinition("idx_thesis_revisions_history", []string{"thesis_id", "revision"}, []int{0, 1})},
		{name: "portfolio index direction", mutate: execSchemaCorruption(`DROP INDEX idx_thesis_revisions_portfolio; CREATE INDEX idx_thesis_revisions_portfolio ON thesis_revisions(portfolio_id DESC, holding_id, revision DESC)`), assert: assertIndexDefinition("idx_thesis_revisions_portfolio", []string{"portfolio_id", "holding_id", "revision"}, []int{0, 0, 1})},
		{name: "lot unique index", mutate: execSchemaCorruption(`DROP INDEX idx_lots_portfolio_id_id`), assert: assertSchemaObjectAbsent("index", "idx_lots_portfolio_id_id")},
		{name: "composite unique definition", mutate: corruptPortfolioThesesCompositeUnique, assert: assertUniqueDefinitionAbsent("portfolio_theses", []string{"portfolio_id", "holding_id"})},
		{name: "scope insert trigger", mutate: execSchemaCorruption(`DROP TRIGGER portfolio_theses_scope_insert`), assert: assertSchemaObjectAbsent("trigger", "portfolio_theses_scope_insert")},
		{name: "scope update trigger", mutate: execSchemaCorruption(`DROP TRIGGER portfolio_theses_scope_update`), assert: assertSchemaObjectAbsent("trigger", "portfolio_theses_scope_update")},
		{name: "revision scope trigger", mutate: execSchemaCorruption(`DROP TRIGGER thesis_revisions_scope_insert`), assert: assertSchemaObjectAbsent("trigger", "thesis_revisions_scope_insert")},
		{name: "thesis delete trigger", mutate: execSchemaCorruption(`DROP TRIGGER portfolio_theses_append_only_delete`), assert: assertSchemaObjectAbsent("trigger", "portfolio_theses_append_only_delete")},
		{name: "revision update trigger", mutate: execSchemaCorruption(`DROP TRIGGER thesis_revisions_append_only_update`), assert: assertSchemaObjectAbsent("trigger", "thesis_revisions_append_only_update")},
		{name: "revision delete trigger", mutate: execSchemaCorruption(`DROP TRIGGER thesis_revisions_append_only_delete`), assert: assertSchemaObjectAbsent("trigger", "thesis_revisions_append_only_delete")},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrupt.sqlite")
			clean := thesisFixtureAt(t, path)
			if err := clean.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			if err := corruption.mutate(db); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := corruption.assert(db); err != nil {
				db.Close()
				t.Fatalf("corruption did not land: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(context.Background(), path)
			if err == nil {
				opened.Close()
				t.Fatal("Open accepted a corrupted thesis schema")
			}
		})
	}
}

func execSchemaCorruption(stmt string) func(*sql.DB) error {
	return func(db *sql.DB) error {
		_, err := db.Exec(stmt)
		return err
	}
}

func assertSchemaObjectAbsent(objectType, name string) func(*sql.DB) error {
	return func(db *sql.DB) error {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, objectType, name).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%s %s still exists", objectType, name)
		}
		return nil
	}
}

func assertIndexDefinition(name string, columns []string, descending []int) func(*sql.DB) error {
	return func(db *sql.DB) error {
		if err := verifyIndexColumns(context.Background(), db, name, columns, descending); err == nil {
			return fmt.Errorf("index %s still has its canonical definition", name)
		}
		return nil
	}
}

func assertUniqueDefinitionAbsent(table string, columns []string) func(*sql.DB) error {
	return func(db *sql.DB) error {
		if err := verifyAnyUniqueColumns(context.Background(), db, table, columns); err == nil {
			return fmt.Errorf("%s still has unique definition %v", table, columns)
		}
		return nil
	}
}

func corruptPortfolioThesesCompositeUnique(db *sql.DB) error {
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type='trigger' AND tbl_name='portfolio_theses'`)
	if err != nil {
		return err
	}
	var triggers []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, definition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; CREATE TABLE portfolio_theses_corrupt (
    id TEXT PRIMARY KEY,
    portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
    holding_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL REFERENCES instruments(id),
    summary TEXT NOT NULL,
    intended_holding_period TEXT NOT NULL,
    allocation_limit TEXT,
    reasons_to_hold TEXT NOT NULL,
    risks TEXT NOT NULL,
    reasons_to_sell TEXT NOT NULL,
    conditions_to_change TEXT NOT NULL,
    sources TEXT NOT NULL,
    current_revision INTEGER NOT NULL CHECK (current_revision >= 1),
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(id, portfolio_id, holding_id, instrument_id),
    FOREIGN KEY (portfolio_id, holding_id) REFERENCES lots(portfolio_id, id)
); INSERT INTO portfolio_theses_corrupt SELECT * FROM portfolio_theses; DROP TABLE portfolio_theses; ALTER TABLE portfolio_theses_corrupt RENAME TO portfolio_theses; CREATE INDEX idx_portfolio_theses_portfolio ON portfolio_theses(portfolio_id); CREATE INDEX idx_portfolio_theses_instrument ON portfolio_theses(portfolio_id, instrument_id)`); err != nil {
		return err
	}
	for _, trigger := range triggers {
		if _, err := db.Exec(trigger); err != nil {
			return err
		}
	}
	return nil
}

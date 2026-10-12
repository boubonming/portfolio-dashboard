package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		if err := firstStore.DB.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
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
				if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
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
		name string
		stmt string
	}{
		{name: "portfolio table", stmt: `PRAGMA foreign_keys=OFF; DROP TABLE portfolio_theses`},
		{name: "revision table", stmt: `PRAGMA foreign_keys=OFF; DROP TABLE thesis_revisions`},
		{name: "portfolio index", stmt: `DROP INDEX idx_portfolio_theses_portfolio`},
		{name: "instrument index", stmt: `DROP INDEX idx_portfolio_theses_instrument`},
		{name: "history index direction", stmt: `DROP INDEX idx_thesis_revisions_history; CREATE INDEX idx_thesis_revisions_history ON thesis_revisions(thesis_id, revision ASC)`},
		{name: "portfolio index direction", stmt: `DROP INDEX idx_thesis_revisions_portfolio; CREATE INDEX idx_thesis_revisions_portfolio ON thesis_revisions(portfolio_id DESC, holding_id, revision DESC)`},
		{name: "lot unique index", stmt: `DROP INDEX idx_lots_portfolio_id_id`},
		{name: "composite unique definition", stmt: "PRAGMA writable_schema=ON; UPDATE sqlite_master SET sql=replace(sql, '    UNIQUE(portfolio_id, holding_id),\\n', '') WHERE type='table' AND name='portfolio_theses'; PRAGMA schema_version=999; PRAGMA writable_schema=OFF"},
		{name: "scope insert trigger", stmt: `DROP TRIGGER portfolio_theses_scope_insert`},
		{name: "scope update trigger", stmt: `DROP TRIGGER portfolio_theses_scope_update`},
		{name: "revision scope trigger", stmt: `DROP TRIGGER thesis_revisions_scope_insert`},
		{name: "thesis delete trigger", stmt: `DROP TRIGGER portfolio_theses_append_only_delete`},
		{name: "revision update trigger", stmt: `DROP TRIGGER thesis_revisions_append_only_update`},
		{name: "revision delete trigger", stmt: `DROP TRIGGER thesis_revisions_append_only_delete`},
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
			if _, err := db.Exec(corruption.stmt); err != nil {
				db.Close()
				t.Fatal(err)
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

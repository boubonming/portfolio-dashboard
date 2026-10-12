package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func thesisFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "thesis.sqlite"))
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

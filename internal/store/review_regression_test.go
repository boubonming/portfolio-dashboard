package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

type reviewRegressionFixture struct {
	store      *Store
	path       string
	portfolio  string
	principal  auth.MachinePrincipal
	snapshot   domain.ReviewSnapshot
	submission domain.ReviewSubmission
}

func newReviewRegressionFixture(t *testing.T) *reviewRegressionFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reviews.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.January, 2, 12, 0, 0, 123456789, time.UTC)
	nowText := now.Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES('review-portfolio','Review fixture','USD',?,?)`,
		`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES('review-instrument','REV','Review fixture','USD',?)`,
		`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,unit_cost,cost_basis,currency,created_at,created_at_order_key) VALUES('review-lot','review-portfolio','review-instrument','1','2','2','USD',?,?)`,
	} {
		args := []any{nowText, nowText}
		if strings.Contains(statement, "lots") {
			args = []any{nowText, nowText}
		} else if strings.Contains(statement, "instruments") {
			args = []any{nowText}
		}
		if _, err := s.DB.Exec(statement, args...); err != nil {
			s.Close()
			t.Fatal(err)
		}
		if strings.Contains(statement, "instruments") {
			if _, err := s.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "review-portfolio", "review-instrument", nowText); err != nil {
				s.Close()
				t.Fatal(err)
			}
		}
	}
	if _, err := s.AddQuote(ctx, QuoteInput{InstrumentID: "review-instrument", Price: "2", Currency: "USD", Source: "fixture", MarketAt: nowText, Basis: "close", Provenance: "review fixture"}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "review-portfolio", ReportingCurrency: "USD", CalculationVersion: "review.v1", AsOf: now, MaxAge: time.Hour})
	if err != nil || !status.Complete {
		s.Close()
		t.Fatalf("snapshot=%+v err=%v", status, err)
	}
	snapshot, err := s.ReviewSnapshot(ctx, "review-portfolio", status.SnapshotID, false)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	principal := auth.MachinePrincipal{PrincipalID: "review-machine", PortfolioID: "review-portfolio", PortfolioRead: true, ReviewsWrite: true}
	if err := s.RegisterMachinePrincipalIdentity(ctx, principal); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return &reviewRegressionFixture{
		store: s, path: path, portfolio: "review-portfolio", principal: principal, snapshot: snapshot,
		submission: domain.ReviewSubmission{
			SnapshotID: snapshot.SnapshotID, CalculationVersion: snapshot.CalculationVersion,
			Scope:  domain.ReviewScope{Type: "portfolio", HoldingIDs: []string{}, InstrumentIDs: []string{}},
			Status: "complete", Recommendation: "hold", Rationale: "rationale", Risks: []string{"risk"},
			Counterarguments: []string{"counterargument"}, Conditions: []string{"condition"},
			SourcedFacts: []domain.SourcedFact{{Statement: "fact", EvidenceIDs: []string{"e1"}}},
			ModelOpinion: "opinion", Evidence: []domain.ReviewEvidence{{EvidenceID: "e1", URL: "https://example.test/source", Title: "source", AccessedAt: "2026-01-02T12:00:00.000000000Z", Availability: "available", Freshness: "current", Excerpt: "excerpt"}},
			Model:       domain.ModelProvenance{Provider: "local", Model: "review-model", Version: "v1", Runtime: "hermes-environment"},
			GeneratedAt: "2026-01-02T12:00:00.000000000Z",
		},
	}
}

func (f *reviewRegressionFixture) close() { _ = f.store.Close() }

func submitRegressionReview(t *testing.T, f *reviewRegressionFixture, key string, submission domain.ReviewSubmission) (domain.AdvisoryReview, bool) {
	t.Helper()
	fingerprint, err := ReviewFingerprint(submission)
	if err != nil {
		t.Fatal(err)
	}
	result, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, key, fingerprint, submission)
	if err != nil {
		t.Fatal(err)
	}
	return result, replay
}

func reviewTableCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"advisory_reviews", "review_targets", "review_risks", "review_counterarguments", "review_conditions", "review_metrics", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios", "activity_events"} {
		var count int
		query := "SELECT COUNT(*) FROM " + table
		switch table {
		case "audit_events":
			query += " WHERE action='review_submitted'"
		case "audit_event_portfolios":
			query = "SELECT COUNT(*) FROM audit_event_portfolios p JOIN audit_events a ON a.id=p.audit_event_id WHERE a.action='review_submitted'"
		case "activity_events":
			query += " WHERE action='review_submitted'"
		}
		if err := s.DB.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestReviewReplayUsesOnlyNormalizedRowsAfterSnapshotPayloadUnavailable(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	created, replay := submitRegressionReview(t, f, "normalized-replay-key", f.submission)
	if replay || created.ReviewID == "" {
		t.Fatalf("first submission replay=%v result=%+v", replay, created)
	}
	before := reviewTableCounts(t, f.store)
	if _, err := f.store.DB.Exec(`DROP TRIGGER snapshots_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`UPDATE snapshots SET payload='{}' WHERE id=?`, f.snapshot.SnapshotID); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	replayed, isReplay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "normalized-replay-key", fingerprint, f.submission)
	if err != nil || !isReplay {
		t.Fatalf("replay=%v err=%v", isReplay, err)
	}
	if !reflect.DeepEqual(created, replayed) {
		t.Fatalf("replay changed immutable response:\ncreated=%+v\nreplayed=%+v", created, replayed)
	}
	if after := reviewTableCounts(t, f.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("replay changed normalized counts: before=%v after=%v", before, after)
	}
}

func TestReviewRevocationDeniesReplayAndNewSubmissionWithoutWrites(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	_, _ = submitRegressionReview(t, f, "revocation-existing-key", f.submission)
	before := reviewTableCounts(t, f.store)
	if _, err := f.store.DB.Exec(`UPDATE machine_principals SET active=0 WHERE id=? AND portfolio_id=?`, f.principal.PrincipalID, f.portfolio); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"revocation-existing-key", "revocation-new-key"} {
		_, replay, callErr := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, key, fingerprint, f.submission)
		if !errors.Is(callErr, auth.ErrForbidden) || replay {
			t.Fatalf("key=%s replay=%v err=%v", key, replay, callErr)
		}
	}
	if after := reviewTableCounts(t, f.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("revocation changed rows: before=%v after=%v", before, after)
	}
}

func TestReviewConcurrentIdempotencyAcrossSeparateConnections(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondPrincipal := f.principal
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan struct {
		result domain.AdvisoryReview
		replay bool
		err    error
	}, 2)
	var group sync.WaitGroup
	for _, database := range []*Store{f.store, second} {
		group.Add(1)
		go func(database *Store) {
			defer group.Done()
			result, replay, submitErr := database.SubmitAdvisoryReview(context.Background(), secondPrincipal, f.portfolio, "concurrent-idempotency-key", fingerprint, f.submission)
			results <- struct {
				result domain.AdvisoryReview
				replay bool
				err    error
			}{result, replay, submitErr}
		}(database)
	}
	group.Wait()
	close(results)
	var successful int
	var reviewID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent submit returned error: %v", result.err)
		}
		if result.result.ReviewID == "" {
			t.Fatal("concurrent submit returned empty review id")
		}
		if reviewID == "" {
			reviewID = result.result.ReviewID
		}
		if result.result.ReviewID != reviewID {
			t.Fatalf("concurrent requests produced review IDs %q and %q", reviewID, result.result.ReviewID)
		}
		successful++
	}
	if successful != 2 {
		t.Fatalf("successful requests=%d", successful)
	}
	counts := reviewTableCounts(t, f.store)
	for _, table := range []string{"advisory_reviews", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios"} {
		if counts[table] != 1 {
			t.Fatalf("%s count=%d, want 1", table, counts[table])
		}
	}
	if counts["activity_events"] != 1 {
		t.Fatalf("activity count=%d, want 1", counts["activity_events"])
	}
}

func TestReviewConcurrentConflictingIdempotencyReturnsOneConflict(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	second, err := Open(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	conflicting := f.submission
	conflicting.Rationale = "conflicting request body"
	firstFingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := ReviewFingerprint(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index, database := range []*Store{f.store, second} {
		group.Add(1)
		go func(index int, database *Store) {
			defer group.Done()
			submission, fingerprint := f.submission, firstFingerprint
			if index == 1 {
				submission, fingerprint = conflicting, secondFingerprint
			}
			_, _, submitErr := database.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "concurrent-conflict-key", fingerprint, submission)
			results <- submitErr
		}(index, database)
	}
	group.Wait()
	close(results)
	var success, conflict int
	for submitErr := range results {
		switch {
		case submitErr == nil:
			success++
		case errors.Is(submitErr, ErrReviewIdempotencyConflict):
			conflict++
		default:
			t.Fatalf("unexpected concurrent conflict error: %v", submitErr)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent conflict results success=%d conflict=%d", success, conflict)
	}
	counts := reviewTableCounts(t, f.store)
	if counts["advisory_reviews"] != 1 || counts["review_idempotency"] != 1 || counts["audit_events"] != 1 || counts["activity_events"] != 1 {
		t.Fatalf("concurrent conflict counts=%v", counts)
	}
}

func TestReviewWitnessUsesActualFactEvidenceOrder(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	f.submission.Evidence = []domain.ReviewEvidence{
		{EvidenceID: "e1", URL: "https://example.test/first", Title: "first", AccessedAt: "2026-01-02T12:00:00.000000000Z", Availability: "available", Freshness: "current", Excerpt: "first"},
		{EvidenceID: "e2", URL: "https://example.test/second", Title: "second", AccessedAt: "2026-01-02T12:00:00.000000000Z", Availability: "available", Freshness: "current", Excerpt: "second"},
	}
	f.submission.SourcedFacts = []domain.SourcedFact{{Statement: "fact one", EvidenceIDs: []string{"e2"}}, {Statement: "fact two", EvidenceIDs: []string{"e1"}}}
	created, replay := submitRegressionReview(t, f, "witness-order-key", f.submission)
	if replay {
		t.Fatal("first witness-order submission was a replay")
	}
	var evidenceID string
	var factOrdinal int
	if err := f.store.DB.QueryRow(`SELECT primary_evidence_id,primary_fact_ordinal FROM advisory_reviews WHERE id=?`, created.ReviewID).Scan(&evidenceID, &factOrdinal); err != nil {
		t.Fatal(err)
	}
	if evidenceID != "e2" || factOrdinal != 0 {
		t.Fatalf("stored witness=%q/%d, want e2/0", evidenceID, factOrdinal)
	}

	f2 := newReviewRegressionFixture(t)
	defer f2.close()
	f2.submission.Evidence[0].Freshness = "stale"
	f2.submission.SourcedFacts[0].EvidenceIDs = []string{"e1"}
	if err := ValidateReviewSubmission(f2.submission, f2.snapshot); !errors.Is(err, ErrReviewIncomplete) {
		t.Fatalf("no-current witness error=%v, want incomplete validation (state=%s scope=%v model=%v metrics=%v evidence=%v facts=%v completeEvidence=%v)", err, f2.snapshot.State, validateReviewScope(f2.submission.Scope, f2.snapshot), validateReviewModel(f2.submission.Model), validateReviewMetrics(f2.submission.Metrics), validateReviewEvidence(f2.submission.Evidence), validateSourcedFacts(f2.submission.SourcedFacts, f2.submission.Evidence), validCompleteEvidence(f2.submission.Evidence))
	}
}

func TestReviewTextControlsAndAllowedWhitespaceMatrix(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	mutations := []struct {
		name string
		edit func(*domain.ReviewSubmission, string)
	}{
		{"rationale", func(s *domain.ReviewSubmission, v string) { s.Rationale = v }},
		{"model opinion", func(s *domain.ReviewSubmission, v string) { s.ModelOpinion = v }},
		{"risk", func(s *domain.ReviewSubmission, v string) { s.Risks = []string{v} }},
		{"counterargument", func(s *domain.ReviewSubmission, v string) { s.Counterarguments = []string{v} }},
		{"condition", func(s *domain.ReviewSubmission, v string) { s.Conditions = []string{v} }},
		{"metric name", func(s *domain.ReviewSubmission, v string) {
			s.Metrics = []domain.ReviewMetric{{Name: v, Value: "1", Unit: "USD", AsOf: "2026-01-02T12:00:00.000000000Z", Source: "submitted", Deterministic: true}}
		}},
		{"metric unit", func(s *domain.ReviewSubmission, v string) {
			s.Metrics = []domain.ReviewMetric{{Name: "metric", Value: "1", Unit: v, AsOf: "2026-01-02T12:00:00.000000000Z", Source: "submitted", Deterministic: true}}
		}},
		{"fact statement", func(s *domain.ReviewSubmission, v string) { s.SourcedFacts[0].Statement = v }},
		{"evidence title", func(s *domain.ReviewSubmission, v string) { s.Evidence[0].Title = v }},
		{"evidence excerpt", func(s *domain.ReviewSubmission, v string) { s.Evidence[0].Excerpt = v }},
		{"model provider", func(s *domain.ReviewSubmission, v string) { s.Model.Provider = v }},
		{"model name", func(s *domain.ReviewSubmission, v string) { s.Model.Model = v }},
		{"model version", func(s *domain.ReviewSubmission, v string) { s.Model.Version = v }},
	}
	controls := []string{"\x01", "\x1f", "\x7f", "\u0085", "\u009f"}
	for _, mutation := range mutations {
		for _, control := range controls {
			t.Run(mutation.name+"/"+fmt.Sprintf("U%04X", []rune(control)[0]), func(t *testing.T) {
				encoded, err := json.Marshal(f.submission)
				if err != nil {
					t.Fatal(err)
				}
				var submission domain.ReviewSubmission
				if err := json.Unmarshal(encoded, &submission); err != nil {
					t.Fatal(err)
				}
				mutation.edit(&submission, "safe"+control+"text")
				if err := ValidateReviewSubmission(submission, f.snapshot); !errors.Is(err, ErrReviewInvalid) {
					t.Fatalf("control accepted: %q err=%v", control, err)
				}
			})
		}
	}
	for _, whitespace := range []string{"line\nbreak", "tab\tvalue"} {
		submission := f.submission
		submission.Rationale = whitespace
		if err := ValidateReviewSubmission(submission, f.snapshot); err != nil {
			t.Fatalf("allowed whitespace %q rejected: %v", whitespace, err)
		}
	}
}

func TestEvidenceURLFidelityAndBoundaryMatrix(t *testing.T) {
	valid := []struct {
		raw    string
		suffix string
		valid  bool
	}{
		{"https://example.test?", "?", true},
		{"https://example.test/path?", "/path?", true},
		{"https://example.test:443/path", "", false}, // explicit port is not persisted
	}
	for _, tc := range valid {
		t.Run(tc.raw, func(t *testing.T) {
			host, suffix, err := canonicalEvidenceURL(tc.raw)
			if tc.valid {
				if err != nil || host != "example.test" || suffix != tc.suffix {
					t.Fatalf("URL %q -> host=%q suffix=%q err=%v", tc.raw, host, suffix, err)
				}
				return
			}
			if !errors.Is(err, ErrReviewInvalid) {
				t.Fatalf("invalid URL accepted: %q err=%v", tc.raw, err)
			}
		})
	}
	for _, raw := range []string{"https://example.test:444/path", "https://user@example.test/path", "https://example.test/path#fragment", " https://example.test/path", "https://example.test/path ", "https://EXAMPLE.test/path", "https://example..test/path", "https://-example.test/path", "https://example.test/path\t", "https://example.test/path\x00", "https://example.test?bad["} {
		if _, _, err := canonicalEvidenceURL(raw); !errors.Is(err, ErrReviewInvalid) {
			t.Fatalf("invalid URL accepted: %q err=%v", raw, err)
		}
	}
	longPath := strings.Repeat("a", 2033)
	if _, _, err := canonicalEvidenceURL("https://x.test/" + longPath); err != nil {
		t.Fatalf("URL at reconstructed-length boundary rejected: %v", err)
	}
	if _, _, err := canonicalEvidenceURL("https://x.test/" + strings.Repeat("a", 2034)); !errors.Is(err, ErrReviewInvalid) {
		t.Fatal("URL beyond reconstructed-length boundary accepted")
	}
}

func TestReviewRollbackLeavesNoPartialGraphAcrossFailurePoints(t *testing.T) {
	for _, failure := range []struct {
		name   string
		table  string
		when   string
		column string
	}{
		{"parent", "advisory_reviews", "BEFORE INSERT", ""},
		{"child", "review_evidence", "BEFORE INSERT", ""},
		{"idempotency", "review_idempotency", "BEFORE INSERT", ""},
		{"audit", "audit_events", "BEFORE INSERT", ""},
		{"activity", "activity_events", "BEFORE INSERT", ""},
	} {
		t.Run(failure.name, func(t *testing.T) {
			f := newReviewRegressionFixture(t)
			defer f.close()
			trigger := "fail_review_" + failure.name
			condition := "1"
			if failure.table == "audit_events" {
				condition = "NEW.action='review_submitted'"
			}
			if failure.table == "activity_events" {
				condition = "NEW.action='review_submitted'"
			}
			if _, err := f.store.DB.Exec(fmt.Sprintf("CREATE TRIGGER %s %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT, 'regression failure'); END", trigger, failure.when, failure.table, condition)); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := ReviewFingerprint(f.submission)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "rollback-"+failure.name, fingerprint, f.submission); err == nil {
				t.Fatal("failure trigger did not abort submission")
			}
			counts := reviewTableCounts(t, f.store)
			for _, table := range []string{"advisory_reviews", "review_evidence", "review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency", "audit_events", "audit_event_portfolios", "activity_events"} {
				if counts[table] != 0 {
					t.Fatalf("%s retained partial rows after %s failure: %d", table, failure.name, counts[table])
				}
			}
		})
	}
}

func TestReviewRetentionAndAccountingForCompleteSubmission(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	f.store.SetClock(func() time.Time { return time.Date(2026, time.January, 2, 12, 0, 0, 123456789, time.UTC) })
	result, replay := submitRegressionReview(t, f, "accounting-key", f.submission)
	if replay || result.ReviewID == "" {
		t.Fatal("expected new complete submission")
	}
	counts := reviewTableCounts(t, f.store)
	want := map[string]int{"advisory_reviews": 1, "review_targets": 0, "review_risks": 1, "review_counterarguments": 1, "review_conditions": 1, "review_metrics": 0, "review_evidence": 1, "review_sourced_facts": 1, "review_fact_evidence": 1, "review_submission_permission_snapshots": 1, "review_idempotency": 1, "audit_events": 1, "audit_event_portfolios": 1, "activity_events": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("complete accounting=%v want=%v", counts, want)
	}
	var retain, submitted string
	if err := f.store.DB.QueryRow(`SELECT retain_until,submitted_at FROM advisory_reviews WHERE id=?`, result.ReviewID).Scan(&retain, &submitted); err != nil {
		t.Fatal(err)
	}
	var years int
	if err := f.store.DB.QueryRow(`SELECT CAST(strftime('%Y',?) AS INTEGER)-CAST(strftime('%Y',?) AS INTEGER)`, retain, submitted).Scan(&years); err != nil {
		t.Fatal(err)
	}
	if years != 7 {
		t.Fatalf("retention years=%d retain=%q submitted=%q", years, retain, submitted)
	}
}

func TestReviewRetentionAndAccountingForIncompleteSubmission(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	f.submission.Status = "incomplete"
	f.submission.Recommendation = "no_recommendation"
	f.submission.Evidence[0].Freshness = "stale"
	result, replay := submitRegressionReview(t, f, "incomplete-accounting-key", f.submission)
	if replay || result.Status != "incomplete" {
		t.Fatalf("incomplete submission replay=%v result=%+v", replay, result)
	}
	counts := reviewTableCounts(t, f.store)
	want := map[string]int{"advisory_reviews": 1, "review_targets": 0, "review_risks": 1, "review_counterarguments": 1, "review_conditions": 1, "review_metrics": 0, "review_evidence": 1, "review_sourced_facts": 1, "review_fact_evidence": 1, "review_submission_permission_snapshots": 1, "review_idempotency": 1, "audit_events": 1, "audit_event_portfolios": 1, "activity_events": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("incomplete accounting=%v want=%v", counts, want)
	}
	var primaryEvidence, primaryFact string
	if err := f.store.DB.QueryRow(`SELECT COALESCE(primary_evidence_id,''),COALESCE(CAST(primary_fact_ordinal AS TEXT),'') FROM advisory_reviews WHERE id=?`, result.ReviewID).Scan(&primaryEvidence, &primaryFact); err != nil {
		t.Fatal(err)
	}
	if primaryEvidence != "" || primaryFact != "" {
		t.Fatalf("incomplete submission persisted witness evidence=%q fact=%q", primaryEvidence, primaryFact)
	}
}

func TestReviewNormalizedCorruptionFailsClosedAsCorrupt(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	created, _ := submitRegressionReview(t, f, "corruption-key", f.submission)
	if _, err := f.store.DB.Exec(`DROP TRIGGER phase_d_review_evidence_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`DELETE FROM review_evidence WHERE review_id=?`, created.ReviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "corruption-key", fingerprint, f.submission)
	if !errors.Is(err, ErrReviewCorrupt) {
		t.Fatalf("corrupt replay replay=%v err=%v", replay, err)
	}
}

func TestReviewConflictingIdempotencyIsDeterministic(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	_, _ = submitRegressionReview(t, f, "conflict-key", f.submission)
	conflicting := f.submission
	conflicting.Rationale = "different body"
	fingerprint, err := ReviewFingerprint(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "conflict-key", fingerprint, conflicting)
	if !errors.Is(err, ErrReviewIdempotencyConflict) || replay {
		t.Fatalf("conflict replay=%v err=%v", replay, err)
	}
}

func TestReviewTargetSnapshotVersionCorruptionFailsClosed(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	f.submission.Scope = domain.ReviewScope{Type: "holding", HoldingIDs: []string{f.snapshot.Holdings[0].HoldingID}, InstrumentIDs: []string{f.snapshot.Holdings[0].InstrumentID}}
	created, replay := submitRegressionReview(t, f, "target-corruption-key", f.submission)
	if replay {
		t.Fatal("target corruption fixture was a replay")
	}
	if _, err := f.store.DB.Exec(`DROP TRIGGER phase_d_review_targets_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`UPDATE review_targets SET snapshot_id='wrong-snapshot', calculation_version='wrong-version' WHERE review_id=?`, created.ReviewID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err = f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "target-corruption-key", fingerprint, f.submission)
	if replay || !errors.Is(err, ErrReviewCorrupt) {
		t.Fatalf("corrupt target replay=%v err=%v", replay, err)
	}
}

func TestReviewFactEvidenceOrphanAndForeignRowsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  string
		args []any
	}{
		{name: "orphan-fact", row: `INSERT INTO review_fact_evidence(review_id,portfolio_id,fact_ordinal,evidence_id,evidence_ordinal) VALUES(?,?,?,?,?)`, args: []any{"REVIEW", "review-portfolio", 31, "e1", 0}},
		{name: "foreign-portfolio", row: `INSERT INTO review_fact_evidence(review_id,portfolio_id,fact_ordinal,evidence_id,evidence_ordinal) VALUES(?,?,?,?,?)`, args: []any{"REVIEW", "foreign-portfolio", 0, "e1", 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewRegressionFixture(t)
			defer f.close()
			created, _ := submitRegressionReview(t, f, "fact-link-"+tc.name, f.submission)
			if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			args := append([]any{created.ReviewID}, tc.args[1:]...)
			if tc.name == "foreign-portfolio" {
				args = []any{created.ReviewID, "foreign-portfolio", 0, "e2", 1}
			} else {
				args = []any{created.ReviewID, "review-portfolio", 31, "e1", 0}
			}
			if _, err := f.store.DB.Exec(tc.row, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.DB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := ReviewFingerprint(f.submission)
			if err != nil {
				t.Fatal(err)
			}
			_, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "fact-link-"+tc.name, fingerprint, f.submission)
			if replay || !errors.Is(err, ErrReviewCorrupt) {
				t.Fatalf("corrupt fact link replay=%v err=%v", replay, err)
			}
		})
	}
}

func TestReviewReplayAttributionMismatchFailsClosed(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	created, _ := submitRegressionReview(t, f, "attribution-corruption-key", f.submission)
	if _, err := f.store.DB.Exec(`DROP TRIGGER phase_d_review_append_only_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB.Exec(`UPDATE advisory_reviews SET submitted_by_id='other-machine' WHERE id=?`, created.ReviewID); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "attribution-corruption-key", fingerprint, f.submission)
	if replay || !errors.Is(err, ErrReviewCorrupt) {
		t.Fatalf("corrupt attribution replay=%v err=%v", replay, err)
	}
}

func TestReviewRetryRejectsAbortLockMessageAndCanceledContext(t *testing.T) {
	f := newReviewRegressionFixture(t)
	defer f.close()
	if _, err := f.store.DB.Exec(`CREATE TRIGGER review_retry_abort_message BEFORE INSERT ON advisory_reviews BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	defer f.store.DB.Exec(`DROP TRIGGER review_retry_abort_message`)
	fingerprint, err := ReviewFingerprint(f.submission)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := f.store.SubmitAdvisoryReview(context.Background(), f.principal, f.portfolio, "abort-lock-message-key", fingerprint, f.submission)
	if replay || err == nil || isSQLiteBusy(err) {
		t.Fatalf("abort message replay=%v err=%v busy=%v", replay, err, isSQLiteBusy(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, replay, err = f.store.SubmitAdvisoryReview(ctx, f.principal, f.portfolio, "canceled-before-work-key", fingerprint, f.submission)
	if replay || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled submission replay=%v err=%v", replay, err)
	}
}

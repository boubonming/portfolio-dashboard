package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func corruptActivitySchema(t *testing.T, path, body string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(body); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func activitySchemaReplacement(includeForeignKeys, includeTimestampCheck bool) string {
	foreignKeys := ""
	if includeForeignKeys {
		foreignKeys = " REFERENCES portfolios(id)"
	}
	auditForeignKey := ""
	if includeForeignKeys {
		auditForeignKey = ",\n FOREIGN KEY(source_audit_event_id,portfolio_id) REFERENCES audit_event_portfolios(audit_event_id,portfolio_id)"
	}
	check := ""
	if includeTimestampCheck {
		check = " CHECK (length(occurred_at) = 30 AND occurred_at >= '1970-01-01T00:00:00.000000000Z')"
	}
	return `PRAGMA foreign_keys=OFF;
DROP TRIGGER IF EXISTS activity_events_append_only_update;
DROP TRIGGER IF EXISTS activity_events_append_only_delete;
DROP INDEX IF EXISTS idx_activity_source_portfolio;
DROP INDEX IF EXISTS idx_activity_portfolio_order;
ALTER TABLE activity_events RENAME TO activity_events_original;
CREATE TABLE activity_events (
 id TEXT PRIMARY KEY,
 portfolio_id TEXT NOT NULL` + foreignKeys + `,
 actor_category TEXT NOT NULL,
 action TEXT NOT NULL,
 category TEXT NOT NULL,
 entity_type TEXT,
 entity_id TEXT,
 summary TEXT NOT NULL,
 details TEXT NOT NULL DEFAULT '',
 occurred_at TEXT NOT NULL` + check + `,
 sort_time TEXT NOT NULL CHECK (sort_time = occurred_at),
 source_audit_event_id TEXT,
 retain_until TEXT NOT NULL CHECK (length(retain_until) = 30)` + auditForeignKey + `
);
CREATE UNIQUE INDEX idx_activity_source_portfolio ON activity_events(source_audit_event_id, portfolio_id);
CREATE INDEX idx_activity_portfolio_order ON activity_events(portfolio_id, sort_time DESC, id DESC);`
}

func TestMigrateRejectsLedgeredActivitySchemaCorruption(t *testing.T) {
	cases := []struct {
		name   string
		mutate func() string
	}{
		{name: "missing table", mutate: func() string {
			return `PRAGMA foreign_keys=OFF; DROP TRIGGER IF EXISTS activity_events_append_only_update; DROP TRIGGER IF EXISTS activity_events_append_only_delete; DROP INDEX IF EXISTS idx_activity_source_portfolio; DROP INDEX IF EXISTS idx_activity_portfolio_order; DROP TABLE activity_events;`
		}},
		{name: "wrong index columns and uniqueness", mutate: func() string {
			return `DROP INDEX idx_activity_source_portfolio; CREATE INDEX idx_activity_source_portfolio ON activity_events(portfolio_id, source_audit_event_id);`
		}},
		{name: "wrong index direction", mutate: func() string {
			return `DROP INDEX idx_activity_portfolio_order; CREATE INDEX idx_activity_portfolio_order ON activity_events(portfolio_id, sort_time ASC, id ASC);`
		}},
		{name: "missing trigger", mutate: func() string { return `DROP TRIGGER activity_events_append_only_update;` }},
		{name: "bad foreign key", mutate: func() string { return activitySchemaReplacement(false, true) }},
		{name: "bad timestamp check", mutate: func() string { return activitySchemaReplacement(true, false) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrupt.sqlite")
			database, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err = database.Close(); err != nil {
				t.Fatal(err)
			}
			corruptActivitySchema(t, path, testCase.mutate())
			if reopened, reopenErr := Open(context.Background(), path); reopenErr == nil {
				reopened.Close()
				t.Fatal("corrupted ledgered schema was accepted")
			}
		})
	}
}

func TestObservationIDCollisionRequiresExactImmutableReplay(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "collision.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Apply(ctx, approvedPreview(strings.Repeat("c", 64)), "collision.md", strings.Repeat("c", 64), "MYR"); err != nil {
		t.Fatal(err)
	}
	var instrumentID string
	if err = s.DB.QueryRow(`SELECT id FROM instruments WHERE symbol='SYMA' AND currency='USD'`).Scan(&instrumentID); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	quoteInput := QuoteInput{ID: "collision-quote", InstrumentID: instrumentID, Currency: "USD", Price: "2", Source: "fixture", MarketAt: at, FetchedAt: at, Basis: "close", Provenance: "first"}
	first, err := s.AddQuote(ctx, quoteInput)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.AddQuote(ctx, quoteInput); err != nil || replay.ID != first.ID {
		t.Fatalf("exact quote replay = %+v, %v", replay, err)
	}
	quoteInput.Price = "3"
	if _, err := s.AddQuote(ctx, quoteInput); !errors.Is(err, ErrObservationIDConflict) {
		t.Fatalf("mismatched quote collision error = %v", err)
	}

	fxInput := FXInput{ID: "collision-fx", BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", Source: "fixture", MarketAt: at, FetchedAt: at, Basis: "close", Provenance: "first"}
	if _, err := s.AddFXRate(ctx, fxInput); err != nil {
		t.Fatal(err)
	}
	fxInput.Rate = "5"
	if _, err := s.AddFXRate(ctx, fxInput); !errors.Is(err, ErrObservationIDConflict) {
		t.Fatalf("mismatched FX collision error = %v", err)
	}
}

func TestObservationTimestampsShareEpochBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "timestamps.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Apply(ctx, approvedPreview(strings.Repeat("t", 64)), "timestamps.md", strings.Repeat("t", 64), "MYR"); err != nil {
		t.Fatal(err)
	}
	var instrumentID string
	if err = s.DB.QueryRow(`SELECT id FROM instruments WHERE symbol='SYMA' AND currency='USD'`).Scan(&instrumentID); err != nil {
		t.Fatal(err)
	}
	before := "1969-12-31T23:59:59.999999999Z"
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "before-quote", InstrumentID: instrumentID, Currency: "USD", Price: "2", Source: "fixture", MarketAt: before, FetchedAt: before, Basis: "close", Provenance: "test"}); err == nil {
		t.Fatal("pre-epoch quote accepted")
	}
	exact := "1970-01-01T00:00:00Z"
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "epoch-quote", InstrumentID: instrumentID, Currency: "USD", Price: "2", Source: "fixture", MarketAt: exact, FetchedAt: exact, Basis: "close", Provenance: "test"}); err != nil {
		t.Fatalf("epoch quote rejected: %v", err)
	}
}

func TestActivitySchemaRejectsImpossibleCalendarValues(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "calendar.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-02T12:00:00Z"
	if _, err = s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "calendar-portfolio", "Calendar", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time,retain_until) VALUES(?,?,?,?,?,?,?,?,?,?)`, "bad-date", "calendar-portfolio", "system", "test", "test", "bad", "", "2026-02-30T12:00:00.000000000Z", "2026-02-30T12:00:00.000000000Z", "2033-01-02T12:00:00.000000000Z")
	if err == nil {
		t.Fatal("impossible calendar date accepted")
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	_, err = s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time) VALUES(?,?,?,?,?,?,?,?,?)`, "bad-date", "calendar-portfolio", "system", "test", "test", "bad", "", "2026-02-30T12:00:00.000000000Z", "2026-02-30T12:00:00.000000000Z")
	if err == nil {
		t.Fatal("impossible calendar date accepted")
	}
}

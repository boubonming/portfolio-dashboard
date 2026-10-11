package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestActivityIsPortfolioScopedAndPaginatesDeterministically(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "activity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"activity-a", "activity-b"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, id, id, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "activity-instrument", "ACT", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?)`, "activity-lot-a", "activity-a", "activity-instrument", "1", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?)`, "activity-lot-b", "activity-b", "activity-instrument", "1", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "activity-quote", InstrumentID: "activity-instrument", Currency: "USD", Price: "1", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddQuote(ctx, QuoteInput{ID: "activity-quote-two", InstrumentID: "activity-instrument", Currency: "USD", Price: "2", Source: "fixture", MarketAt: now, Basis: "close", Provenance: "fixture-two"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Activity(ctx, "activity-a", ActivityPageOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].PortfolioID != "activity-a" || !page.HasMore || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("page=%+v", page)
	}
	page, err = s.Activity(ctx, "activity-a", ActivityPageOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.HasMore {
		t.Fatalf("second page=%+v", page)
	}
	other, err := s.Activity(ctx, "activity-b", ActivityPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Items) != 2 || other.Items[0].PortfolioID != "activity-b" || other.Items[1].PortfolioID != "activity-b" {
		t.Fatalf("other=%+v", other)
	}
}

func TestActivityLegacyAmbiguousRowsFailClosed(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "legacy-activity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range []string{"legacy-a", "legacy-b"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, id, id, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "legacy-instrument", "LEG", "USD", now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-lot-a", "legacy-lot-b"} {
		portfolio := "legacy-a"
		if id == "legacy-lot-b" {
			portfolio = "legacy-b"
		}
		if _, err := s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,created_at) VALUES(?,?,?,?,?,?)`, id, portfolio, "legacy-instrument", "1", "USD", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB.Exec(`INSERT INTO quotes(id,instrument_id,price,currency,source,market_at,fetched_at,basis,provenance) VALUES(?,?,?,?,?,?,?,?,?)`, "legacy-quote", "legacy-instrument", "1", "USD", "fixture", now, now, "close", "redaction-marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, "legacy-audit", "administrative-quote", "market.quote.added", "quote", "legacy-quote", `{"marker":"redaction-marker"}`, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-a", "legacy-b"} {
		page, err := s.Activity(ctx, id, ActivityPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 {
			t.Fatalf("ambiguous row leaked to %s: %+v", id, page.Items)
		}
	}
}

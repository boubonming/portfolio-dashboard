package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
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
	if len(page.Items) != 1 || page.Items[0].PortfolioID != "activity-a" || !page.HasMore || page.NextCursor == nil {
		t.Fatalf("page=%+v", page)
	}
	page, err = s.Activity(ctx, "activity-a", ActivityPageOptions{Limit: 1, Cursor: *page.NextCursor})
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

func TestActivityCursorIsBoundCanonicalAndStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cursor.sqlite")
	secret := []byte("0123456789abcdef0123456789abcdef")
	open := func() *Store {
		store, err := OpenWithActivityCursorSecret(ctx, path, secret)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	s := open()
	now := time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"cursor-a", "cursor-b"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, id, id, "USD", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	for index, id := range []string{"event-old", "event-tie"} {
		occurred := "2026-01-02T11:00:00.000000000Z"
		if index == 1 {
			occurred = "2026-01-02T11:00:00.000000000Z"
		}
		if _, err := s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time) VALUES(?,?,?,?,?,?,?,?,?)`, id, "cursor-a", "system", "portfolio.snapshot.created", "portfolio", id, "", occurred, occurred); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Activity(ctx, "cursor-a", ActivityPageOptions{Limit: 1})
	if err != nil || !page.HasMore || page.NextCursor == nil {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	cursor := *page.NextCursor
	if _, err := s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time) VALUES(?,?,?,?,?,?,?,?,?)`, "event-new", "cursor-a", "system", "portfolio.snapshot.created", "portfolio", "event-new", "", "2026-01-02T12:00:01.000000000Z", "2026-01-02T12:00:01.000000000Z"); err != nil {
		t.Fatal(err)
	}
	next, err := s.Activity(ctx, "cursor-a", ActivityPageOptions{Limit: 1, Cursor: cursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "event-old" {
		t.Fatalf("stable keyset page=%+v err=%v", next, err)
	}
	if _, err := s.Activity(ctx, "cursor-b", ActivityPageOptions{Cursor: cursor}); err != ErrInvalidActivityPagination {
		t.Fatalf("wrong-portfolio cursor err=%v", err)
	}
	if _, err := s.Activity(ctx, "cursor-a", ActivityPageOptions{Cursor: cursor[:len(cursor)-1] + "A"}); err != ErrInvalidActivityPagination {
		t.Fatalf("tampered cursor err=%v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["x"] = "unknown"
	malformed, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	malformedCursor := base64.RawURLEncoding.EncodeToString(malformed)
	if _, err := s.Activity(ctx, "cursor-a", ActivityPageOptions{Cursor: malformedCursor}); err != ErrInvalidActivityPagination {
		t.Fatalf("unknown-field cursor err=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.Close()
	stable, err := s.Activity(ctx, "cursor-a", ActivityPageOptions{Limit: 1, Cursor: cursor})
	if err != nil || len(stable.Items) != 1 || stable.Items[0].ID != "event-old" {
		t.Fatalf("reopened cursor page=%+v err=%v", stable, err)
	}
}

func TestActivityDeepTupleRangePlanIsIndependentOfCursorDepth(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "deep.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-02T12:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "deep-portfolio", "Deep", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2000; index++ {
		id := fmt.Sprintf("deep-event-%04d", index)
		if _, err := s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time) VALUES(?,?,?,?,?,?,?,?,?)`, id, "deep-portfolio", "system", "portfolio.snapshot.created", "portfolio", id, "", now, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Activity(ctx, "deep-portfolio", ActivityPageOptions{Limit: 20})
	if err != nil || first.NextCursor == nil {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	shallow := *first.NextCursor
	deep := first
	for depth := 0; depth < 90; depth++ {
		deep, err = s.Activity(ctx, "deep-portfolio", ActivityPageOptions{Limit: 20, Cursor: *deep.NextCursor})
		if err != nil || deep.NextCursor == nil {
			t.Fatalf("deep page %d=%+v err=%v", depth, deep, err)
		}
	}
	deepCursor := *deep.NextCursor
	plan := func(encoded string) string {
		cursor, decodeErr := decodeActivityCursorWithKey(s.cursorKey, encoded, "deep-portfolio")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		rows, queryErr := s.DB.Query(`EXPLAIN QUERY PLAN SELECT id FROM activity_events WHERE portfolio_id=? AND (sort_time,id) < (?,?) ORDER BY sort_time DESC,id DESC LIMIT ?`, "deep-portfolio", cursor.SortTime, cursor.ID, 21)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		defer rows.Close()
		var detail string
		for rows.Next() {
			var id, parent, unused string
			if scanErr := rows.Scan(&id, &parent, &unused, &detail); scanErr != nil {
				t.Fatal(scanErr)
			}
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		return detail
	}
	shallowPlan, deepPlan := plan(shallow), plan(deepCursor)
	if shallowPlan != deepPlan || !strings.Contains(deepPlan, "idx_activity_portfolio_order") || !strings.Contains(deepPlan, "sort_time") {
		t.Fatalf("shallow plan=%q deep plan=%q", shallowPlan, deepPlan)
	}
	if len(deep.Items) != 20 {
		t.Fatalf("deep page item count=%d", len(deep.Items))
	}
}

func TestActivityIsAppendOnlyAndUsesBoundedOrderIndex(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "immutable.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-02T12:00:00Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "immutable-portfolio", "Immutable", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,summary,details,occurred_at,sort_time) VALUES(?,?,?,?,?,?,?,?,?)`, "immutable-event", "immutable-portfolio", "system", "portfolio.snapshot.created", "portfolio", "summary", "", "2026-01-02T12:00:00.000000000Z", "2026-01-02T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE activity_events SET summary='changed' WHERE id='immutable-event'`); err == nil {
		t.Fatal("activity update unexpectedly succeeded")
	}
	if _, err := s.DB.Exec(`DELETE FROM activity_events WHERE id='immutable-event'`); err == nil {
		t.Fatal("activity delete unexpectedly succeeded")
	}
	rows, err := s.DB.Query(`EXPLAIN QUERY PLAN SELECT id FROM activity_events WHERE portfolio_id=? AND (sort_time,id) < (?,?) ORDER BY sort_time DESC,id DESC LIMIT ?`, "immutable-portfolio", "2026-01-02T12:00:00.000000000Z", "immutable-event", 21)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var indexed bool
	var ranged bool
	for rows.Next() {
		var id, parent, unused, detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "idx_activity_portfolio_order") {
			indexed = true
		}
		if strings.Contains(detail, "sort_time") && strings.Contains(detail, "<") {
			ranged = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("activity order query did not use the canonical order index")
	}
	if !ranged {
		t.Fatal("activity cursor query did not retain a tuple range constraint")
	}
}

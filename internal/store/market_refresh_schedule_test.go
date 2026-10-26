package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/config"
)

func TestMarketRefreshScheduleBootstrapCASAndSavedDisabled(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "schedule.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "schedule-portfolio", "Schedule", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	fallback := config.MarketRefreshConfig{Cron: "0 5 * * *", Timezone: "Asia/Singapore", PortfolioID: "schedule-portfolio", ReportingCurrency: "USD"}
	initial, err := s.MarketRefreshSchedule(ctx, "schedule-portfolio", fallback)
	if err != nil || initial.Source != "environment" || !initial.Enabled {
		t.Fatalf("initial schedule=%+v err=%v", initial, err)
	}
	actor := MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}
	s.SetClock(func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) })
	saved, err := s.SaveMarketRefreshScheduleWithFallback(ctx, "schedule-portfolio", initial.ETag, MarketRefreshScheduleUpdate{Timezone: "Asia/Singapore"}, actor, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Source != "saved" || saved.Enabled || saved.Cron != "" || saved.Revision != 1 {
		t.Fatalf("saved disabled schedule=%+v", saved)
	}
	if _, err := s.SaveMarketRefreshScheduleWithFallback(ctx, "schedule-portfolio", initial.ETag, MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore"}, actor, fallback); err != ErrMarketRefreshScheduleConflict {
		t.Fatalf("stale save err=%v", err)
	}
	reloaded, err := s.MarketRefreshSchedule(ctx, "schedule-portfolio", config.MarketRefreshConfig{Cron: "0 7 * * *", Timezone: "Asia/Singapore", PortfolioID: "schedule-portfolio"})
	if err != nil || reloaded.Source != "saved" || reloaded.Enabled {
		t.Fatalf("reload=%+v err=%v", reloaded, err)
	}
	var auditCount, activityCount int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='market.refresh.schedule.updated'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE action='market.refresh.schedule.updated' AND portfolio_id='schedule-portfolio'`).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || activityCount != 1 {
		t.Fatalf("audit=%d activity=%d", auditCount, activityCount)
	}
}

func TestMarketRefreshScheduleDatabaseGuardsRejectReplacementAndMisdirectedOwnership(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "schedule-guards.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := "2026-01-01T00:00:00.000000000Z"
	for _, portfolioID := range []string{"guard-portfolio", "other-portfolio"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolioID, portfolioID, "MYR", now, now); err != nil {
			t.Fatal(err)
		}
	}
	fallback := config.MarketRefreshConfig{Cron: "0 5 * * *", Timezone: "Asia/Singapore", PortfolioID: "guard-portfolio", ReportingCurrency: "USD"}
	initial, err := s.MarketRefreshSchedule(ctx, "guard-portfolio", fallback)
	if err != nil {
		t.Fatal(err)
	}
	actor := MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}
	if _, err := s.SaveMarketRefreshScheduleWithFallback(ctx, "guard-portfolio", initial.ETag, MarketRefreshScheduleUpdate{Timezone: "Asia/Singapore"}, actor, fallback); err != nil {
		t.Fatal(err)
	}
	var auditID, payload, retainUntil string
	if err := s.DB.QueryRow(`SELECT id,payload,retain_until FROM audit_events WHERE action='market.refresh.schedule.updated'`).Scan(&auditID, &payload, &retainUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT OR REPLACE INTO market_refresh_schedules(portfolio_id,enabled,cron,timezone,reporting_currency,revision,updated_at,source) VALUES(?,?,?,?,?,?,?,?)`, "guard-portfolio", 1, "0 6 * * *", "Asia/Singapore", "USD", 2, now, "saved"); err == nil {
		t.Fatal("schedule replacement unexpectedly succeeded")
	}
	if _, err := s.DB.Exec(`INSERT OR REPLACE INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, "replacement", "market.refresh.schedule.updated", "market_refresh_schedule", "guard-portfolio", payload, now, retainUntil); err == nil {
		t.Fatal("audit replacement unexpectedly succeeded")
	}
	if _, err := s.DB.Exec(`INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, auditID, "other-portfolio", retainUntil); err == nil {
		t.Fatal("misdirected audit ownership unexpectedly succeeded")
	}
}

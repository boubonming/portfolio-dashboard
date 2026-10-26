package store

import (
	"context"
	"errors"
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

func TestMarketRefreshScheduleValidationAndStaleCASAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "schedule-validation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const now = "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "validation-portfolio", "Validation", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	fallback := config.MarketRefreshConfig{Cron: "0 5 * * *", Timezone: "Asia/Singapore", PortfolioID: "validation-portfolio", ReportingCurrency: "USD"}
	initial, err := s.MarketRefreshSchedule(ctx, "validation-portfolio", fallback)
	if err != nil {
		t.Fatal(err)
	}
	actor := MutationActor{Category: "browser_user", ID: "validation-owner"}
	type testCase struct {
		name   string
		update MarketRefreshScheduleUpdate
		want   error
	}
	valid := MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore", ReportingCurrency: "USD"}
	cases := []testCase{
		{name: "disabled with cron", update: MarketRefreshScheduleUpdate{Cron: "0 6 * * *", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "enabled without cron", update: MarketRefreshScheduleUpdate{Enabled: true, Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "cron whitespace", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: " 0 6 * * *", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "cron macro", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "@daily", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "cron four fields", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * *", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "cron six fields", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 0 6 * * *", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "cron invalid value", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "61 6 * * *", Timezone: "Asia/Singapore"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "invalid IANA timezone", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Not/IANA"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "lower currency", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore", ReportingCurrency: "usd"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "short currency", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore", ReportingCurrency: "US"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "long currency", update: MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore", ReportingCurrency: "USDD"}, want: ErrInvalidMarketRefreshSchedule},
		{name: "stale etag", update: valid, want: ErrMarketRefreshScheduleConflict},
	}
	countEvents := func(table, action string) int {
		t.Helper()
		var count int
		query := `SELECT COUNT(*) FROM ` + table
		args := []any{}
		if action != "" {
			query += ` WHERE action=?`
			args = append(args, action)
		}
		if err := s.DB.QueryRow(query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, tc := range cases {
		beforeAudit := countEvents("audit_events", "market.refresh.schedule.updated")
		beforeActivity := countEvents("activity_events", "market.refresh.schedule.updated")
		etag := initial.ETag
		if tc.name == "stale etag" {
			etag = `"stale"`
		}
		_, gotErr := s.SaveMarketRefreshSchedule(ctx, "validation-portfolio", etag, tc.update, actor)
		if gotErr == nil || !errors.Is(gotErr, tc.want) {
			t.Fatalf("%s error=%v, want %v", tc.name, gotErr, tc.want)
		}
		current, readErr := s.MarketRefreshSchedule(ctx, "validation-portfolio", fallback)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.ETag != initial.ETag || current.Revision != initial.Revision || current.Source != initial.Source || current.Enabled != initial.Enabled || current.Cron != initial.Cron || current.Timezone != initial.Timezone || current.ReportingCurrency != initial.ReportingCurrency {
			t.Fatalf("%s changed schedule: before=%+v after=%+v", tc.name, initial, current)
		}
		if got := countEvents("audit_events", "market.refresh.schedule.updated"); got != beforeAudit {
			t.Fatalf("%s changed audit count from %d to %d", tc.name, beforeAudit, got)
		}
		if got := countEvents("activity_events", "market.refresh.schedule.updated"); got != beforeActivity {
			t.Fatalf("%s changed activity count from %d to %d", tc.name, beforeActivity, got)
		}
	}
}

func TestMarketRefreshScheduleSavedDisabledSurvivesReopenAndCAS(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "schedule-reopen.sqlite")
	s, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	const now = "2026-01-01T00:00:00.000000000Z"
	if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "reopen-portfolio", "Reopen", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	fallback := config.MarketRefreshConfig{Cron: "0 5 * * *", Timezone: "Asia/Singapore", PortfolioID: "reopen-portfolio", ReportingCurrency: "USD"}
	initial, err := s.MarketRefreshSchedule(ctx, "reopen-portfolio", fallback)
	if err != nil {
		t.Fatal(err)
	}
	actor := MutationActor{Category: "browser_user", ID: "reopen-owner"}
	s.SetClock(func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) })
	saved, err := s.SaveMarketRefreshScheduleWithFallback(ctx, "reopen-portfolio", initial.ETag, MarketRefreshScheduleUpdate{Timezone: "Asia/Singapore"}, actor, fallback)
	if err != nil || saved.Enabled || saved.Source != "saved" || saved.Revision != 1 {
		t.Fatalf("saved disabled schedule=%+v err=%v", saved, err)
	}
	var rows int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_schedules WHERE portfolio_id=?`, "reopen-portfolio").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("saved row count=%d err=%v", rows, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reloaded, err := s.MarketRefreshSchedule(ctx, "reopen-portfolio", config.MarketRefreshConfig{Cron: "0 7 * * *", Timezone: "Asia/Singapore", PortfolioID: "reopen-portfolio", ReportingCurrency: "EUR"})
	if err != nil || reloaded.Source != "saved" || reloaded.Enabled || reloaded.Revision != 1 || reloaded.Cron != "" {
		t.Fatalf("reloaded schedule=%+v err=%v", reloaded, err)
	}
	updated, err := s.SaveMarketRefreshSchedule(ctx, "reopen-portfolio", reloaded.ETag, MarketRefreshScheduleUpdate{Enabled: true, Cron: "0 6 * * *", Timezone: "Asia/Singapore", ReportingCurrency: "USD"}, actor)
	if err != nil || !updated.Enabled || updated.Revision != 2 {
		t.Fatalf("CAS update=%+v err=%v", updated, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM market_refresh_schedules WHERE portfolio_id=?`, "reopen-portfolio").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("CAS row count=%d err=%v", rows, err)
	}
}

func TestMarketRefreshScheduleAuditAndOwnershipGuardsCoverBothUpdateDirections(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "schedule-directions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const now = "2026-01-01T00:00:00.000000000Z"
	for _, portfolioID := range []string{"direction-schedule", "direction-other"} {
		if _, err := s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, portfolioID, portfolioID, "USD", now, now); err != nil {
			t.Fatal(err)
		}
	}
	fallback := config.MarketRefreshConfig{Cron: "0 5 * * *", Timezone: "Asia/Singapore", PortfolioID: "direction-schedule", ReportingCurrency: "USD"}
	initial, err := s.MarketRefreshSchedule(ctx, "direction-schedule", fallback)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMarketRefreshScheduleWithFallback(ctx, "direction-schedule", initial.ETag, MarketRefreshScheduleUpdate{Timezone: "Asia/Singapore"}, MutationActor{Category: "browser_user", ID: "direction-owner"}, fallback); err != nil {
		t.Fatal(err)
	}
	var scheduleAuditID, schedulePayload, scheduleRetain string
	if err := s.DB.QueryRow(`SELECT id,payload,retain_until FROM audit_events WHERE action=?`, "market.refresh.schedule.updated").Scan(&scheduleAuditID, &schedulePayload, &scheduleRetain); err != nil {
		t.Fatal(err)
	}
	otherAuditID := "direction-unrelated-audit"
	if _, err := s.DB.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, otherAuditID, "fixture", "unrelated.action", "fixture", "direction-other", `{}`, now, "2033-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, otherAuditID, "direction-other", "2033-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	rejected := func(name, statement string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(statement, args...); err == nil {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
	}
	rejected("audit schedule to unrelated", `UPDATE audit_events SET action=? WHERE id=?`, "unrelated.action", scheduleAuditID)
	rejected("audit unrelated to schedule", `UPDATE audit_events SET action=? WHERE id=?`, "market.refresh.schedule.updated", otherAuditID)
	rejected("audit delete", `DELETE FROM audit_events WHERE id=?`, scheduleAuditID)
	rejected("audit replacement", `INSERT OR REPLACE INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, scheduleAuditID, "replacement", "market.refresh.schedule.updated", "market_refresh_schedule", "direction-schedule", schedulePayload, now, scheduleRetain)
	rejected("audit unrelated replacement into schedule", `INSERT OR REPLACE INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, otherAuditID, "replacement", "market.refresh.schedule.updated", "market_refresh_schedule", "direction-schedule", schedulePayload, scheduleRetain)
	rejected("owner schedule to unrelated", `UPDATE audit_event_portfolios SET audit_event_id=?,portfolio_id=? WHERE audit_event_id=? AND portfolio_id=?`, otherAuditID, "direction-other", scheduleAuditID, "direction-schedule")
	rejected("owner unrelated to schedule", `UPDATE audit_event_portfolios SET audit_event_id=? WHERE audit_event_id=? AND portfolio_id=?`, scheduleAuditID, otherAuditID, "direction-other")
	rejected("owner delete", `DELETE FROM audit_event_portfolios WHERE audit_event_id=? AND portfolio_id=?`, scheduleAuditID, "direction-schedule")
	rejected("owner duplicate", `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, scheduleAuditID, "direction-schedule", scheduleRetain)
	rejected("owner replacement", `INSERT OR REPLACE INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, scheduleAuditID, "direction-other", scheduleRetain)
	rejected("owner misdirected insert", `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, scheduleAuditID, "direction-other", scheduleRetain)

	var action, ownerPortfolio, ownerRetain string
	if err := s.DB.QueryRow(`SELECT action FROM audit_events WHERE id=?`, scheduleAuditID).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "market.refresh.schedule.updated" {
		t.Fatalf("schedule audit action=%q", action)
	}
	if err := s.DB.QueryRow(`SELECT portfolio_id,retain_until FROM audit_event_portfolios WHERE audit_event_id=?`, scheduleAuditID).Scan(&ownerPortfolio, &ownerRetain); err != nil {
		t.Fatal(err)
	}
	if ownerPortfolio != "direction-schedule" || ownerRetain != scheduleRetain {
		t.Fatalf("schedule owner portfolio=%q retain=%q", ownerPortfolio, ownerRetain)
	}
	if err := s.DB.QueryRow(`SELECT action FROM audit_events WHERE id=?`, otherAuditID).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "unrelated.action" {
		t.Fatalf("unrelated audit action=%q", action)
	}
}

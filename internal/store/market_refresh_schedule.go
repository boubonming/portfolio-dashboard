package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/config"
)

var (
	ErrMarketRefreshScheduleConflict = errors.New("market refresh schedule conflict")
	ErrInvalidMarketRefreshSchedule  = errors.New("invalid market refresh schedule")
)

type MarketRefreshSchedule struct {
	PortfolioID       string `json:"portfolio_id"`
	Enabled           bool   `json:"enabled"`
	Cron              string `json:"cron"`
	Timezone          string `json:"timezone"`
	ReportingCurrency string `json:"reporting_currency"`
	Revision          int    `json:"revision"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	Source            string `json:"source"`
	ETag              string `json:"etag"`
}

type MarketRefreshScheduleUpdate struct {
	Enabled           bool
	Cron              string
	Timezone          string
	ReportingCurrency string
}

// MarketRefreshSchedule returns the durable browser setting when present and
// otherwise the validated deployment setting. The fallback is never written.
func (s *Store) MarketRefreshSchedule(ctx context.Context, portfolioID string, fallback config.MarketRefreshConfig) (MarketRefreshSchedule, error) {
	var portfolioExists int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM portfolios WHERE id=?`, portfolioID).Scan(&portfolioExists); err != nil {
		return MarketRefreshSchedule{}, err
	}
	if portfolioExists != 1 {
		return MarketRefreshSchedule{}, ErrPortfolioNotFound
	}
	var schedule MarketRefreshSchedule
	var enabled int
	var source string
	err := s.DB.QueryRowContext(ctx, `SELECT enabled,cron,timezone,reporting_currency,revision,updated_at,source FROM market_refresh_schedules WHERE portfolio_id=?`, portfolioID).Scan(&enabled, &schedule.Cron, &schedule.Timezone, &schedule.ReportingCurrency, &schedule.Revision, &schedule.UpdatedAt, &source)
	if err == nil {
		schedule.PortfolioID = portfolioID
		schedule.Enabled = enabled == 1
		schedule.Source = source
		schedule.ETag = marketRefreshScheduleETag(schedule)
		return schedule, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MarketRefreshSchedule{}, err
	}
	parsed, err := config.ParseMarketRefreshConfig(fallback.Cron, fallback.Timezone, portfolioID, fallback.ReportingCurrency)
	if err != nil {
		return MarketRefreshSchedule{}, err
	}
	schedule = MarketRefreshSchedule{
		PortfolioID:       portfolioID,
		Enabled:           parsed.Enabled(),
		Cron:              parsed.Cron,
		Timezone:          parsed.Timezone,
		ReportingCurrency: parsed.ReportingCurrency,
		Source:            "environment",
	}
	schedule.ETag = marketRefreshScheduleETag(schedule)
	return schedule, nil
}

// SavedMarketRefreshSchedules lists only durable rows so startup can restore
// every portfolio without materializing environment fallbacks as writes.
func (s *Store) SavedMarketRefreshSchedules(ctx context.Context) ([]MarketRefreshSchedule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT portfolio_id,enabled,cron,timezone,reporting_currency,revision,updated_at,source FROM market_refresh_schedules ORDER BY portfolio_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []MarketRefreshSchedule
	for rows.Next() {
		var schedule MarketRefreshSchedule
		var enabled int
		if err := rows.Scan(&schedule.PortfolioID, &enabled, &schedule.Cron, &schedule.Timezone, &schedule.ReportingCurrency, &schedule.Revision, &schedule.UpdatedAt, &schedule.Source); err != nil {
			return nil, err
		}
		schedule.Enabled = enabled == 1
		schedule.ETag = marketRefreshScheduleETag(schedule)
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}

func marketRefreshScheduleETag(schedule MarketRefreshSchedule) string {
	value := strings.Join([]string{
		schedule.PortfolioID,
		strconv.FormatBool(schedule.Enabled),
		schedule.Cron,
		schedule.Timezone,
		schedule.ReportingCurrency,
		strconv.Itoa(schedule.Revision),
		schedule.UpdatedAt,
		schedule.Source,
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// SaveMarketRefreshSchedule performs validation, compare-and-swap, durable
// persistence, and the portfolio-owned audit/activity writes in one SQLite
// transaction. It never records invalid or stale attempts.
func (s *Store) SaveMarketRefreshSchedule(ctx context.Context, portfolioID, expectedETag string, update MarketRefreshScheduleUpdate, actor MutationActor) (MarketRefreshSchedule, error) {
	return s.SaveMarketRefreshScheduleWithFallback(ctx, portfolioID, expectedETag, update, actor, config.MarketRefreshConfig{Timezone: "Asia/Singapore", PortfolioID: portfolioID})
}

func (s *Store) SaveMarketRefreshScheduleWithFallback(ctx context.Context, portfolioID, expectedETag string, update MarketRefreshScheduleUpdate, actor MutationActor, fallback config.MarketRefreshConfig) (MarketRefreshSchedule, error) {
	if portfolioID == "" || actor.Category != "browser_user" || actor.ID == "" {
		return MarketRefreshSchedule{}, ErrInvalidMarketRefreshSchedule
	}
	if !update.Enabled && update.Cron != "" || update.Enabled && update.Cron == "" {
		return MarketRefreshSchedule{}, ErrInvalidMarketRefreshSchedule
	}
	parsed, err := config.ParseMarketRefreshConfig(update.Cron, update.Timezone, portfolioID, update.ReportingCurrency)
	if err != nil {
		return MarketRefreshSchedule{}, fmt.Errorf("%w: %v", ErrInvalidMarketRefreshSchedule, err)
	}
	if expectedETag == "" {
		return MarketRefreshSchedule{}, ErrMarketRefreshScheduleConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return MarketRefreshSchedule{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM portfolios WHERE id=?`, portfolioID).Scan(&exists); err != nil {
		return MarketRefreshSchedule{}, err
	}
	if exists != 1 {
		return MarketRefreshSchedule{}, ErrPortfolioNotFound
	}
	var current MarketRefreshSchedule
	var enabled int
	var source string
	err = tx.QueryRowContext(ctx, `SELECT enabled,cron,timezone,reporting_currency,revision,updated_at,source FROM market_refresh_schedules WHERE portfolio_id=?`, portfolioID).Scan(&enabled, &current.Cron, &current.Timezone, &current.ReportingCurrency, &current.Revision, &current.UpdatedAt, &source)
	if errors.Is(err, sql.ErrNoRows) {
		parsedFallback, parseErr := config.ParseMarketRefreshConfig(fallback.Cron, fallback.Timezone, portfolioID, fallback.ReportingCurrency)
		err = parseErr
		if err == nil {
			current = MarketRefreshSchedule{PortfolioID: portfolioID, Enabled: parsedFallback.Enabled(), Cron: parsedFallback.Cron, Timezone: parsedFallback.Timezone, ReportingCurrency: parsedFallback.ReportingCurrency, Source: "environment"}
			current.ETag = marketRefreshScheduleETag(current)
		}
		if err != nil {
			return MarketRefreshSchedule{}, err
		}
	} else if err != nil {
		return MarketRefreshSchedule{}, err
	} else {
		current.PortfolioID = portfolioID
		current.Enabled = enabled == 1
		current.Source = source
		current.ETag = marketRefreshScheduleETag(current)
	}
	if current.ETag != expectedETag {
		return MarketRefreshSchedule{}, ErrMarketRefreshScheduleConflict
	}
	now := canonicalSnapshotTime(s.now())
	newRevision := current.Revision + 1
	if newRevision < 1 {
		newRevision = 1
	}
	newSchedule := MarketRefreshSchedule{
		PortfolioID:       portfolioID,
		Enabled:           parsed.Enabled(),
		Cron:              parsed.Cron,
		Timezone:          parsed.Timezone,
		ReportingCurrency: parsed.ReportingCurrency,
		Revision:          newRevision,
		UpdatedAt:         now,
		Source:            "saved",
	}
	if current.Source == "environment" {
		_, err = tx.ExecContext(ctx, `INSERT INTO market_refresh_schedules(portfolio_id,enabled,cron,timezone,reporting_currency,revision,updated_at,source) VALUES(?,?,?,?,?,?,?,?)`, portfolioID, scheduleBoolInt(newSchedule.Enabled), newSchedule.Cron, newSchedule.Timezone, newSchedule.ReportingCurrency, newSchedule.Revision, newSchedule.UpdatedAt, newSchedule.Source)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE market_refresh_schedules SET enabled=?,cron=?,timezone=?,reporting_currency=?,revision=?,updated_at=?,source=? WHERE portfolio_id=? AND revision=?`, scheduleBoolInt(newSchedule.Enabled), newSchedule.Cron, newSchedule.Timezone, newSchedule.ReportingCurrency, newSchedule.Revision, newSchedule.UpdatedAt, newSchedule.Source, portfolioID, current.Revision)
	}
	if err != nil {
		return MarketRefreshSchedule{}, err
	}
	newSchedule.ETag = marketRefreshScheduleETag(newSchedule)
	auditID := deterministicID("audit", "market-refresh-schedule", portfolioID, strconv.Itoa(newRevision))
	payload, _ := json.Marshal(map[string]any{
		"id": auditID, "action": "market.refresh.schedule.updated", "principal_category": actor.Category,
		"principal_id": actor.ID, "portfolio_id": portfolioID, "revision": newRevision, "created_at": now,
	})
	retainUntil := canonicalSnapshotTime(s.now().AddDate(7, 0, 0))
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, actor.Category+"-"+actor.ID, "market.refresh.schedule.updated", "market_refresh_schedule", portfolioID, payload, now, retainUntil); err != nil {
		return MarketRefreshSchedule{}, err
	}
	if err := recordActivity(ctx, tx, portfolioID, actor.Category, "market.refresh.schedule.updated", "market_data", "market_refresh_schedule", portfolioID, "Market refresh schedule updated", "", now, auditID); err != nil {
		return MarketRefreshSchedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return MarketRefreshSchedule{}, err
	}
	return newSchedule, nil
}

func scheduleBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

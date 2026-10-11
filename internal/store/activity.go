package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const (
	DefaultActivityLimit = 20
	MaxActivityLimit     = 100
	MaxActivityOffset    = 100000
)

var ErrInvalidActivityPagination = errors.New("invalid activity pagination")

type ActivityPageOptions struct {
	Limit  int
	Offset int
}

// RecordActivity writes one safe summary for one explicit portfolio. Callers
// must invoke this in the same transaction as the source write.
func recordActivity(ctx context.Context, tx *sql.Tx, portfolioID, actor, action, category, entityType, entityID, summary, details, occurredAt, sourceAuditID string) error {
	if strings.TrimSpace(portfolioID) == "" {
		return errors.New("activity portfolio is required")
	}
	id := deterministicID("activity", sourceAuditID, portfolioID)
	_, err := tx.ExecContext(ctx, `INSERT INTO activity_events
		(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,source_audit_event_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, portfolioID, actor, action, category,
		nullableStringValue(entityType), nullableStringValue(entityID), summary, details, occurredAt, nullableStringValue(sourceAuditID))
	return err
}

func nullableStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func activitySummary(action string) (string, string, string) {
	switch action {
	case "portfolio.import.approved":
		return "import", "Approved portfolio import", "An approved import was recorded."
	case "market.quote.added":
		return "market", "Manual quote added", "A manual market quote was recorded."
	case "market.fx.added":
		return "market", "Manual FX rate added", "A manual foreign-exchange observation was recorded."
	case "market.mapping.added":
		return "market", "Provider mapping added", "A provider instrument mapping was recorded."
	case "market.refresh.completed":
		return "market", "Market refresh completed", "A market-data refresh completed."
	case "portfolio.snapshot.created":
		return "portfolio", "Immutable snapshot created", "An immutable valuation snapshot was created."
	default:
		return "other", "Portfolio activity recorded", "A portfolio activity event was recorded."
	}
}

func (s *Store) recordActivityForPortfolios(ctx context.Context, tx *sql.Tx, portfolioIDs []string, actor, action, entityType, entityID, occurredAt, auditID string) error {
	category, summary, details := activitySummary(action)
	seen := make(map[string]bool, len(portfolioIDs))
	for _, portfolioID := range portfolioIDs {
		if seen[portfolioID] || strings.TrimSpace(portfolioID) == "" {
			continue
		}
		seen[portfolioID] = true
		if err := recordActivity(ctx, tx, portfolioID, actor, action, category, entityType, entityID, summary, details, occurredAt, auditID); err != nil {
			return err
		}
	}
	return nil
}

func portfoliosForInstrument(ctx context.Context, tx *sql.Tx, instrumentID string) ([]string, error) {
	return queryPortfolioIDs(ctx, tx, `SELECT DISTINCT portfolio_id FROM lots WHERE instrument_id = ? ORDER BY portfolio_id`, instrumentID)
}

func portfoliosForFX(ctx context.Context, tx *sql.Tx, base, quote string) ([]string, error) {
	return queryPortfolioIDs(ctx, tx, `SELECT DISTINCT p.id
		FROM portfolios p JOIN lots l ON l.portfolio_id=p.id
		WHERE l.currency=? AND p.reporting_currency=? ORDER BY p.id`, base, quote)
}

func queryPortfolioIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var IDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		IDs = append(IDs, id)
	}
	return IDs, rows.Err()
}

// Activity returns deterministic newest-first activity. Legacy audit rows are
// included only where their portfolio can be derived uniquely from relational
// data; ambiguous rows are intentionally omitted.
func (s *Store) Activity(ctx context.Context, portfolioID string, options ActivityPageOptions) (domain.ActivityPage, error) {
	if strings.TrimSpace(portfolioID) == "" {
		return domain.ActivityPage{}, ErrPortfolioNotFound
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id=?", portfolioID).Scan(&exists); err != nil {
		return domain.ActivityPage{}, err
	}
	if exists == 0 {
		return domain.ActivityPage{}, ErrPortfolioNotFound
	}
	if options.Limit == 0 {
		options.Limit = DefaultActivityLimit
	}
	if options.Limit < 1 || options.Limit > MaxActivityLimit || options.Offset < 0 || options.Offset > MaxActivityOffset {
		return domain.ActivityPage{}, ErrInvalidActivityPagination
	}
	query := legacyActivityQuery()
	args := []any{portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, options.Limit + 1, options.Offset}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ActivityPage{}, fmt.Errorf("read activity: %w", err)
	}
	defer rows.Close()
	page := domain.ActivityPage{Items: make([]domain.Activity, 0, options.Limit)}
	for rows.Next() {
		var item domain.Activity
		if err := rows.Scan(&item.ID, &item.PortfolioID, &item.ActorCategory, &item.Action, &item.Category, &item.EntityType, &item.EntityID, &item.OccurredAt, &item.Summary, &item.Details); err != nil {
			return domain.ActivityPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.ActivityPage{}, err
	}
	if len(page.Items) > options.Limit {
		page.Items = page.Items[:options.Limit]
		page.HasMore = true
		next := options.Offset + options.Limit
		page.NextOffset = &next
	}
	return page, nil
}

func legacyActivityQuery() string {
	return `WITH all_activity AS (
		SELECT id, portfolio_id, actor_category, action, category, entity_type, entity_id, summary, details, occurred_at
		FROM activity_events WHERE portfolio_id = ?
		UNION ALL
		SELECT ae.id, ?,
			CASE WHEN ae.actor LIKE 'administrative-%' THEN 'administrative' ELSE ae.actor END,
			ae.action,
			CASE WHEN ae.action LIKE 'portfolio.%' THEN 'portfolio' WHEN ae.action LIKE 'market.%' THEN 'market' ELSE 'other' END,
			ae.entity_type, ae.entity_id,
			CASE ae.action
				WHEN 'portfolio.import.approved' THEN 'Approved portfolio import'
				WHEN 'market.quote.added' THEN 'Manual quote added'
				WHEN 'market.fx.added' THEN 'Manual FX rate added'
				WHEN 'market.mapping.added' THEN 'Provider mapping added'
				WHEN 'market.refresh.completed' THEN 'Market refresh completed'
				WHEN 'portfolio.snapshot.created' THEN 'Immutable snapshot created'
				ELSE 'Portfolio activity recorded' END,
			'', ae.created_at
		FROM audit_events ae
		WHERE ae.action IN ('portfolio.import.approved','market.quote.added','market.fx.added','market.mapping.added','market.refresh.completed','portfolio.snapshot.created')
		  AND NOT EXISTS (SELECT 1 FROM activity_events newer WHERE newer.source_audit_event_id=ae.id)
		  AND (
			(ae.action='portfolio.import.approved' AND ae.entity_type='import_run' AND (SELECT portfolio_id FROM import_runs WHERE id=ae.entity_id)=?)
			OR (ae.action='market.refresh.completed' AND ae.entity_type='refresh_run' AND (SELECT portfolio_id FROM refresh_runs WHERE id=ae.entity_id)=?)
			OR (ae.action='portfolio.snapshot.created' AND ae.entity_type='snapshot' AND (SELECT portfolio_id FROM snapshots WHERE id=ae.entity_id)=?)
			OR (ae.action='market.quote.added' AND (SELECT COUNT(DISTINCT l.portfolio_id) FROM lots l JOIN quotes q ON q.instrument_id=l.instrument_id WHERE q.id=ae.entity_id)=1 AND (SELECT MIN(l.portfolio_id) FROM lots l JOIN quotes q ON q.instrument_id=l.instrument_id WHERE q.id=ae.entity_id)=?)
			OR (ae.action='market.mapping.added' AND (SELECT COUNT(DISTINCT l.portfolio_id) FROM lots l JOIN instrument_provider_mappings m ON m.instrument_id=l.instrument_id WHERE m.id=ae.entity_id)=1 AND (SELECT MIN(l.portfolio_id) FROM lots l JOIN instrument_provider_mappings m ON m.instrument_id=l.instrument_id WHERE m.id=ae.entity_id)=?)
			OR (ae.action='market.fx.added' AND (SELECT COUNT(DISTINCT p.id) FROM portfolios p JOIN lots l ON l.portfolio_id=p.id JOIN fx_rates f ON f.id=ae.entity_id WHERE l.currency=f.base_currency AND p.reporting_currency=f.quote_currency)=1 AND (SELECT MIN(p.id) FROM portfolios p JOIN lots l ON l.portfolio_id=p.id JOIN fx_rates f ON f.id=ae.entity_id WHERE l.currency=f.base_currency AND p.reporting_currency=f.quote_currency)=?)
		  )
	)
	SELECT id,portfolio_id,actor_category,action,category,COALESCE(entity_type,''),COALESCE(entity_id,''),occurred_at,summary,details
	FROM all_activity ORDER BY occurred_at DESC, id DESC LIMIT ? OFFSET ?`
}

func ParseActivityPagination(limit, offset string) (ActivityPageOptions, error) {
	options := ActivityPageOptions{Limit: DefaultActivityLimit}
	if limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil || parsed < 1 || parsed > MaxActivityLimit {
			return options, ErrInvalidActivityPagination
		}
		options.Limit = parsed
	}
	if offset != "" {
		parsed, err := strconv.Atoi(offset)
		if err != nil || parsed < 0 || parsed > MaxActivityOffset {
			return options, ErrInvalidActivityPagination
		}
		options.Offset = parsed
	}
	return options, nil
}

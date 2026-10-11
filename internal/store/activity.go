package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const (
	DefaultActivityLimit  = 20
	MaxActivityLimit      = 100
	activityCursorVersion = 1
)

var ErrInvalidActivityPagination = errors.New("invalid activity pagination")

type ActivityPageOptions struct {
	Limit  int
	Cursor string
	Offset int // deprecated internal compatibility; never parsed from HTTP
}

type activityCursor struct {
	Version    int    `json:"v"`
	OccurredAt string `json:"o"`
	ID         string `json:"i"`
	MAC        string `json:"m"`
}

type activityCursorPayload struct {
	Version    int    `json:"v"`
	OccurredAt string `json:"o"`
	ID         string `json:"i"`
}

var activityCursorKey = []byte("portfolio-dashboard/activity-cursor/v1")

func encodeActivityCursor(occurredAt, id string) (string, error) {
	payload := activityCursorPayload{Version: activityCursorVersion, OccurredAt: occurredAt, ID: id}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, activityCursorKey)
	_, _ = h.Write(raw)
	cursor := activityCursor{Version: payload.Version, OccurredAt: payload.OccurredAt, ID: payload.ID, MAC: base64.RawURLEncoding.EncodeToString(h.Sum(nil))}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeActivityCursor(encoded string) (activityCursor, error) {
	if len(encoded) < 16 || len(encoded) > 512 {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	var cursor activityCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Version != activityCursorVersion || cursor.OccurredAt == "" || cursor.ID == "" || cursor.MAC == "" {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	canonical, err := encodeActivityCursor(cursor.OccurredAt, cursor.ID)
	if err != nil || canonical != encoded {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	return cursor, nil
}

// RecordActivity writes one safe summary for one explicit portfolio. Callers
// must invoke this in the same transaction as the source write.
func recordActivity(ctx context.Context, tx *sql.Tx, portfolioID, actor, action, category, entityType, entityID, summary, details, occurredAt, sourceAuditID string) error {
	if strings.TrimSpace(portfolioID) == "" {
		return errors.New("activity portfolio is required")
	}
	id := deterministicID("activity", sourceAuditID, portfolioID)
	actor = safeActorCategory(actor)
	_, err := tx.ExecContext(ctx, `INSERT INTO activity_events
		(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,source_audit_event_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, portfolioID, actor, action, category,
		nullableStringValue(entityType), nullableStringValue(entityID), summary, details, occurredAt, nullableStringValue(sourceAuditID))
	return err
}

func safeActorCategory(actor string) string {
	actor = strings.ToLower(strings.TrimSpace(actor))
	switch {
	case actor == "administrative" || strings.HasPrefix(actor, "administrative-"):
		return "administrative"
	case actor == "system" || strings.HasPrefix(actor, "system-"):
		return "system"
	case actor == "hermes" || strings.HasPrefix(actor, "hermes-"):
		return "hermes"
	default:
		return "unknown"
	}
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

func validatePortfolioAssociations(provided, expected []string) error {
	set := make(map[string]bool, len(expected))
	for _, id := range expected {
		set[id] = true
	}
	seen := make(map[string]bool, len(provided))
	for _, id := range provided {
		if strings.TrimSpace(id) == "" || seen[id] || !set[id] {
			return errors.New("invalid portfolio association")
		}
		seen[id] = true
	}
	if len(provided) > 0 && len(seen) != len(set) {
		return errors.New("incomplete portfolio association")
	}
	return nil
}

type fxAssociationEdge struct{ from, to string }

func portfoliosForFX(ctx context.Context, tx *sql.Tx, base, quote string) ([]string, error) {
	type portfolio struct {
		id, reporting string
		currencies    []string
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.reporting_currency,l.currency FROM portfolios p JOIN lots l ON l.portfolio_id=p.id ORDER BY p.id,l.currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	portfolios := map[string]*portfolio{}
	for rows.Next() {
		var id, reporting, currency string
		if err := rows.Scan(&id, &reporting, &currency); err != nil {
			return nil, err
		}
		if portfolios[id] == nil {
			portfolios[id] = &portfolio{id: id, reporting: reporting}
		}
		portfolios[id].currencies = append(portfolios[id].currencies, currency)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	edges := []fxAssociationEdge{{from: base, to: quote}}
	fxRows, err := tx.QueryContext(ctx, `SELECT base_currency,quote_currency FROM fx_rates`)
	if err != nil {
		return nil, err
	}
	for fxRows.Next() {
		var from, to string
		if err := fxRows.Scan(&from, &to); err != nil {
			fxRows.Close()
			return nil, err
		}
		edges = append(edges, fxAssociationEdge{from: from, to: to})
	}
	if err := fxRows.Close(); err != nil {
		return nil, err
	}
	connected := func(from, to string) bool {
		seen := map[string]bool{from: true}
		queue := []string{from}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if current == to {
				return true
			}
			for _, edge := range edges {
				if edge.from == current && !seen[edge.to] {
					seen[edge.to] = true
					queue = append(queue, edge.to)
				}
				if edge.to == current && !seen[edge.from] {
					seen[edge.from] = true
					queue = append(queue, edge.from)
				}
			}
		}
		return false
	}
	result := []string{}
	for id, p := range portfolios {
		relevant := false
		for _, currency := range p.currencies {
			if currency != p.reporting && connected(currency, p.reporting) {
				relevant = true
				break
			}
		}
		if relevant {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result, nil
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
	if options.Limit < 1 || options.Limit > MaxActivityLimit {
		return domain.ActivityPage{}, ErrInvalidActivityPagination
	}
	var cursor activityCursor
	if options.Cursor != "" {
		var err error
		cursor, err = decodeActivityCursor(options.Cursor)
		if err != nil {
			return domain.ActivityPage{}, err
		}
	}
	query := legacyActivityQuery()
	args := []any{portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID, portfolioID}
	if options.Cursor != "" {
		query += " WHERE occurred_at < ? OR (occurred_at = ? AND id < ?)"
		args = append(args, cursor.OccurredAt, cursor.OccurredAt, cursor.ID)
	}
	query += " ORDER BY occurred_at DESC, id DESC LIMIT ?"
	args = append(args, options.Limit+1)
	if options.Offset > 0 && options.Cursor == "" {
		query += " OFFSET ?"
		args = append(args, options.Offset)
	}
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
		item.ActorCategory = safeActorCategory(item.ActorCategory)
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.ActivityPage{}, err
	}
	if len(page.Items) > options.Limit {
		page.Items = page.Items[:options.Limit]
		page.HasMore = true
		nextOffset := options.Offset + options.Limit
		page.NextOffset = &nextOffset
		next, err := encodeActivityCursor(page.Items[len(page.Items)-1].OccurredAt, page.Items[len(page.Items)-1].ID)
		if err != nil {
			return domain.ActivityPage{}, err
		}
		page.NextCursor = &next
	}
	return page, nil
}

func legacyActivityQuery() string {
	return `WITH all_activity AS (
		SELECT id, portfolio_id,
			CASE WHEN actor_category IN ('administrative','system','hermes','unknown') THEN actor_category ELSE 'unknown' END AS actor_category,
			action, category, entity_type, entity_id, summary, details, occurred_at
		FROM activity_events WHERE portfolio_id = ?
		UNION ALL
		SELECT ae.id, ?,
			CASE WHEN ae.actor='administrative' OR ae.actor LIKE 'administrative-%' THEN 'administrative'
				WHEN ae.actor='system' OR ae.actor LIKE 'system-%' THEN 'system'
				WHEN ae.actor='hermes' OR ae.actor LIKE 'hermes-%' THEN 'hermes' ELSE 'unknown' END,
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
			OR (ae.action='market.refresh.completed' AND ae.entity_type='refresh_run' AND EXISTS (SELECT 1 FROM refresh_runs rr WHERE (rr.id=ae.entity_id OR rr.refresh_group_id=ae.entity_id) AND rr.portfolio_id=?))
			OR (ae.action='portfolio.snapshot.created' AND ae.entity_type='snapshot' AND (SELECT portfolio_id FROM snapshots WHERE id=ae.entity_id)=?)
			OR (ae.action='market.quote.added' AND (SELECT COUNT(DISTINCT l.portfolio_id) FROM lots l JOIN quotes q ON q.instrument_id=l.instrument_id WHERE q.id=ae.entity_id)=1 AND (SELECT MIN(l.portfolio_id) FROM lots l JOIN quotes q ON q.instrument_id=l.instrument_id WHERE q.id=ae.entity_id)=?)
			OR (ae.action='market.mapping.added' AND (SELECT COUNT(DISTINCT l.portfolio_id) FROM lots l JOIN instrument_provider_mappings m ON m.instrument_id=l.instrument_id WHERE m.id=ae.entity_id)=1 AND (SELECT MIN(l.portfolio_id) FROM lots l JOIN instrument_provider_mappings m ON m.instrument_id=l.instrument_id WHERE m.id=ae.entity_id)=?)
			OR (ae.action='market.fx.added' AND (SELECT COUNT(DISTINCT p.id) FROM portfolios p JOIN lots l ON l.portfolio_id=p.id JOIN fx_rates f ON f.id=ae.entity_id WHERE l.currency=f.base_currency AND p.reporting_currency=f.quote_currency)=1 AND (SELECT MIN(p.id) FROM portfolios p JOIN lots l ON l.portfolio_id=p.id JOIN fx_rates f ON f.id=ae.entity_id WHERE l.currency=f.base_currency AND p.reporting_currency=f.quote_currency)=?)
		  )
		UNION ALL
		SELECT 'legacy-snapshot:' || s.id, s.portfolio_id, 'system', 'portfolio.snapshot.created', 'portfolio', 'snapshot', s.id,
			'Immutable snapshot created', '', s.created_at
		FROM snapshots s
		WHERE s.portfolio_id=?
		  AND NOT EXISTS (SELECT 1 FROM audit_events ae WHERE ae.entity_type='snapshot' AND ae.entity_id=s.id)
	)
	SELECT id,portfolio_id,actor_category,action,category,COALESCE(entity_type,''),COALESCE(entity_id,''),occurred_at,summary,details
	FROM all_activity`
}

func ParseActivityPagination(limit, cursor string) (ActivityPageOptions, error) {
	options := ActivityPageOptions{Limit: DefaultActivityLimit}
	if limit != "" {
		var parsed int
		if _, err := fmt.Sscanf(limit, "%d", &parsed); err != nil || fmt.Sprintf("%d", parsed) != limit || parsed < 1 || parsed > MaxActivityLimit {
			return options, ErrInvalidActivityPagination
		}
		options.Limit = parsed
	}
	if cursor != "" {
		if _, err := decodeActivityCursor(cursor); err != nil {
			return options, err
		}
		options.Cursor = cursor
	}
	return options, nil
}

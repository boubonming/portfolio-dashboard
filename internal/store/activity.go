package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

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
}

type activityCursor struct {
	Version    int    `json:"v"`
	Portfolio  string `json:"p"`
	OccurredAt string `json:"o"`
	ID         string `json:"i"`
	MAC        string `json:"m"`
}

type activityCursorPayload struct {
	Version    int    `json:"v"`
	Portfolio  string `json:"p"`
	OccurredAt string `json:"o"`
	ID         string `json:"i"`
}

func canonicalActivityTime(value string) (string, time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", time.Time{}, ErrInvalidActivityPagination
	}
	parsed = parsed.UTC()
	return parsed.Format(time.RFC3339Nano), parsed, nil
}

func encodeActivityCursorWithKey(key []byte, portfolioID, occurredAt, id string) (string, error) {
	canonical, _, err := canonicalActivityTime(occurredAt)
	if err != nil || strings.TrimSpace(portfolioID) == "" || strings.TrimSpace(id) == "" || len(key) < 32 {
		return "", ErrInvalidActivityPagination
	}
	payload := activityCursorPayload{Version: activityCursorVersion, Portfolio: portfolioID, OccurredAt: canonical, ID: id}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(raw)
	cursor := activityCursor{Version: payload.Version, Portfolio: payload.Portfolio, OccurredAt: payload.OccurredAt, ID: payload.ID, MAC: base64.RawURLEncoding.EncodeToString(h.Sum(nil))}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeActivityCursorWithKey(key []byte, encoded, portfolioID string) (activityCursor, error) {
	if len(encoded) < 16 || len(encoded) > 512 {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	if base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	fields := make(map[string]json.RawMessage, 5)
	for decoder.More() {
		nameToken, err := decoder.Token()
		name, ok := nameToken.(string)
		if err != nil || !ok {
			return activityCursor{}, ErrInvalidActivityPagination
		}
		if _, duplicate := fields[name]; duplicate {
			return activityCursor{}, ErrInvalidActivityPagination
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return activityCursor{}, ErrInvalidActivityPagination
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	if token, err = decoder.Token(); err != io.EOF {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	if len(fields) != 5 {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	canonicalPayload := activityCursorPayload{}
	canonicalCursor := activityCursor{}
	for keyName, value := range fields {
		switch keyName {
		case "v":
			if json.Unmarshal(value, &canonicalCursor.Version) != nil {
				return activityCursor{}, ErrInvalidActivityPagination
			}
		case "p":
			if json.Unmarshal(value, &canonicalCursor.Portfolio) != nil {
				return activityCursor{}, ErrInvalidActivityPagination
			}
		case "o":
			if json.Unmarshal(value, &canonicalCursor.OccurredAt) != nil {
				return activityCursor{}, ErrInvalidActivityPagination
			}
		case "i":
			if json.Unmarshal(value, &canonicalCursor.ID) != nil {
				return activityCursor{}, ErrInvalidActivityPagination
			}
		case "m":
			if json.Unmarshal(value, &canonicalCursor.MAC) != nil {
				return activityCursor{}, ErrInvalidActivityPagination
			}
		default:
			return activityCursor{}, ErrInvalidActivityPagination
		}
	}
	if canonicalCursor.Version != activityCursorVersion || canonicalCursor.Portfolio != portfolioID || canonicalCursor.Portfolio == "" || canonicalCursor.OccurredAt == "" || canonicalCursor.ID == "" || canonicalCursor.MAC == "" || len(key) < 32 {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	canonical, _, err := canonicalActivityTime(canonicalCursor.OccurredAt)
	if err != nil || canonical != canonicalCursor.OccurredAt {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	canonicalPayload = activityCursorPayload{Version: canonicalCursor.Version, Portfolio: canonicalCursor.Portfolio, OccurredAt: canonicalCursor.OccurredAt, ID: canonicalCursor.ID}
	payloadBytes, err := json.Marshal(canonicalPayload)
	if err != nil {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payloadBytes)
	supplied, err := base64.RawURLEncoding.DecodeString(canonicalCursor.MAC)
	if err != nil || len(supplied) != sha256.Size || !hmac.Equal(supplied, mac.Sum(nil)) {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	canonicalEncoded, err := json.Marshal(canonicalCursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonicalEncoded) != encoded {
		return activityCursor{}, ErrInvalidActivityPagination
	}
	return canonicalCursor, nil
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
	uniqueEdges := make([]fxAssociationEdge, 0, len(edges))
	seenEdges := make(map[string]bool, len(edges))
	for _, edge := range edges {
		key := edge.from + "\x00" + edge.to
		reverse := edge.to + "\x00" + edge.from
		if seenEdges[key] || seenEdges[reverse] {
			continue
		}
		seenEdges[key] = true
		uniqueEdges = append(uniqueEdges, edge)
	}
	result := []string{}
	for id, p := range portfolios {
		for _, currency := range p.currencies {
			if currency != p.reporting && uniqueFXPathUsesCandidate(currency, p.reporting, uniqueEdges, 0) {
				result = append(result, id)
				break
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

// The valuation converter rejects ambiguous paths. Activity attribution uses
// the same invariant and additionally requires that the candidate edge is on
// the one possible path.
func uniqueFXPathUsesCandidate(from, to string, edges []fxAssociationEdge, candidate int) bool {
	if from == to || candidate < 0 || candidate >= len(edges) {
		return false
	}
	type state struct {
		currency string
		visited  map[string]bool
		uses     bool
	}
	paths := []state{{currency: from, visited: map[string]bool{from: true}}}
	found, usingCandidate := 0, 0
	for len(paths) > 0 {
		current := paths[0]
		paths = paths[1:]
		if current.currency == to {
			found++
			if current.uses {
				usingCandidate++
			}
			continue
		}
		for index, edge := range edges {
			var next string
			if edge.from == current.currency {
				next = edge.to
			} else if edge.to == current.currency {
				next = edge.from
			} else {
				continue
			}
			if current.visited[next] {
				continue
			}
			visited := make(map[string]bool, len(current.visited)+1)
			for currency := range current.visited {
				visited[currency] = true
			}
			visited[next] = true
			paths = append(paths, state{currency: next, visited: visited, uses: current.uses || index == candidate})
		}
	}
	return found == 1 && usingCandidate == 1
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

// Activity returns the canonical transactional activity read model.
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
		cursor, err = decodeActivityCursorWithKey(s.cursorKey, options.Cursor, portfolioID)
		if err != nil {
			return domain.ActivityPage{}, err
		}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,portfolio_id,actor_category,action,category,
		COALESCE(entity_type,''),COALESCE(entity_id,''),occurred_at,summary,details
		FROM activity_events WHERE portfolio_id = ?`, portfolioID)
	if err != nil {
		return domain.ActivityPage{}, fmt.Errorf("read activity: %w", err)
	}
	defer rows.Close()
	type activityRecord struct {
		item     domain.Activity
		occurred time.Time
	}
	records := make([]activityRecord, 0, options.Limit+1)
	for rows.Next() {
		var item domain.Activity
		if err := rows.Scan(&item.ID, &item.PortfolioID, &item.ActorCategory, &item.Action, &item.Category, &item.EntityType, &item.EntityID, &item.OccurredAt, &item.Summary, &item.Details); err != nil {
			return domain.ActivityPage{}, err
		}
		item.ActorCategory = safeActorCategory(item.ActorCategory)
		canonical, occurred, err := canonicalActivityTime(item.OccurredAt)
		if err != nil {
			return domain.ActivityPage{}, ErrInvalidActivityPagination
		}
		item.OccurredAt = canonical
		records = append(records, activityRecord{item: item, occurred: occurred})
	}
	if err := rows.Err(); err != nil {
		return domain.ActivityPage{}, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].occurred.Equal(records[j].occurred) {
			return records[i].item.ID > records[j].item.ID
		}
		return records[i].occurred.After(records[j].occurred)
	})
	if options.Cursor != "" {
		_, cutoff, _ := canonicalActivityTime(cursor.OccurredAt)
		filtered := records[:0]
		for _, record := range records {
			if record.occurred.Before(cutoff) || (record.occurred.Equal(cutoff) && record.item.ID < cursor.ID) {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	page := domain.ActivityPage{Items: make([]domain.Activity, 0, options.Limit)}
	for _, record := range records {
		if len(page.Items) == options.Limit {
			page.HasMore = true
			break
		}
		page.Items = append(page.Items, record.item)
	}
	if page.HasMore {
		next, err := encodeActivityCursorWithKey(s.cursorKey, portfolioID, page.Items[len(page.Items)-1].OccurredAt, page.Items[len(page.Items)-1].ID)
		if err != nil {
			return domain.ActivityPage{}, err
		}
		page.NextCursor = &next
	}
	return page, nil
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
		if len(cursor) < 16 || len(cursor) > 512 || strings.TrimSpace(cursor) != cursor {
			return options, ErrInvalidActivityPagination
		}
		options.Cursor = cursor
	}
	return options, nil
}

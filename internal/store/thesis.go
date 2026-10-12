package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

const (
	MaxThesisBodyBytes = 64 << 10
	MaxThesisText      = 4000
	MaxThesisListItems = 32
	MaxThesisListText  = 1000
	MaxThesisSources   = 16
	MaxSourceLabel     = 256
	MaxSourceURL       = 2048
)

var (
	ErrThesisNotFound  = errors.New("thesis not found")
	ErrThesisConflict  = errors.New("thesis revision conflict")
	ErrInvalidThesis   = errors.New("invalid thesis")
	ErrHoldingNotFound = errors.New("holding not found")
	isoDatePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	allocationPattern  = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})(\.[0-9]{1,2})?$`)
)

type thesisDocument struct {
	Summary               string                `json:"summary"`
	IntendedHoldingPeriod string                `json:"intended_holding_period"`
	AllocationLimit       *string               `json:"allocation_limit"`
	ReasonsToHold         []string              `json:"reasons_to_hold"`
	Risks                 []string              `json:"risks"`
	ReasonsToSell         []string              `json:"reasons_to_sell"`
	ConditionsToChange    []string              `json:"conditions_to_change"`
	Sources               []domain.ThesisSource `json:"sources"`
}

func normalizeThesisUpdate(input domain.ThesisUpdate) (domain.ThesisUpdate, thesisDocument, error) {
	input.PortfolioID = strings.TrimSpace(input.PortfolioID)
	input.HoldingID = strings.TrimSpace(input.HoldingID)
	if input.PortfolioID == "" || len(input.PortfolioID) > 128 || input.HoldingID == "" || len(input.HoldingID) > 128 || input.ExpectedRevision < 0 {
		return input, thesisDocument{}, fmt.Errorf("%w: invalid scope or revision", ErrInvalidThesis)
	}
	clean := func(value string, max int, required bool) (string, error) {
		if strings.IndexByte(value, 0) >= 0 || len(value) > max || (required && strings.TrimSpace(value) == "") {
			return "", fmt.Errorf("%w: invalid text field", ErrInvalidThesis)
		}
		return value, nil
	}
	var err error
	if input.Summary, err = clean(input.Summary, MaxThesisText, true); err != nil {
		return input, thesisDocument{}, err
	}
	if input.IntendedHoldingPeriod, err = clean(input.IntendedHoldingPeriod, 256, true); err != nil {
		return input, thesisDocument{}, err
	}
	if input.AllocationLimit != nil {
		value := strings.TrimSpace(*input.AllocationLimit)
		if !allocationPattern.MatchString(value) {
			return input, thesisDocument{}, fmt.Errorf("%w: allocation_limit must be a decimal percentage from 0 to 100", ErrInvalidThesis)
		}
		if value == "100.00" || value == "100.0" {
			value = "100"
		}
		if strings.Contains(value, ".") {
			value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
		}
		if value == "" {
			value = "0"
		}
		// The shape allows 000-999; compare without float conversion.
		parts := strings.Split(value, ".")
		if len(parts[0]) == 3 && parts[0] > "100" {
			return input, thesisDocument{}, fmt.Errorf("%w: allocation_limit is above 100", ErrInvalidThesis)
		}
		if parts[0] == "100" && len(parts) == 2 && strings.Trim(parts[1], "0") != "" {
			return input, thesisDocument{}, fmt.Errorf("%w: allocation_limit is above 100", ErrInvalidThesis)
		}
		input.AllocationLimit = &value
	}
	validateList := func(values []string) ([]string, error) {
		if values == nil {
			values = []string{}
		}
		if len(values) > MaxThesisListItems {
			return nil, fmt.Errorf("%w: too many list items", ErrInvalidThesis)
		}
		out := make([]string, len(values))
		for i, value := range values {
			if strings.IndexByte(value, 0) >= 0 || len(value) > MaxThesisListText || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%w: invalid list item", ErrInvalidThesis)
			}
			out[i] = value
		}
		return out, nil
	}
	if input.ReasonsToHold, err = validateList(input.ReasonsToHold); err != nil {
		return input, thesisDocument{}, err
	}
	if input.Risks, err = validateList(input.Risks); err != nil {
		return input, thesisDocument{}, err
	}
	if input.ReasonsToSell, err = validateList(input.ReasonsToSell); err != nil {
		return input, thesisDocument{}, err
	}
	if input.ConditionsToChange, err = validateList(input.ConditionsToChange); err != nil {
		return input, thesisDocument{}, err
	}
	if len(input.Sources) > MaxThesisSources {
		return input, thesisDocument{}, fmt.Errorf("%w: too many sources", ErrInvalidThesis)
	}
	for i := range input.Sources {
		source := &input.Sources[i]
		if strings.TrimSpace(source.Label) == "" || len(source.Label) > MaxSourceLabel || strings.IndexByte(source.Label, 0) >= 0 || len(source.URL) > MaxSourceURL {
			return input, thesisDocument{}, fmt.Errorf("%w: invalid source", ErrInvalidThesis)
		}
		u, parseErr := url.Parse(source.URL)
		if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(source.URL, "\r\n") {
			return input, thesisDocument{}, fmt.Errorf("%w: invalid source URL", ErrInvalidThesis)
		}
		if source.PublicationDate != nil {
			if !isoDatePattern.MatchString(*source.PublicationDate) {
				return input, thesisDocument{}, fmt.Errorf("%w: invalid publication date", ErrInvalidThesis)
			}
			parsed, parseErr := time.Parse("2006-01-02", *source.PublicationDate)
			if parseErr != nil || parsed.Format("2006-01-02") != *source.PublicationDate {
				return input, thesisDocument{}, fmt.Errorf("%w: invalid publication date", ErrInvalidThesis)
			}
		}
	}
	doc := thesisDocument{input.Summary, input.IntendedHoldingPeriod, input.AllocationLimit, input.ReasonsToHold, input.Risks, input.ReasonsToSell, input.ConditionsToChange, input.Sources}
	return input, doc, nil
}

func thesisHash(doc thesisDocument, expectedRevision int64) (string, []byte, error) {
	encoded, err := json.Marshal(struct {
		Document         thesisDocument `json:"document"`
		ExpectedRevision int64          `json:"expected_revision"`
	}{doc, expectedRevision})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), encoded, nil
}

func thesisArrays(raw string, target *thesisDocument) error {
	return json.Unmarshal([]byte(raw), target)
}

func (s *Store) SaveThesis(ctx context.Context, input domain.ThesisUpdate) (domain.Thesis, error) {
	for attempt := 0; attempt < 5; attempt++ {
		result, err := s.saveThesisOnce(ctx, input)
		if err == nil || !isThesisContention(err) {
			return result, err
		}
		if waitErr := waitForThesisRetry(ctx, attempt); waitErr != nil {
			return domain.Thesis{}, err
		}
	}
	return domain.Thesis{}, ErrThesisConflict
}

func isThesisContention(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "unique constraint failed: portfolio_theses.portfolio_id, portfolio_theses.holding_id") || strings.Contains(message, "unique constraint failed: portfolio_theses.id") || strings.Contains(message, "unique constraint failed: thesis_revisions.thesis_id, thesis_revisions.revision") || strings.Contains(message, "unique constraint failed: thesis_revisions.id")
}

func waitForThesisRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Store) saveThesisOnce(ctx context.Context, input domain.ThesisUpdate) (domain.Thesis, error) {
	input, doc, err := normalizeThesisUpdate(input)
	if err != nil {
		return domain.Thesis{}, err
	}
	hash, _, err := thesisHash(doc, input.ExpectedRevision)
	if err != nil {
		return domain.Thesis{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.Thesis{}, err
	}
	defer tx.Rollback()
	var instrumentID string
	if err := tx.QueryRowContext(ctx, `SELECT instrument_id FROM lots WHERE id=? AND portfolio_id=?`, input.HoldingID, input.PortfolioID).Scan(&instrumentID); err == sql.ErrNoRows {
		return domain.Thesis{}, ErrHoldingNotFound
	} else if err != nil {
		return domain.Thesis{}, err
	}
	var thesisID string
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT id,current_revision FROM portfolio_theses WHERE portfolio_id=? AND holding_id=?`, input.PortfolioID, input.HoldingID).Scan(&thesisID, &current)
	if err == sql.ErrNoRows {
		if input.ExpectedRevision != 0 {
			return domain.Thesis{}, ErrThesisConflict
		}
		thesisID = deterministicID("thesis", input.PortfolioID, input.HoldingID)
		current = 0
	} else if err != nil {
		return domain.Thesis{}, err
	}
	if current > 0 {
		var duplicateRevision int64
		if duplicateErr := tx.QueryRowContext(ctx, `SELECT revision FROM thesis_revisions WHERE thesis_id=? AND request_hash=?`, thesisID, hash).Scan(&duplicateRevision); duplicateErr == nil {
			return readThesisRevisionTx(ctx, tx, thesisID, duplicateRevision)
		} else if duplicateErr != sql.ErrNoRows {
			return domain.Thesis{}, duplicateErr
		}
		if input.ExpectedRevision != current {
			return domain.Thesis{}, ErrThesisConflict
		}
	}
	revision := current + 1
	now := s.now().Format(time.RFC3339Nano)
	revisionID := deterministicID("thesis-revision", thesisID, fmt.Sprint(revision), hash)
	if current == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO portfolio_theses(id,portfolio_id,holding_id,instrument_id,summary,intended_holding_period,allocation_limit,reasons_to_hold,risks,reasons_to_sell,conditions_to_change,sources,current_revision,request_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, thesisID, input.PortfolioID, input.HoldingID, instrumentID, doc.Summary, doc.IntendedHoldingPeriod, nullableThesisString(doc.AllocationLimit), mustJSON(doc.ReasonsToHold), mustJSON(doc.Risks), mustJSON(doc.ReasonsToSell), mustJSON(doc.ConditionsToChange), mustJSON(doc.Sources), revision, hash, now, now)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE portfolio_theses SET summary=?,intended_holding_period=?,allocation_limit=?,reasons_to_hold=?,risks=?,reasons_to_sell=?,conditions_to_change=?,sources=?,current_revision=?,request_hash=?,updated_at=? WHERE id=? AND current_revision=?`, doc.Summary, doc.IntendedHoldingPeriod, nullableThesisString(doc.AllocationLimit), mustJSON(doc.ReasonsToHold), mustJSON(doc.Risks), mustJSON(doc.ReasonsToSell), mustJSON(doc.ConditionsToChange), mustJSON(doc.Sources), revision, hash, now, thesisID, current)
		if err == nil {
			var affected int64
			affected, err = result.RowsAffected()
			if err == nil && affected != 1 {
				err = ErrThesisConflict
			}
		}
	}
	if err != nil {
		return domain.Thesis{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO thesis_revisions(id,thesis_id,portfolio_id,holding_id,instrument_id,revision,summary,intended_holding_period,allocation_limit,reasons_to_hold,risks,reasons_to_sell,conditions_to_change,sources,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, revisionID, thesisID, input.PortfolioID, input.HoldingID, instrumentID, revision, doc.Summary, doc.IntendedHoldingPeriod, nullableThesisString(doc.AllocationLimit), mustJSON(doc.ReasonsToHold), mustJSON(doc.Risks), mustJSON(doc.ReasonsToSell), mustJSON(doc.ConditionsToChange), mustJSON(doc.Sources), hash, now)
	if err != nil {
		return domain.Thesis{}, err
	}
	auditID := deterministicID("audit", "thesis", thesisID, fmt.Sprint(revision))
	payload := fmt.Sprintf(`{"revision":%d,"field_count":8}`, revision)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`, auditID, "administrative-thesis", "portfolio.thesis.updated", "thesis_revision", revisionID, payload, now); err != nil {
		return domain.Thesis{}, err
	}
	if err = recordActivity(ctx, tx, input.PortfolioID, "administrative", "portfolio.thesis.updated", "portfolio", "thesis", thesisID, "Investment thesis updated", "An investment thesis revision was recorded.", now, auditID); err != nil {
		return domain.Thesis{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Thesis{}, err
	}
	return readThesisRevisionDB(ctx, s.DB, thesisID, revision)
}

func CreateOrUpdateThesis(ctx context.Context, s *Store, input domain.ThesisUpdate) (domain.Thesis, error) {
	return s.SaveThesis(ctx, input)
}

func nullableThesisString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }

func decodeThesisArrays(raw string, target any) error {
	if strings.TrimSpace(raw) == "null" || !strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return fmt.Errorf("%w: invalid stored array", ErrInvalidThesis)
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("%w: invalid stored array", ErrInvalidThesis)
	}
	return nil
}

func readThesisRevisionTx(ctx context.Context, tx *sql.Tx, thesisID string, revision int64) (domain.Thesis, error) {
	return scanThesisRevision(ctx, tx.QueryRowContext(ctx, `SELECT t.id,t.portfolio_id,t.holding_id,t.instrument_id,r.id,r.revision,r.summary,r.intended_holding_period,r.allocation_limit,r.reasons_to_hold,r.risks,r.reasons_to_sell,r.conditions_to_change,r.sources,r.created_at FROM thesis_revisions r JOIN portfolio_theses t ON t.id=r.thesis_id WHERE r.thesis_id=? AND r.revision=?`, thesisID, revision))
}

func readThesisRevisionDB(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, thesisID string, revision int64) (domain.Thesis, error) {
	return scanThesisRevision(ctx, db.QueryRowContext(ctx, `SELECT t.id,t.portfolio_id,t.holding_id,t.instrument_id,r.id,r.revision,r.summary,r.intended_holding_period,r.allocation_limit,r.reasons_to_hold,r.risks,r.reasons_to_sell,r.conditions_to_change,r.sources,r.created_at FROM thesis_revisions r JOIN portfolio_theses t ON t.id=r.thesis_id WHERE r.thesis_id=? AND r.revision=?`, thesisID, revision))
}

func scanThesisRevision(_ context.Context, row *sql.Row) (domain.Thesis, error) {
	var result domain.Thesis
	var allocation sql.NullString
	var reasons, risks, sell, conditions, sources string
	err := row.Scan(&result.ID, &result.PortfolioID, &result.HoldingID, &result.InstrumentID, &result.RevisionID, &result.Revision, &result.Summary, &result.IntendedHoldingPeriod, &allocation, &reasons, &risks, &sell, &conditions, &sources, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.Thesis{}, ErrThesisNotFound
	}
	if err != nil {
		return domain.Thesis{}, err
	}
	if allocation.Valid {
		result.AllocationLimit = &allocation.String
	}
	if err := decodeThesisArrays(reasons, &result.ReasonsToHold); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(risks, &result.Risks); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sell, &result.ReasonsToSell); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(conditions, &result.ConditionsToChange); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sources, &result.Sources); err != nil {
		return domain.Thesis{}, err
	}
	return result, nil
}

func readThesisTx(ctx context.Context, tx *sql.Tx, portfolioID, holdingID string) (domain.Thesis, error) {
	var result domain.Thesis
	var allocation sql.NullString
	var reasons, risks, sell, conditions, sources string
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.portfolio_id,t.holding_id,t.instrument_id,t.summary,t.intended_holding_period,t.allocation_limit,t.reasons_to_hold,t.risks,t.reasons_to_sell,t.conditions_to_change,t.sources,t.current_revision,r.id,t.updated_at FROM portfolio_theses t JOIN thesis_revisions r ON r.thesis_id=t.id AND r.revision=t.current_revision WHERE t.portfolio_id=? AND t.holding_id=?`, portfolioID, holdingID).Scan(&result.ID, &result.PortfolioID, &result.HoldingID, &result.InstrumentID, &result.Summary, &result.IntendedHoldingPeriod, &allocation, &reasons, &risks, &sell, &conditions, &sources, &result.Revision, &result.RevisionID, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.Thesis{}, ErrThesisNotFound
	}
	if err != nil {
		return domain.Thesis{}, err
	}
	if allocation.Valid {
		result.AllocationLimit = &allocation.String
	}
	if err := decodeThesisArrays(reasons, &result.ReasonsToHold); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(risks, &result.Risks); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sell, &result.ReasonsToSell); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(conditions, &result.ConditionsToChange); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sources, &result.Sources); err != nil {
		return domain.Thesis{}, err
	}
	return result, nil
}

func (s *Store) GetThesis(ctx context.Context, portfolioID, holdingID string) (domain.Thesis, error) {
	portfolioID, holdingID = strings.TrimSpace(portfolioID), strings.TrimSpace(holdingID)
	if portfolioID == "" || holdingID == "" {
		return domain.Thesis{}, ErrThesisNotFound
	}
	return readThesisDB(ctx, s.DB, portfolioID, holdingID)
}

func readThesisDB(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, portfolioID, holdingID string) (domain.Thesis, error) {
	var result domain.Thesis
	var allocation sql.NullString
	var reasons, risks, sell, conditions, sources string
	err := db.QueryRowContext(ctx, `SELECT t.id,t.portfolio_id,t.holding_id,t.instrument_id,t.summary,t.intended_holding_period,t.allocation_limit,t.reasons_to_hold,t.risks,t.reasons_to_sell,t.conditions_to_change,t.sources,t.current_revision,r.id,t.updated_at FROM portfolio_theses t JOIN thesis_revisions r ON r.thesis_id=t.id AND r.revision=t.current_revision WHERE t.portfolio_id=? AND t.holding_id=?`, portfolioID, holdingID).Scan(&result.ID, &result.PortfolioID, &result.HoldingID, &result.InstrumentID, &result.Summary, &result.IntendedHoldingPeriod, &allocation, &reasons, &risks, &sell, &conditions, &sources, &result.Revision, &result.RevisionID, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return domain.Thesis{}, ErrThesisNotFound
	}
	if err != nil {
		return domain.Thesis{}, err
	}
	if allocation.Valid {
		result.AllocationLimit = &allocation.String
	}
	if err := decodeThesisArrays(reasons, &result.ReasonsToHold); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(risks, &result.Risks); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sell, &result.ReasonsToSell); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(conditions, &result.ConditionsToChange); err != nil {
		return domain.Thesis{}, err
	}
	if err := decodeThesisArrays(sources, &result.Sources); err != nil {
		return domain.Thesis{}, err
	}
	return result, nil
}

func (s *Store) ThesisHistory(ctx context.Context, portfolioID, holdingID string) ([]domain.ThesisRevision, error) {
	if _, err := s.GetThesis(ctx, portfolioID, holdingID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id,r.thesis_id,t.portfolio_id,t.holding_id,t.instrument_id,r.revision,r.summary,r.intended_holding_period,r.allocation_limit,r.reasons_to_hold,r.risks,r.reasons_to_sell,r.conditions_to_change,r.sources,r.created_at FROM thesis_revisions r JOIN portfolio_theses t ON t.id=r.thesis_id WHERE t.portfolio_id=? AND t.holding_id=? ORDER BY r.revision ASC`, portfolioID, holdingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.ThesisRevision{}
	for rows.Next() {
		var item domain.ThesisRevision
		var allocation sql.NullString
		var reasons, risks, sell, conditions, sources string
		if err := rows.Scan(&item.RevisionID, &item.ID, &item.PortfolioID, &item.HoldingID, &item.InstrumentID, &item.Revision, &item.Summary, &item.IntendedHoldingPeriod, &allocation, &reasons, &risks, &sell, &conditions, &sources, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt = item.CreatedAt
		if allocation.Valid {
			item.AllocationLimit = &allocation.String
		}
		if decodeThesisArrays(reasons, &item.ReasonsToHold) != nil || decodeThesisArrays(risks, &item.Risks) != nil || decodeThesisArrays(sell, &item.ReasonsToSell) != nil || decodeThesisArrays(conditions, &item.ConditionsToChange) != nil || decodeThesisArrays(sources, &item.Sources) != nil {
			return nil, fmt.Errorf("%w: invalid stored revision", ErrInvalidThesis)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetThesisHistory(ctx context.Context, portfolioID, holdingID string) ([]domain.ThesisRevision, error) {
	return s.ThesisHistory(ctx, portfolioID, holdingID)
}

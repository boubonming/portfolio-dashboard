package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var ErrReviewNotFound = errors.New("review not found")
var ErrReviewCursor = errors.New("invalid review cursor")

type ReviewListFilter struct {
	Limit       int
	Cursor      string
	HoldingID   string
	HasHolding  bool
	Status      string
	HasStatus   bool
	Outdated    bool
	HasOutdated bool
}

type reviewCursorPayload struct {
	PortfolioID string  `json:"portfolio_id"`
	HoldingID   *string `json:"holding_id"`
	Status      *string `json:"status"`
	Outdated    *bool   `json:"outdated"`
	SubmittedAt string  `json:"submitted_at"`
	ReviewID    string  `json:"review_id"`
}

type reviewCursorEnvelope struct {
	Payload json.RawMessage `json:"p"`
	MAC     string          `json:"m"`
}

func (s *Store) ListAdvisoryReviews(ctx context.Context, portfolioID string, filter ReviewListFilter) (domain.ReviewPage, error) {
	if !validID(portfolioID, 128) {
		return domain.ReviewPage{}, ErrReviewNotFound
	}
	if filter.Limit < 1 || filter.Limit > 50 {
		return domain.ReviewPage{}, ErrReviewCursor
	}
	if filter.HasHolding && !validID(filter.HoldingID, 128) {
		return domain.ReviewPage{}, ErrReviewCursor
	}
	if filter.HasStatus && filter.Status != "complete" && filter.Status != "incomplete" {
		return domain.ReviewPage{}, ErrReviewCursor
	}
	if filter.Cursor != "" && len(filter.Cursor) < 16 {
		return domain.ReviewPage{}, ErrReviewCursor
	}

	var portfolioExists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM portfolios WHERE id=?", portfolioID).Scan(&portfolioExists); err != nil {
		return domain.ReviewPage{}, err
	}
	if portfolioExists == 0 {
		return domain.ReviewPage{}, ErrPortfolioNotFound
	}

	var cursor reviewCursorPayload
	var err error
	if filter.Cursor != "" {
		cursor, err = s.decodeReviewCursor(filter.Cursor)
		if err != nil || cursor.PortfolioID != portfolioID || cursor.SubmittedAt == "" || cursor.ReviewID == "" || !cursorMatchesFilter(cursor, filter) {
			return domain.ReviewPage{}, ErrReviewCursor
		}
	}
	current, err := s.currentReviewSnapshot(ctx, portfolioID)
	if err != nil {
		return domain.ReviewPage{}, err
	}

	query := `SELECT r.id,r.submitted_at,r.snapshot_id FROM advisory_reviews r WHERE r.portfolio_id=?`
	args := []any{portfolioID}
	if filter.HasHolding {
		query += ` AND EXISTS (SELECT 1 FROM review_targets t WHERE t.review_id=r.id AND t.portfolio_id=r.portfolio_id AND t.holding_id=?)`
		args = append(args, filter.HoldingID)
	}
	if filter.HasStatus {
		query += ` AND r.historical_status=?`
		args = append(args, filter.Status)
	}
	if filter.HasOutdated {
		if filter.Outdated {
			if current == "" {
				return domain.ReviewPage{Items: []domain.AdvisoryReview{}, HasMore: false}, nil
			}
			query += ` AND r.snapshot_id<>?`
			args = append(args, current)
		} else if current != "" {
			query += ` AND r.snapshot_id=?`
			args = append(args, current)
		}
	}
	if filter.Cursor != "" {
		query += ` AND (r.submitted_at<? OR (r.submitted_at=? AND r.id<?))`
		args = append(args, cursor.SubmittedAt, cursor.SubmittedAt, cursor.ReviewID)
	}
	query += ` ORDER BY r.submitted_at DESC,r.id DESC LIMIT ?`
	args = append(args, filter.Limit+1)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ReviewPage{}, err
	}
	type reviewCandidate struct{ reviewID, submittedAt, snapshotID string }
	candidates := make([]reviewCandidate, 0, filter.Limit+1)
	for rows.Next() {
		var candidate reviewCandidate
		if err := rows.Scan(&candidate.reviewID, &candidate.submittedAt, &candidate.snapshotID); err != nil {
			_ = rows.Close()
			return domain.ReviewPage{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.ReviewPage{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.ReviewPage{}, err
	}
	items := make([]domain.AdvisoryReview, 0, filter.Limit)
	var lastSubmittedAt, lastReviewID string
	for _, candidate := range candidates {
		if !validCanonicalReviewTime(candidate.submittedAt) || !validID(candidate.reviewID, 128) || !validID(candidate.snapshotID, 128) {
			return domain.ReviewPage{}, ErrReviewCorrupt
		}
		item, readErr := reconstructAdvisoryReviewRead(ctx, s.DB, portfolioID, candidate.reviewID, current)
		if readErr != nil {
			return domain.ReviewPage{}, readErr
		}
		if item.SubmittedAt != candidate.submittedAt || item.SnapshotID != candidate.snapshotID {
			return domain.ReviewPage{}, ErrReviewCorrupt
		}
		items = append(items, item)
	}
	page := domain.ReviewPage{Items: items, HasMore: len(items) > filter.Limit}
	if page.HasMore {
		page.Items = page.Items[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		lastSubmittedAt, lastReviewID = last.SubmittedAt, last.ReviewID
		page.NextCursor, err = s.encodeReviewCursor(reviewCursorPayload{
			PortfolioID: portfolioID,
			HoldingID:   optionalString(filter.HasHolding, filter.HoldingID),
			Status:      optionalString(filter.HasStatus, filter.Status),
			Outdated:    optionalBool(filter.HasOutdated, filter.Outdated),
			SubmittedAt: lastSubmittedAt,
			ReviewID:    lastReviewID,
		})
		if err != nil {
			return domain.ReviewPage{}, err
		}
	}
	return page, nil
}

func (s *Store) GetAdvisoryReview(ctx context.Context, portfolioID, reviewID string) (domain.AdvisoryReview, error) {
	if !validID(portfolioID, 128) || !validID(reviewID, 128) {
		return domain.AdvisoryReview{}, ErrReviewNotFound
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM advisory_reviews WHERE id=? AND portfolio_id=?`, reviewID, portfolioID).Scan(&exists); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if exists == 0 {
		return domain.AdvisoryReview{}, ErrReviewNotFound
	}
	current, err := s.currentReviewSnapshot(ctx, portfolioID)
	if err != nil {
		return domain.AdvisoryReview{}, err
	}
	return reconstructAdvisoryReviewRead(ctx, s.DB, portfolioID, reviewID, current)
}

func (s *Store) currentReviewSnapshot(ctx context.Context, portfolioID string) (string, error) {
	// The current snapshot is derived only from normalized snapshot metadata and
	// its review association. Payload JSON is intentionally never read here.
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id,s.calculation_version,s.created_at,rs.portfolio_id,rs.snapshot_id,rs.calculation_version FROM snapshots s LEFT JOIN review_snapshots rs ON rs.portfolio_id=s.portfolio_id AND rs.snapshot_id=s.id WHERE s.portfolio_id=? ORDER BY s.created_at DESC,s.id DESC`, portfolioID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	current := ""
	seen := 0
	for rows.Next() {
		var snapshotID, snapshotVersion, createdAt string
		var associatedPortfolioID, associatedID, associatedVersion sql.NullString
		if err := rows.Scan(&snapshotID, &snapshotVersion, &createdAt, &associatedPortfolioID, &associatedID, &associatedVersion); err != nil {
			return "", err
		}
		if !validID(snapshotID, 128) || !validID(snapshotVersion, 128) || !validCanonicalReviewTime(createdAt) {
			return "", ErrReviewCorrupt
		}
		if associatedPortfolioID.Valid {
			if !validID(associatedPortfolioID.String, 128) || associatedPortfolioID.String != portfolioID || !associatedID.Valid || !associatedVersion.Valid || associatedID.String != snapshotID || associatedVersion.String != snapshotVersion {
				return "", ErrReviewCorrupt
			}
		} else if associatedID.Valid || associatedVersion.Valid {
			return "", ErrReviewCorrupt
		}
		if seen == 0 {
			current = snapshotID
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	var dangling int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_snapshots rs LEFT JOIN snapshots s ON s.portfolio_id=rs.portfolio_id AND s.id=rs.snapshot_id WHERE rs.portfolio_id=? AND s.id IS NULL`, portfolioID).Scan(&dangling); err != nil {
		return "", err
	}
	if dangling != 0 {
		return "", ErrReviewCorrupt
	}
	return current, nil
}

func reconstructAdvisoryReviewRead(ctx context.Context, q reviewQueryer, portfolioID, reviewID, currentSnapshotID string) (domain.AdvisoryReview, error) {
	var result domain.AdvisoryReview
	var scopeType, status, generatedAt, submittedAt, submittedByID, submittedByKind, primaryPermission string
	var primaryEvidenceID, primaryEvidenceAvailability, primaryEvidenceFreshness, primaryFactOrdinal, primaryTargetHoldingID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,portfolio_id,snapshot_id,calculation_version,scope_type,historical_status,recommendation,rationale,model_opinion,model_provider,model_name,model_version,model_runtime,generated_at,submitted_at,submitted_by_id,submitted_by_kind,submitted_by_primary_permission,primary_evidence_id,primary_evidence_availability,primary_evidence_freshness,primary_fact_ordinal,primary_target_holding_id FROM advisory_reviews WHERE id=? AND portfolio_id=?`, reviewID, portfolioID).Scan(&result.ReviewID, &result.PortfolioID, &result.SnapshotID, &result.CalculationVersion, &scopeType, &status, &result.Recommendation, &result.Rationale, &result.ModelOpinion, &result.Model.Provider, &result.Model.Model, &result.Model.Version, &result.Model.Runtime, &generatedAt, &submittedAt, &submittedByID, &submittedByKind, &primaryPermission, &primaryEvidenceID, &primaryEvidenceAvailability, &primaryEvidenceFreshness, &primaryFactOrdinal, &primaryTargetHoldingID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AdvisoryReview{}, ErrReviewNotFound
	}
	if err != nil {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	result.Status = status
	result.GeneratedAt, result.SubmittedAt = generatedAt, submittedAt
	result.Scope = domain.ReviewScope{Type: scopeType, HoldingIDs: []string{}, InstrumentIDs: []string{}}
	result.Risks, result.Counterarguments, result.Conditions = []string{}, []string{}, []string{}
	result.Metrics, result.Evidence, result.SourcedFacts = []domain.ReviewMetric{}, []domain.ReviewEvidence{}, []domain.SourcedFact{}
	if !validID(result.ReviewID, 128) || result.PortfolioID != portfolioID || !validID(result.SnapshotID, 128) || !validID(result.CalculationVersion, 128) || (scopeType != "portfolio" && scopeType != "holding") || (status != "complete" && status != "incomplete") || !validRecommendation(result.Recommendation) || !validReviewText(result.Rationale, 8000) || !validReviewText(result.ModelOpinion, 8000) || !validCanonicalReviewTime(generatedAt) || !validCanonicalReviewTime(submittedAt) || !validID(submittedByID, 128) || submittedByKind != "machine" || primaryPermission != "reviews:write" || !validateReviewModel(result.Model) {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	var linkedCalculationVersion string
	if err := q.QueryRowContext(ctx, `SELECT calculation_version FROM review_snapshots WHERE portfolio_id=? AND snapshot_id=?`, portfolioID, result.SnapshotID).Scan(&linkedCalculationVersion); err != nil || linkedCalculationVersion != result.CalculationVersion {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	if err := loadReviewTargets(ctx, q, portfolioID, reviewID, result.SnapshotID, result.CalculationVersion, &result.Scope); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewTextChildren(ctx, q, "review_risks", "text", portfolioID, reviewID, &result.Risks); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewTextChildren(ctx, q, "review_counterarguments", "text", portfolioID, reviewID, &result.Counterarguments); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewTextChildren(ctx, q, "review_conditions", "text", portfolioID, reviewID, &result.Conditions); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewMetrics(ctx, q, portfolioID, reviewID, &result.Metrics); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewEvidence(ctx, q, portfolioID, reviewID, &result.Evidence); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if err := loadReviewFacts(ctx, q, portfolioID, reviewID, &result.SourcedFacts); err != nil {
		return domain.AdvisoryReview{}, err
	}
	evidenceByID := make(map[string]domain.ReviewEvidence, len(result.Evidence))
	for _, item := range result.Evidence {
		evidenceByID[item.EvidenceID] = item
	}
	for _, fact := range result.SourcedFacts {
		seen := map[string]bool{}
		for _, evidenceID := range fact.EvidenceIDs {
			if seen[evidenceID] || evidenceByID[evidenceID].EvidenceID == "" {
				return domain.AdvisoryReview{}, ErrReviewCorrupt
			}
			seen[evidenceID] = true
		}
	}
	if status == "complete" {
		factOrdinal, parseErr := strconv.Atoi(primaryFactOrdinal.String)
		witness := false
		if parseErr == nil && factOrdinal >= 0 && factOrdinal < len(result.SourcedFacts) && primaryEvidenceID.Valid {
			for _, evidenceID := range result.SourcedFacts[factOrdinal].EvidenceIDs {
				if evidenceID == primaryEvidenceID.String {
					witness = true
					break
				}
			}
		}
		item := evidenceByID[primaryEvidenceID.String]
		if result.Recommendation == "no_recommendation" || !validCompleteEvidence(result.Evidence) || len(result.SourcedFacts) == 0 || !primaryEvidenceID.Valid || primaryEvidenceAvailability.String != "available" || primaryEvidenceFreshness.String != "current" || item.Availability != "available" || item.Freshness != "current" || !primaryFactOrdinal.Valid || !witness {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
	} else if result.Recommendation != "no_recommendation" || primaryEvidenceID.Valid || primaryEvidenceAvailability.Valid || primaryEvidenceFreshness.Valid || primaryFactOrdinal.Valid {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	if scopeType == "holding" {
		if !primaryTargetHoldingID.Valid {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
		found := false
		for _, holdingID := range result.Scope.HoldingIDs {
			if holdingID == primaryTargetHoldingID.String {
				found = true
				break
			}
		}
		if !found {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
	} else if primaryTargetHoldingID.Valid {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}

	permissions := []string{"reviews:write"}
	rows, err := q.QueryContext(ctx, `SELECT ordinal,portfolio_id,permission FROM review_submission_permission_snapshots WHERE review_id=? ORDER BY ordinal`, reviewID)
	if err != nil {
		return domain.AdvisoryReview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal int
		var rowPortfolioID, permission string
		if err := rows.Scan(&ordinal, &rowPortfolioID, &permission); err != nil || rowPortfolioID != portfolioID || ordinal != 1 || permission != "portfolio:read" {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Err(); err != nil || len(permissions) != 2 {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	result.SubmittedBy = domain.PrincipalAttribution{PrincipalID: submittedByID, PrincipalType: "machine", Permissions: permissions}
	result.Lifecycle = domain.ReviewLifecycle{HistoricalStatus: status, Outdated: false}
	if currentSnapshotID != "" {
		if !validID(currentSnapshotID, 128) {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
		current := currentSnapshotID
		result.Lifecycle.CurrentSnapshotID = &current
		result.Lifecycle.Outdated = currentSnapshotID != result.SnapshotID
	}
	return result, nil
}

func cursorMatchesFilter(cursor reviewCursorPayload, filter ReviewListFilter) bool {
	if (cursor.HoldingID != nil) != filter.HasHolding || (cursor.Status != nil) != filter.HasStatus || (cursor.Outdated != nil) != filter.HasOutdated {
		return false
	}
	if filter.HasHolding && *cursor.HoldingID != filter.HoldingID {
		return false
	}
	if filter.HasStatus && *cursor.Status != filter.Status {
		return false
	}
	return !filter.HasOutdated || *cursor.Outdated == filter.Outdated
}

func optionalString(present bool, value string) *string {
	if !present {
		return nil
	}
	return &value
}
func optionalBool(present, value bool) *bool {
	if !present {
		return nil
	}
	return &value
}

func (s *Store) reviewCursorSigningKey() []byte {
	if len(s.reviewCursorKey) != 0 {
		return s.reviewCursorKey
	}
	return s.cursorKey
}

func (s *Store) encodeReviewCursor(payload reviewCursorPayload) (string, error) {
	if !validID(payload.PortfolioID, 128) || !validID(payload.ReviewID, 128) || !validCanonicalReviewTime(payload.SubmittedAt) {
		return "", ErrReviewCursor
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", ErrReviewCursor
	}
	key := s.reviewCursorSigningKey()
	if len(key) < 32 {
		return "", ErrReviewCursor
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("portfolio-dashboard/review-cursor/v1\x00"))
	_, _ = mac.Write(encodedPayload)
	envelopeBytes, err := json.Marshal(reviewCursorEnvelope{Payload: encodedPayload, MAC: base64.RawURLEncoding.EncodeToString(mac.Sum(nil))})
	if err != nil {
		return "", ErrReviewCursor
	}
	token := base64.RawURLEncoding.EncodeToString(envelopeBytes)
	if len(token) < 16 || len(token) > 512 {
		return "", ErrReviewCursor
	}
	return token, nil
}

func (s *Store) decodeReviewCursor(token string) (reviewCursorPayload, error) {
	var result reviewCursorPayload
	if len(token) < 16 || len(token) > 512 || !validCursorToken(token) {
		return result, ErrReviewCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return result, ErrReviewCursor
	}
	var raw map[string]json.RawMessage
	if hasReviewCursorDuplicateKeys(decoded) || json.Unmarshal(decoded, &raw) != nil || len(raw) != 2 {
		return result, ErrReviewCursor
	}
	payloadRaw, okPayload := raw["p"]
	macRaw, okMAC := raw["m"]
	if !okPayload || !okMAC {
		return result, ErrReviewCursor
	}
	var macText string
	if json.Unmarshal(macRaw, &macText) != nil {
		return result, ErrReviewCursor
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(macText)
	if err != nil || len(providedMAC) != sha256.Size || base64.RawURLEncoding.EncodeToString(providedMAC) != macText {
		return result, ErrReviewCursor
	}
	var payloadMap map[string]json.RawMessage
	if hasReviewCursorDuplicateKeys(payloadRaw) || json.Unmarshal(payloadRaw, &payloadMap) != nil || len(payloadMap) != 6 {
		return result, ErrReviewCursor
	}
	for _, name := range []string{"portfolio_id", "holding_id", "status", "outdated", "submitted_at", "review_id"} {
		if _, ok := payloadMap[name]; !ok {
			return result, ErrReviewCursor
		}
	}
	if json.Unmarshal(payloadRaw, &result) != nil {
		return result, ErrReviewCursor
	}
	canonicalPayload, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonicalPayload, payloadRaw) {
		return reviewCursorPayload{}, ErrReviewCursor
	}
	canonicalEnvelope, err := json.Marshal(reviewCursorEnvelope{Payload: canonicalPayload, MAC: macText})
	if err != nil || !bytes.Equal(canonicalEnvelope, decoded) {
		return reviewCursorPayload{}, ErrReviewCursor
	}
	key := s.reviewCursorSigningKey()
	if len(key) < 32 {
		return reviewCursorPayload{}, ErrReviewCursor
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("portfolio-dashboard/review-cursor/v1\x00"))
	_, _ = mac.Write(payloadRaw)
	if subtle.ConstantTimeCompare(mac.Sum(nil), providedMAC) != 1 {
		return reviewCursorPayload{}, ErrReviewCursor
	}
	if !validID(result.PortfolioID, 128) || !validID(result.ReviewID, 128) || !validCanonicalReviewTime(result.SubmittedAt) || (result.Status != nil && *result.Status != "complete" && *result.Status != "incomplete") || (result.HoldingID != nil && !validID(*result.HoldingID, 128)) {
		return reviewCursorPayload{}, ErrReviewCursor
	}
	return result, nil
}

func validCursorToken(value string) bool {
	for _, ch := range value {
		if !(ch >= 'A' && ch <= 'Z') && !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}

func hasReviewCursorDuplicateKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() bool
	walk = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return true
		}
		switch delimiter := token.(type) {
		case json.Delim:
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return true
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return true
					}
					seen[name] = true
					if walk() {
						return true
					}
				}
				_, err = decoder.Token()
				return err != nil
			case '[':
				for decoder.More() {
					if walk() {
						return true
					}
				}
				_, err = decoder.Token()
				return err != nil
			}
		}
		return false
	}
	if walk() {
		return true
	}
	var trailing any
	return decoder.Decode(&trailing) != io.EOF
}

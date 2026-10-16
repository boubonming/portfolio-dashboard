package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

var (
	ErrFeedbackInvalid             = errors.New("invalid review feedback")
	ErrFeedbackNotFound            = errors.New("review feedback review not found")
	ErrFeedbackIdempotencyConflict = errors.New("review feedback idempotency conflict")
	ErrFeedbackCorrupt             = errors.New("review feedback is corrupt")
)

const feedbackTimestampFormat = "2006-01-02T15:04:05.000000000Z"

func FeedbackFingerprint(submission domain.FeedbackSubmission) (string, error) {
	canonical := domain.FeedbackSubmission{Decision: submission.Decision, Note: submission.Note}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateFeedbackSubmission(submission domain.FeedbackSubmission) error {
	switch submission.Decision {
	case "agree", "disagree", "defer", "request_another_review":
	default:
		return ErrFeedbackInvalid
	}
	if submission.Note != nil && !validReviewText(*submission.Note, 2000) {
		return ErrFeedbackInvalid
	}
	return nil
}

// SubmitReviewFeedback appends the immutable feedback graph. The caller must
// pass the stable browser principal, never a session or CSRF value.
func (s *Store) SubmitReviewFeedback(ctx context.Context, browserPrincipalID, portfolioID, reviewID, idempotencyKey, fingerprint string, submission domain.FeedbackSubmission) (domain.ReviewFeedback, bool, error) {
	if browserPrincipalID != "local-owner" || !validReviewID(portfolioID, 128) || !validReviewID(reviewID, 128) || !validIdempotencyKeyStore(idempotencyKey) || len(fingerprint) != 64 {
		return domain.ReviewFeedback{}, false, ErrFeedbackInvalid
	}
	if err := ValidateFeedbackSubmission(submission); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	canonicalFingerprint, err := FeedbackFingerprint(submission)
	if err != nil || !sameSecretText(canonicalFingerprint, fingerprint) {
		return domain.ReviewFeedback{}, false, ErrFeedbackInvalid
	}
	const maxAttempts = 4
	const maxRetryElapsed = 250 * time.Millisecond
	started := time.Now()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if reviewAttemptHook != nil {
			reviewAttemptHook(attempt)
		}
		if err := ctx.Err(); err != nil {
			return domain.ReviewFeedback{}, false, err
		}
		remaining := maxRetryElapsed - time.Since(started)
		if remaining <= 0 {
			return domain.ReviewFeedback{}, false, context.DeadlineExceeded
		}
		attemptCtx, cancel := context.WithTimeout(ctx, remaining)
		result, replay, err := s.submitReviewFeedbackOnce(attemptCtx, browserPrincipalID, portfolioID, reviewID, idempotencyKey, fingerprint, submission)
		attemptErr := attemptCtx.Err()
		cancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.ReviewFeedback{}, false, ctxErr
		}
		if attemptErr != nil {
			if err == nil {
				return domain.ReviewFeedback{}, false, attemptErr
			}
			return domain.ReviewFeedback{}, false, err
		}
		if err == nil || !isSQLiteBusy(err) || attempt == maxAttempts-1 {
			return result, replay, err
		}
		backoff := time.Duration(attempt+1) * 10 * time.Millisecond
		if time.Since(started)+backoff+100*time.Millisecond > maxRetryElapsed {
			return domain.ReviewFeedback{}, false, err
		}
		if reviewBackoffHook != nil {
			reviewBackoffHook(attempt)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return domain.ReviewFeedback{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.ReviewFeedback{}, false, ctx.Err()
}

func validIdempotencyKeyStore(value string) bool {
	return len(value) >= 16 && len(value) <= 128 && idempotencyKeyPatternStore(value)
}

func idempotencyKeyPatternStore(value string) bool {
	for _, char := range value {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("._~-", char)) {
			return false
		}
	}
	return true
}

func (s *Store) submitReviewFeedbackOnce(ctx context.Context, principalID, portfolioID, reviewID, key, fingerprint string, submission domain.FeedbackSubmission) (domain.ReviewFeedback, bool, error) {
	tx, release, err := s.beginReviewTx(ctx)
	if err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	defer release()
	defer tx.Rollback()

	var principalExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_principals WHERE id=?`, principalID).Scan(&principalExists); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if principalExists != 1 {
		return domain.ReviewFeedback{}, false, ErrFeedbackCorrupt
	}
	var reviewExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM advisory_reviews WHERE id=? AND portfolio_id=?`, reviewID, portfolioID).Scan(&reviewExists); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if reviewExists != 1 {
		return domain.ReviewFeedback{}, false, ErrFeedbackNotFound
	}

	var storedFingerprint, feedbackID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,feedback_id FROM feedback_idempotency WHERE browser_principal_id=? AND portfolio_id=? AND review_id=? AND idempotency_key=?`, principalID, portfolioID, reviewID, key).Scan(&storedFingerprint, &feedbackID)
	if err == nil {
		if !sameSecretText(storedFingerprint, fingerprint) {
			var storedDecision string
			var storedNote sql.NullString
			if fingerprintErr := tx.QueryRowContext(ctx, `SELECT decision,note FROM review_feedback WHERE id=?`, feedbackID).Scan(&storedDecision, &storedNote); fingerprintErr != nil {
				return domain.ReviewFeedback{}, false, ErrFeedbackCorrupt
			}
			storedSubmission := domain.FeedbackSubmission{Decision: storedDecision}
			if storedNote.Valid {
				storedSubmission.Note = &storedNote.String
			}
			canonicalStoredFingerprint, fingerprintErr := FeedbackFingerprint(storedSubmission)
			if fingerprintErr != nil || !sameSecretText(canonicalStoredFingerprint, storedFingerprint) {
				return domain.ReviewFeedback{}, false, ErrFeedbackCorrupt
			}
			return domain.ReviewFeedback{}, false, ErrFeedbackIdempotencyConflict
		}
		result, readErr := reconstructReviewFeedback(ctx, tx, principalID, portfolioID, reviewID, key, fingerprint)
		if readErr != nil {
			return domain.ReviewFeedback{}, false, readErr
		}
		if err := tx.Commit(); err != nil {
			return domain.ReviewFeedback{}, false, err
		}
		return result, true, nil
	}
	if err != sql.ErrNoRows {
		return domain.ReviewFeedback{}, false, err
	}
	var feedbackCount, reservationCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_feedback WHERE browser_principal_id=? AND portfolio_id=? AND review_id=?`, principalID, portfolioID, reviewID).Scan(&feedbackCount); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback_idempotency WHERE browser_principal_id=? AND portfolio_id=? AND review_id=?`, principalID, portfolioID, reviewID).Scan(&reservationCount); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if feedbackCount != reservationCount {
		return domain.ReviewFeedback{}, false, ErrFeedbackCorrupt
	}

	now := canonicalSnapshotTime(s.now())
	retainUntil := retentionFromCanonical(now)
	newFeedbackID, err := randomFeedbackID()
	if err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_feedback(id,browser_principal_id,portfolio_id,review_id,decision,note,occurred_at,retain_until,submitted_by_id,submitted_by_kind,submitted_by_primary_permission) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, newFeedbackID, principalID, portfolioID, reviewID, submission.Decision, nullableFeedbackNote(submission.Note), now, retainUntil, principalID, "browser_user", "portfolio:read"); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_permission_snapshots(feedback_id,browser_principal_id,portfolio_id,review_id,ordinal,permission) VALUES(?,?,?,?,?,?)`, newFeedbackID, principalID, portfolioID, reviewID, 1, "portfolio:read"); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO feedback_idempotency(browser_principal_id,portfolio_id,review_id,idempotency_key,request_fingerprint,feedback_id,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, principalID, portfolioID, reviewID, key, fingerprint, newFeedbackID, now, retainUntil); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	auditID, err := randomFeedbackID()
	if err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	auditPayload, _ := json.Marshal(map[string]string{
		"id": auditID, "action": "feedback_submitted", "principal_category": "browser",
		"principal_id": principalID, "portfolio_id": portfolioID, "review_id": reviewID,
		"occurred_at": now, "created_at": now,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, "browser", "feedback_submitted", "review_feedback", newFeedbackID, auditPayload, now, retainUntil); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, auditID, portfolioID, retainUntil); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if err := recordActivity(ctx, tx, portfolioID, "browser", "feedback_submitted", "feedback", "review_feedback", newFeedbackID, "Review feedback submitted", "Attributable human feedback was submitted.", now, auditID); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	result, err := reconstructReviewFeedback(ctx, tx, principalID, portfolioID, reviewID, key, fingerprint)
	if err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ReviewFeedback{}, false, err
	}
	return result, false, nil
}

func nullableFeedbackNote(note *string) any {
	if note == nil {
		return nil
	}
	return *note
}

func retentionFromCanonical(value string) string {
	parsed, err := time.Parse(feedbackTimestampFormat, value)
	if err != nil {
		return ""
	}
	return parsed.AddDate(7, 0, 0).Format(feedbackTimestampFormat)
}

func randomFeedbackID() (string, error) {
	id, err := randomReviewID()
	if err != nil {
		return "", err
	}
	return strings.Replace(id, "rev-", "feedback-", 1), nil
}

func reconstructReviewFeedback(ctx context.Context, q reviewQueryer, principalID, portfolioID, reviewID, key, expectedFingerprint string) (domain.ReviewFeedback, error) {
	var result domain.ReviewFeedback
	var feedbackID, storedRequestFingerprint, reservationCreatedAt, reservationRetainUntil string
	if err := q.QueryRowContext(ctx, `SELECT request_fingerprint,feedback_id,created_at,retain_until FROM feedback_idempotency WHERE browser_principal_id=? AND portfolio_id=? AND review_id=? AND idempotency_key=?`, principalID, portfolioID, reviewID, key).Scan(&storedRequestFingerprint, &feedbackID, &reservationCreatedAt, &reservationRetainUntil); err != nil || !sameSecretText(storedRequestFingerprint, expectedFingerprint) {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var principal, storedPortfolio, storedReview, decision, occurredAt, retainUntil, submittedByID, submittedByKind, primaryPermission string
	var note sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT id,browser_principal_id,portfolio_id,review_id,decision,note,occurred_at,retain_until,submitted_by_id,submitted_by_kind,submitted_by_primary_permission FROM review_feedback WHERE id=?`, feedbackID).Scan(&feedbackID, &principal, &storedPortfolio, &storedReview, &decision, &note, &occurredAt, &retainUntil, &submittedByID, &submittedByKind, &primaryPermission); err != nil {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	if feedbackID == "" || !validReviewID(feedbackID, 128) || principal != principalID || storedPortfolio != portfolioID || storedReview != reviewID || submittedByID != "local-owner" || submittedByKind != "browser_user" || primaryPermission != "portfolio:read" || !validFeedbackDecision(decision) || !validCanonicalReviewTime(occurredAt) || !validCanonicalReviewTime(retainUntil) {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	parsedOccurred, _ := time.Parse(feedbackTimestampFormat, occurredAt)
	parsedRetain, _ := time.Parse(feedbackTimestampFormat, retainUntil)
	if parsedRetain.Before(parsedOccurred.AddDate(7, 0, 0)) {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	if !validCanonicalReviewTime(reservationCreatedAt) || !validCanonicalReviewTime(reservationRetainUntil) || reservationCreatedAt != occurredAt || reservationRetainUntil != retainUntil {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	if note.Valid {
		value := note.String
		if !validReviewText(value, 2000) {
			return domain.ReviewFeedback{}, ErrFeedbackCorrupt
		}
		result.Note = &value
	}
	canonicalSubmission := domain.FeedbackSubmission{Decision: decision, Note: result.Note}
	canonicalFingerprint, err := FeedbackFingerprint(canonicalSubmission)
	if err != nil || !sameSecretText(canonicalFingerprint, storedRequestFingerprint) {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var reservationCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback_idempotency WHERE browser_principal_id=? AND portfolio_id=? AND review_id=? AND idempotency_key=? AND feedback_id=?`, principalID, portfolioID, reviewID, key, feedbackID).Scan(&reservationCount); err != nil || reservationCount != 1 {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var feedbackReservationCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback_idempotency WHERE feedback_id=?`, feedbackID).Scan(&feedbackReservationCount); err != nil || feedbackReservationCount != 1 {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal,browser_principal_id,portfolio_id,review_id,permission FROM feedback_permission_snapshots WHERE feedback_id=? ORDER BY ordinal`, feedbackID)
	if err != nil {
		return domain.ReviewFeedback{}, err
	}
	permissionCount := 0
	for rows.Next() {
		var ordinal int
		var rowPrincipal, rowPortfolio, rowReview, permission string
		if err := rows.Scan(&ordinal, &rowPrincipal, &rowPortfolio, &rowReview, &permission); err != nil {
			_ = rows.Close()
			return domain.ReviewFeedback{}, ErrFeedbackCorrupt
		}
		if ordinal != 1 || rowPrincipal != principalID || rowPortfolio != portfolioID || rowReview != reviewID || permission != "portfolio:read" {
			_ = rows.Close()
			return domain.ReviewFeedback{}, ErrFeedbackCorrupt
		}
		permissionCount++
	}
	if err := rows.Close(); err != nil || permissionCount != 1 {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var auditCount int
	var auditID, auditActor, auditAction, auditEntityType, auditEntityID, auditCreatedAt, auditRetain, payload string
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(id),''),COALESCE(MAX(actor),''),COALESCE(MAX(action),''),COALESCE(MAX(entity_type),''),COALESCE(MAX(entity_id),''),COALESCE(MAX(created_at),''),COALESCE(MAX(retain_until),''),COALESCE(MAX(payload),'') FROM audit_events WHERE entity_id=?`, feedbackID).Scan(&auditCount, &auditID, &auditActor, &auditAction, &auditEntityType, &auditEntityID, &auditCreatedAt, &auditRetain, &payload); err != nil || auditCount != 1 || auditActor != "browser" || auditAction != "feedback_submitted" || auditEntityType != "review_feedback" || auditEntityID != feedbackID || auditCreatedAt != occurredAt || auditRetain != retainUntil || !validFeedbackAuditPayload(payload, auditID, principalID, portfolioID, reviewID, occurredAt) {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var auditScopeCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_event_portfolios WHERE audit_event_id=?`, auditID).Scan(&auditScopeCount); err != nil || auditScopeCount != 1 {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_event_portfolios WHERE audit_event_id=? AND portfolio_id=? AND retain_until=?`, auditID, portfolioID, retainUntil).Scan(&auditScopeCount); err != nil || auditScopeCount != 1 {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	var activityCount int
	var activityID, activityActor, activityOccurred, activitySortTime, activityRetain, activitySource, activityCategory, activityAction, activityEntityType, activityEntityID, activitySummary, activityDetails string
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(id),''),COALESCE(MAX(actor_category),''),COALESCE(MAX(occurred_at),''),COALESCE(MAX(sort_time),''),COALESCE(MAX(retain_until),''),COALESCE(MAX(source_audit_event_id),''),COALESCE(MAX(category),''),COALESCE(MAX(action),''),COALESCE(MAX(entity_type),''),COALESCE(MAX(entity_id),''),COALESCE(MAX(summary),''),COALESCE(MAX(details),'') FROM activity_events WHERE portfolio_id=? AND entity_id=?`, portfolioID, feedbackID).Scan(&activityCount, &activityID, &activityActor, &activityOccurred, &activitySortTime, &activityRetain, &activitySource, &activityCategory, &activityAction, &activityEntityType, &activityEntityID, &activitySummary, &activityDetails); err != nil || activityCount != 1 || activityID != deterministicID("activity", auditID, portfolioID) || activityActor != "browser" || activityOccurred != occurredAt || activitySortTime != occurredAt || activityRetain != retainUntil || activitySource != auditID || activityCategory != "feedback" || activityAction != "feedback_submitted" || activityEntityType != "review_feedback" || activityEntityID != feedbackID || activitySummary != "Review feedback submitted" || activityDetails != "Attributable human feedback was submitted." {
		return domain.ReviewFeedback{}, ErrFeedbackCorrupt
	}
	result.FeedbackID, result.PortfolioID, result.ReviewID, result.Decision, result.OccurredAt = feedbackID, portfolioID, reviewID, decision, occurredAt
	result.SubmittedBy = domain.PrincipalAttribution{PrincipalID: "local-owner", PrincipalType: "browser_user", Permissions: []string{"portfolio:read"}}
	return result, nil
}

func validFeedbackDecision(value string) bool {
	return value == "agree" || value == "disagree" || value == "defer" || value == "request_another_review"
}

func validFeedbackAuditPayload(raw, auditID, principalID, portfolioID, reviewID, occurredAt string) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &payload) != nil || len(payload) != 8 {
		return false
	}
	values := map[string]string{}
	for _, key := range []string{"id", "action", "principal_category", "principal_id", "portfolio_id", "review_id", "occurred_at", "created_at"} {
		var value string
		if rawValue, ok := payload[key]; !ok || json.Unmarshal(rawValue, &value) != nil {
			return false
		}
		values[key] = value
	}
	return values["id"] == auditID && values["action"] == "feedback_submitted" && values["principal_category"] == "browser" && values["principal_id"] == principalID && values["portfolio_id"] == portfolioID && values["review_id"] == reviewID && values["occurred_at"] == occurredAt && values["created_at"] == occurredAt
}

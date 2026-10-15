package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrReviewInvalid             = errors.New("invalid review submission")
	ErrReviewIncomplete          = errors.New("incomplete review")
	ErrReviewIdempotencyConflict = errors.New("review idempotency conflict")
	ErrReviewCorrupt             = errors.New("review is corrupt")
	ErrReviewTargetNotFound      = errors.New("review target not found")
)

var reviewIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)
var reviewDecimalPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
var evidenceSuffixPattern = regexp.MustCompile(`^[A-Za-z0-9._~:/?$&'()*+,;=%!-]*$`)

// These hooks are intentionally unexported test seams for deterministic
// cancellation/contention tests. Production callers cannot alter retry state.
var reviewAttemptHook func(int)
var reviewBackoffHook func(int)

const reviewBodyLimit = 256 << 10

// ReviewBodyLimit is the maximum encoded JSON request accepted by the HTTP
// boundary. It is exported for production handlers and focused tests.
const ReviewBodyLimit = reviewBodyLimit

type reviewQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func ReviewFingerprint(submission domain.ReviewSubmission) (string, error) {
	submission = canonicalReviewSubmission(submission)
	encoded, err := json.Marshal(submission)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalReviewSubmission(submission domain.ReviewSubmission) domain.ReviewSubmission {
	if submission.Scope.HoldingIDs == nil { submission.Scope.HoldingIDs = []string{} }
	if submission.Scope.InstrumentIDs == nil { submission.Scope.InstrumentIDs = []string{} }
	if submission.Risks == nil { submission.Risks = []string{} }
	if submission.Counterarguments == nil { submission.Counterarguments = []string{} }
	if submission.Conditions == nil { submission.Conditions = []string{} }
	if submission.Metrics == nil { submission.Metrics = []domain.ReviewMetric{} }
	if submission.SourcedFacts == nil { submission.SourcedFacts = []domain.SourcedFact{} }
	if submission.Evidence == nil { submission.Evidence = []domain.ReviewEvidence{} }
	for index := range submission.SourcedFacts {
		if submission.SourcedFacts[index].EvidenceIDs == nil { submission.SourcedFacts[index].EvidenceIDs = []string{} }
	}
	return submission
}

func ValidateReviewSubmission(submission domain.ReviewSubmission, snapshot domain.ReviewSnapshot) error {
	if !validReviewID(submission.SnapshotID, 128) || !validReviewID(submission.CalculationVersion, 128) || submission.SnapshotID != snapshot.SnapshotID || submission.CalculationVersion != snapshot.CalculationVersion {
		return ErrReviewInvalid
	}
	if submission.Status != "complete" && submission.Status != "incomplete" {
		return ErrReviewInvalid
	}
	if !validReviewText(submission.Rationale, 8000) || !validReviewText(submission.ModelOpinion, 8000) || !validCanonicalReviewTime(submission.GeneratedAt) {
		return ErrReviewInvalid
	}
	if !validReviewStringArray(submission.Risks, 32, 2000) || !validReviewStringArray(submission.Counterarguments, 32, 2000) || !validReviewStringArray(submission.Conditions, 32, 2000) {
		return ErrReviewInvalid
	}
	if !validateReviewScope(submission.Scope, snapshot) {
		if submission.Scope.Type == "holding" && len(submission.Scope.HoldingIDs) > 0 && len(submission.Scope.HoldingIDs) == len(submission.Scope.InstrumentIDs) && len(submission.Scope.HoldingIDs) <= 100 && reviewScopeIDsWellFormed(submission.Scope) {
			return ErrReviewTargetNotFound
		}
		return ErrReviewInvalid
	}
	if !validateReviewModel(submission.Model) || !validateReviewMetrics(submission.Metrics) {
		return ErrReviewInvalid
	}
	if !validateReviewEvidence(submission.Evidence) || !validateSourcedFacts(submission.SourcedFacts, submission.Evidence) {
		return ErrReviewInvalid
	}
	if submission.Status == "complete" {
		if snapshot.State != "complete" || submission.Recommendation == "no_recommendation" || !validCompleteEvidence(submission.Evidence) || len(submission.SourcedFacts) == 0 {
			return ErrReviewIncomplete
		}
		if submission.Recommendation != "hold" && submission.Recommendation != "review" && submission.Recommendation != "trim_candidate" && submission.Recommendation != "exit_candidate" {
			return ErrReviewInvalid
		}
	} else {
		if submission.Recommendation != "no_recommendation" {
			return ErrReviewIncomplete
		}
		if !reviewHasActualIncompleteSupport(snapshot, submission) {
			return ErrReviewIncomplete
		}
	}
	return nil
}

func reviewHasActualIncompleteSupport(snapshot domain.ReviewSnapshot, submission domain.ReviewSubmission) bool {
	if snapshot.State != "complete" || len(snapshot.MissingDependencies) != 0 || len(snapshot.StaleDependencies) != 0 || len(snapshot.InvalidDependencies) != 0 {
		return true
	}
	if len(submission.Evidence) == 0 || len(submission.SourcedFacts) == 0 {
		return true
	}
	for _, evidence := range submission.Evidence {
		if evidence.Availability != "available" || evidence.Freshness != "current" {
			return true
		}
	}
	return false
}

func validateReviewScope(scope domain.ReviewScope, snapshot domain.ReviewSnapshot) bool {
	if scope.Type == "portfolio" {
		return scope.HoldingIDs != nil && scope.InstrumentIDs != nil && len(scope.HoldingIDs) == 0 && len(scope.InstrumentIDs) == 0
	}
	if scope.Type != "holding" || len(scope.HoldingIDs) == 0 || len(scope.HoldingIDs) != len(scope.InstrumentIDs) || len(scope.HoldingIDs) > 100 {
		return false
	}
	seenH, seenI := map[string]bool{}, map[string]bool{}
	for index, holdingID := range scope.HoldingIDs {
		instrumentID := scope.InstrumentIDs[index]
		if !validReviewID(holdingID, 128) || !validReviewID(instrumentID, 128) || seenH[holdingID] || seenI[instrumentID] {
			return false
		}
		seenH[holdingID], seenI[instrumentID] = true, true
		found := false
		for _, holding := range snapshot.Holdings {
			if holding.HoldingID == holdingID && holding.InstrumentID == instrumentID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func reviewScopeIDsWellFormed(scope domain.ReviewScope) bool {
	seenH, seenI := map[string]bool{}, map[string]bool{}
	for index, holdingID := range scope.HoldingIDs {
		instrumentID := scope.InstrumentIDs[index]
		if !validReviewID(holdingID, 128) || !validReviewID(instrumentID, 128) || seenH[holdingID] || seenI[instrumentID] {
			return false
		}
		seenH[holdingID], seenI[instrumentID] = true, true
	}
	return true
}

func validateReviewModel(model domain.ModelProvenance) bool {
	return validPatternText(model.Provider, 128, regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)) &&
		validPatternText(model.Model, 256, regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~:/-]*$`)) &&
		validReviewText(model.Version, 128) && model.Runtime == "hermes-environment"
}

func validateReviewMetrics(metrics []domain.ReviewMetric) bool {
	if len(metrics) > 100 {
		return false
	}
	for _, metric := range metrics {
		if !validReviewText(metric.Name, 128) || !validReviewDecimal(metric.Value) || !validReviewText(metric.Unit, 64) || !validCanonicalReviewTime(metric.AsOf) || (metric.Source != "snapshot" && metric.Source != "calculation" && metric.Source != "submitted") || !metric.Deterministic {
			return false
		}
	}
	return true
}

func validateReviewEvidence(evidence []domain.ReviewEvidence) bool {
	if len(evidence) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range evidence {
		if !validReviewID(item.EvidenceID, 128) || seen[item.EvidenceID] || !validReviewText(item.Title, 512) || !validCanonicalReviewTime(item.AccessedAt) || !validReviewText(item.Excerpt, 4000) || !validEvidenceAvailability(item.Availability) || !validEvidenceFreshness(item.Freshness) {
			return false
		}
		if item.PublishedAt != nil && !validCanonicalReviewTime(*item.PublishedAt) {
			return false
		}
		if _, _, err := canonicalEvidenceURL(item.URL); err != nil {
			return false
		}
		seen[item.EvidenceID] = true
	}
	return true
}

func validateSourcedFacts(facts []domain.SourcedFact, evidence []domain.ReviewEvidence) bool {
	if len(facts) > 32 {
		return false
	}
	known := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		known[item.EvidenceID] = true
	}
	for _, fact := range facts {
		if !validReviewText(fact.Statement, 2000) || len(fact.EvidenceIDs) == 0 || len(fact.EvidenceIDs) > 16 {
			return false
		}
		seen := map[string]bool{}
		for _, evidenceID := range fact.EvidenceIDs {
			if !validReviewID(evidenceID, 128) || seen[evidenceID] || !known[evidenceID] {
				return false
			}
			seen[evidenceID] = true
		}
	}
	return true
}

func validCompleteEvidence(evidence []domain.ReviewEvidence) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, item := range evidence {
		if item.Availability != "available" || item.Freshness != "current" {
			return false
		}
	}
	return true
}

func validEvidenceAvailability(value string) bool {
	return value == "available" || value == "unavailable" || value == "blocked" || value == "invalid"
}
func validEvidenceFreshness(value string) bool {
	return value == "current" || value == "stale" || value == "unknown"
}
func validRecommendation(value string) bool {
	return value == "hold" || value == "review" || value == "trim_candidate" || value == "exit_candidate" || value == "no_recommendation"
}
func validReviewStringArray(values []string, max, textMax int) bool {
	if len(values) > max {
		return false
	}
	for _, value := range values {
		if !validReviewText(value, textMax) {
			return false
		}
	}
	return true
}
func validPatternText(value string, max int, pattern *regexp.Regexp) bool {
	return utf8.ValidString(value) && len([]rune(value)) >= 1 && len([]rune(value)) <= max && pattern.MatchString(value)
}
func validReviewText(value string, max int) bool {
	if !utf8.ValidString(value) || len([]rune(value)) < 1 || len([]rune(value)) > max || strings.ContainsAny(value, "<>") {
		return false
	}
	for _, char := range value {
		if (char < 0x20 && char != '\n' && char != '	') || (char >= 0x7f && char <= 0x9f) {
			return false
		}
	}
	return true
}
func validReviewID(value string, max int) bool {
	return len(value) >= 1 && len(value) <= max && reviewIDPattern.MatchString(value)
}
func validReviewDecimal(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && reviewDecimalPattern.MatchString(value)
}
func validCanonicalReviewTime(value string) bool {
	if len(value) != 30 {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000000000Z", value)
	return err == nil && parsed.UTC().Format("2006-01-02T15:04:05.000000000Z") == value
}

func canonicalEvidenceURL(raw string) (string, string, error) {
	if len(raw) < 1 || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", "", ErrReviewInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Host == "" || parsed.Port() != "" {
		return "", "", ErrReviewInvalid
	}
	host := parsed.Hostname()
	if host == "" || host != strings.ToLower(host) || !isASCII(host) || strings.HasSuffix(host, ".") || strings.HasPrefix(host, ".") || strings.Contains(host, "..") || len(host) > 253 {
		return "", "", ErrReviewInvalid
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", "", ErrReviewInvalid
		}
		for _, char := range label {
			if !(char == '-' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
				return "", "", ErrReviewInvalid
			}
		}
	}
	if parsed.Host != host {
		return "", "", ErrReviewInvalid
	}
	suffix := parsed.EscapedPath()
	if suffix == "/" && parsed.RawQuery == "" {
		// The contract permits slash suffixes; retain it exactly.
	}
	if parsed.RawQuery != "" {
		suffix += "?" + parsed.RawQuery
	} else if parsed.ForceQuery {
		suffix += "?"
	}
	if suffix != "" && suffix[0] != '/' && suffix[0] != '?' {
		return "", "", ErrReviewInvalid
	}
	if len(suffix) > 2047 || !evidenceSuffixPattern.MatchString(suffix) || len("https://"+host+suffix) > 2048 {
		return "", "", ErrReviewInvalid
	}
	return host, suffix, nil
}
func isASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] > 0x7f {
			return false
		}
	}
	return true
}

// SubmitAdvisoryReview appends a complete normalized graph. The transaction
// also owns the idempotency reservation, audit association and Activity row.
// SQLite can reject a deferred read-to-write transaction upgrade when two
// independent connections submit the same key at once. A bounded retry keeps
// that expected contention on the idempotency path instead of exposing a
// spurious SQLITE_BUSY error to callers.
func (s *Store) SubmitAdvisoryReview(ctx context.Context, principal auth.MachinePrincipal, portfolioID, idempotencyKey, fingerprint string, submission domain.ReviewSubmission) (domain.AdvisoryReview, bool, error) {
	const maxAttempts = 4
	const maxRetryElapsed = 250 * time.Millisecond
	started := time.Now()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if reviewAttemptHook != nil {
			reviewAttemptHook(attempt)
		}
		if err := ctx.Err(); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
		result, replay, err := s.submitAdvisoryReviewOnce(ctx, principal, portfolioID, idempotencyKey, fingerprint, submission)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.AdvisoryReview{}, false, ctxErr
		}
		if err == nil || !isSQLiteBusy(err) || attempt == maxAttempts-1 {
			return result, replay, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.AdvisoryReview{}, false, ctxErr
		}
		backoff := time.Duration(attempt+1) * 10 * time.Millisecond
		if elapsed := time.Since(started); elapsed+backoff > maxRetryElapsed {
			return domain.AdvisoryReview{}, false, err
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
			return domain.AdvisoryReview{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return domain.AdvisoryReview{}, false, ctx.Err()
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_BUSY_RECOVERY, sqlite3.SQLITE_BUSY_SNAPSHOT, sqlite3.SQLITE_BUSY_TIMEOUT,
		sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_LOCKED_SHAREDCACHE:
		return true
	default:
		return false
	}
}

func (s *Store) submitAdvisoryReviewOnce(ctx context.Context, principal auth.MachinePrincipal, portfolioID, idempotencyKey, fingerprint string, submission domain.ReviewSubmission) (domain.AdvisoryReview, bool, error) {
	if principal.PrincipalID == "" || portfolioID != principal.PortfolioID || !principal.PortfolioRead || !principal.ReviewsWrite {
		return domain.AdvisoryReview{}, false, auth.ErrForbidden
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	defer tx.Rollback()
	// Recheck the persisted identity and grants before looking at either the
	// idempotency reservation or the historical review graph. This is the
	// authorization linearization point for both new submissions and replays.
	var active, readGrant, writeGrant int
	if err := tx.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_reviews_write FROM machine_principals WHERE id=? AND portfolio_id=?`, principal.PrincipalID, portfolioID).Scan(&active, &readGrant, &writeGrant); err != nil {
		if err == sql.ErrNoRows {
			return domain.AdvisoryReview{}, false, auth.ErrForbidden
		}
		return domain.AdvisoryReview{}, false, err
	}
	if active != 1 || readGrant != 1 || writeGrant != 1 {
		return domain.AdvisoryReview{}, false, auth.ErrForbidden
	}
	var existingFingerprint, existingReviewID string
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,review_id FROM review_idempotency WHERE principal_id=? AND portfolio_id=? AND idempotency_key=?`, principal.PrincipalID, portfolioID, idempotencyKey).Scan(&existingFingerprint, &existingReviewID)
	if err == nil {
		if !sameSecretText(existingFingerprint, fingerprint) {
			return domain.AdvisoryReview{}, false, ErrReviewIdempotencyConflict
		}
		result, readErr := reconstructAdvisoryReview(ctx, tx, portfolioID, existingReviewID, principal.PrincipalID, fingerprint)
		if readErr != nil {
			return domain.AdvisoryReview{}, false, readErr
		}
		if err := tx.Commit(); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
		return result, true, nil
	}
	if err != sql.ErrNoRows {
		return domain.AdvisoryReview{}, false, err
	}
	// A reservation whose key was rebound to another owner is corruption, not
	// a new submission. The join is deliberately scoped to this review's
	// immutable attribution so an unrelated principal may legitimately reuse
	// the same textual key in its own namespace.
	var reboundReviewID string
	err = tx.QueryRowContext(ctx, `SELECT i.review_id FROM review_idempotency i JOIN advisory_reviews r ON r.id=i.review_id WHERE i.idempotency_key=? AND r.portfolio_id=? AND r.submitted_by_id=? LIMIT 1`, idempotencyKey, portfolioID, principal.PrincipalID).Scan(&reboundReviewID)
	if err == nil {
		return domain.AdvisoryReview{}, false, ErrReviewCorrupt
	}
	if err != sql.ErrNoRows {
		return domain.AdvisoryReview{}, false, err
	}
	// Snapshot validation is intentionally outside the authorization transaction:
	// ReviewSnapshot may use another pooled connection. Reopen and recheck the
	// principal before any write, and re-read idempotency to close this race.
	_ = tx.Rollback()
	snapshot, err := s.ReviewSnapshot(ctx, portfolioID, submission.SnapshotID, false)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if err := ValidateReviewSubmission(submission, snapshot); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	tx, err = s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_reviews_write FROM machine_principals WHERE id=? AND portfolio_id=?`, principal.PrincipalID, portfolioID).Scan(&active, &readGrant, &writeGrant); err != nil {
		if err == sql.ErrNoRows {
			return domain.AdvisoryReview{}, false, auth.ErrForbidden
		}
		return domain.AdvisoryReview{}, false, err
	}
	if active != 1 || readGrant != 1 || writeGrant != 1 {
		return domain.AdvisoryReview{}, false, auth.ErrForbidden
	}
	existingFingerprint, existingReviewID = "", ""
	err = tx.QueryRowContext(ctx, `SELECT request_fingerprint,review_id FROM review_idempotency WHERE principal_id=? AND portfolio_id=? AND idempotency_key=?`, principal.PrincipalID, portfolioID, idempotencyKey).Scan(&existingFingerprint, &existingReviewID)
	if err == nil {
		if !sameSecretText(existingFingerprint, fingerprint) {
			return domain.AdvisoryReview{}, false, ErrReviewIdempotencyConflict
		}
		result, readErr := reconstructAdvisoryReview(ctx, tx, portfolioID, existingReviewID, principal.PrincipalID, fingerprint)
		if readErr != nil {
			return domain.AdvisoryReview{}, false, readErr
		}
		if err := tx.Commit(); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
		return result, true, nil
	}
	if err != sql.ErrNoRows {
		return domain.AdvisoryReview{}, false, err
	}
	reviewID, err := randomReviewID()
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	now := s.now()
	submittedAt := canonicalSnapshotTime(now)
	retainUntil := now.AddDate(7, 0, 0).Format("2006-01-02T15:04:05.000000000Z")
	primaryEvidenceID, primaryEvidenceAvailability, primaryEvidenceFreshness := "", "", ""
	primaryFactOrdinal, primaryTargetHoldingID := any(nil), any(nil)
	if submission.Status == "complete" {
		evidence := make(map[string]domain.ReviewEvidence, len(submission.Evidence))
		for _, item := range submission.Evidence {
			evidence[item.EvidenceID] = item
		}
		for factOrdinal, fact := range submission.SourcedFacts {
			for _, evidenceID := range fact.EvidenceIDs {
				item := evidence[evidenceID]
				if item.Availability == "available" && item.Freshness == "current" {
					primaryEvidenceID, primaryEvidenceAvailability, primaryEvidenceFreshness = item.EvidenceID, item.Availability, item.Freshness
					primaryFactOrdinal = factOrdinal
					break
				}
			}
			if primaryEvidenceID != "" {
				break
			}
		}
		if primaryEvidenceID == "" || primaryFactOrdinal == nil {
			return domain.AdvisoryReview{}, false, ErrReviewIncomplete
		}
	}
	if submission.Scope.Type == "holding" {
		primaryTargetHoldingID = submission.Scope.HoldingIDs[0]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO advisory_reviews(id,portfolio_id,snapshot_id,calculation_version,scope_type,historical_status,recommendation,rationale,model_opinion,model_provider,model_name,model_version,model_runtime,generated_at,submitted_at,submitted_by_id,submitted_by_kind,submitted_by_primary_permission,primary_evidence_id,primary_evidence_availability,primary_evidence_freshness,primary_fact_ordinal,primary_target_holding_id,retain_until) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, reviewID, portfolioID, submission.SnapshotID, submission.CalculationVersion, submission.Scope.Type, submission.Status, submission.Recommendation, submission.Rationale, submission.ModelOpinion, submission.Model.Provider, submission.Model.Model, submission.Model.Version, submission.Model.Runtime, submission.GeneratedAt, submittedAt, principal.PrincipalID, "machine", "reviews:write", nullableText(primaryEvidenceID), nullableText(primaryEvidenceAvailability), nullableText(primaryEvidenceFreshness), primaryFactOrdinal, primaryTargetHoldingID, retainUntil)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	for ordinal := range submission.Scope.HoldingIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(review_id,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id,ordinal) VALUES(?,?,?,?,?,?,?)`, reviewID, portfolioID, submission.SnapshotID, submission.CalculationVersion, submission.Scope.HoldingIDs[ordinal], submission.Scope.InstrumentIDs[ordinal], ordinal); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for ordinal, text := range submission.Risks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_risks(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, reviewID, portfolioID, ordinal, text); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for ordinal, text := range submission.Counterarguments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_counterarguments(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, reviewID, portfolioID, ordinal, text); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for ordinal, text := range submission.Conditions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_conditions(review_id,portfolio_id,ordinal,text) VALUES(?,?,?,?)`, reviewID, portfolioID, ordinal, text); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for ordinal, metric := range submission.Metrics {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_metrics(review_id,portfolio_id,ordinal,name,value,unit,as_of,source,deterministic) VALUES(?,?,?,?,?,?,?,?,?)`, reviewID, portfolioID, ordinal, metric.Name, metric.Value, metric.Unit, metric.AsOf, metric.Source, 1); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for ordinal, item := range submission.Evidence {
		host, suffix, urlErr := canonicalEvidenceURL(item.URL)
		if urlErr != nil {
			return domain.AdvisoryReview{}, false, ErrReviewInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_evidence(review_id,portfolio_id,evidence_id,ordinal,url_host,url_suffix,title,published_at,accessed_at,availability,freshness,excerpt) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, reviewID, portfolioID, item.EvidenceID, ordinal, host, suffix, item.Title, nullablePublishedAt(item.PublishedAt), item.AccessedAt, item.Availability, item.Freshness, item.Excerpt); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	for factOrdinal, fact := range submission.SourcedFacts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_sourced_facts(review_id,portfolio_id,fact_ordinal,statement) VALUES(?,?,?,?)`, reviewID, portfolioID, factOrdinal, fact.Statement); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
		for evidenceOrdinal, evidenceID := range fact.EvidenceIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_fact_evidence(review_id,portfolio_id,fact_ordinal,evidence_id,evidence_ordinal) VALUES(?,?,?,?,?)`, reviewID, portfolioID, factOrdinal, evidenceID, evidenceOrdinal); err != nil {
				return domain.AdvisoryReview{}, false, err
			}
		}
	}
	if principal.PortfolioRead {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_submission_permission_snapshots(review_id,portfolio_id,ordinal,permission) VALUES(?,?,?,?)`, reviewID, portfolioID, 1, "portfolio:read"); err != nil {
			return domain.AdvisoryReview{}, false, err
		}
	}
	insertResult, err := tx.ExecContext(ctx, `INSERT INTO review_idempotency(principal_id,portfolio_id,idempotency_key,request_fingerprint,review_id,created_at,retain_until) VALUES(?,?,?,?,?,?,?) ON CONFLICT(principal_id,portfolio_id,idempotency_key) DO NOTHING`, principal.PrincipalID, portfolioID, idempotencyKey, fingerprint, reviewID, submittedAt, retainUntil)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if affected, affectedErr := insertResult.RowsAffected(); affectedErr == nil && affected == 0 {
		_ = tx.Rollback()
		return s.replayReviewIdempotency(ctx, principal.PrincipalID, portfolioID, idempotencyKey, fingerprint)
	}
	auditID, err := randomReviewID()
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	auditPayload, _ := json.Marshal(map[string]string{"id": auditID, "action": "review_submitted", "principal_category": "machine", "principal_id": principal.PrincipalID, "portfolio_id": portfolioID, "review_id": reviewID, "snapshot_id": submission.SnapshotID, "status": submission.Status, "occurred_at": submittedAt, "submitted_at": submittedAt, "created_at": submittedAt})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, auditID, "machine", "review_submitted", "advisory_review", reviewID, auditPayload, submittedAt, retainUntil); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, auditID, portfolioID, retainUntil); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if err := recordActivity(ctx, tx, portfolioID, "hermes", "review_submitted", "review", "advisory_review", reviewID, "Advisory review submitted", "An immutable advisory review was submitted.", submittedAt, auditID); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	result, err := reconstructAdvisoryReview(ctx, tx, portfolioID, reviewID, principal.PrincipalID, fingerprint)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	return result, false, nil
}

func (s *Store) replayReviewIdempotency(ctx context.Context, principalID, portfolioID, key, fingerprint string) (domain.AdvisoryReview, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	defer tx.Rollback()
	var active, readGrant, writeGrant int
	if err := tx.QueryRowContext(ctx, `SELECT active,permission_portfolio_read,permission_reviews_write FROM machine_principals WHERE id=? AND portfolio_id=?`, principalID, portfolioID).Scan(&active, &readGrant, &writeGrant); err != nil {
		if err == sql.ErrNoRows {
			return domain.AdvisoryReview{}, false, auth.ErrForbidden
		}
		return domain.AdvisoryReview{}, false, err
	}
	if active != 1 || readGrant != 1 || writeGrant != 1 {
		return domain.AdvisoryReview{}, false, auth.ErrForbidden
	}
	var storedFingerprint, reviewID string
	if err := tx.QueryRowContext(ctx, `SELECT request_fingerprint,review_id FROM review_idempotency WHERE principal_id=? AND portfolio_id=? AND idempotency_key=?`, principalID, portfolioID, key).Scan(&storedFingerprint, &reviewID); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if !sameSecretText(storedFingerprint, fingerprint) {
		return domain.AdvisoryReview{}, false, ErrReviewIdempotencyConflict
	}
	result, err := reconstructAdvisoryReview(ctx, tx, portfolioID, reviewID, principalID, fingerprint)
	if err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AdvisoryReview{}, false, err
	}
	return result, true, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullablePublishedAt(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func sameSecretText(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
func randomReviewID() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "rev-" + base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func reconstructAdvisoryReview(ctx context.Context, q reviewQueryer, portfolioID, reviewID, expectedPrincipalID, expectedFingerprint string) (domain.AdvisoryReview, error) {
	var result domain.AdvisoryReview
	var scopeType, status, generatedAt, submittedAt, submittedByID, submittedByKind, primaryPermission string
	var primaryEvidenceID, primaryEvidenceAvailability, primaryEvidenceFreshness, primaryFactOrdinal, primaryTargetHoldingID sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT id,portfolio_id,snapshot_id,calculation_version,scope_type,historical_status,recommendation,rationale,model_opinion,model_provider,model_name,model_version,model_runtime,generated_at,submitted_at,submitted_by_id,submitted_by_kind,submitted_by_primary_permission,primary_evidence_id,primary_evidence_availability,primary_evidence_freshness,primary_fact_ordinal,primary_target_holding_id FROM advisory_reviews WHERE id=? AND portfolio_id=?`, reviewID, portfolioID).Scan(&result.ReviewID, &result.PortfolioID, &result.SnapshotID, &result.CalculationVersion, &scopeType, &status, &result.Recommendation, &result.Rationale, &result.ModelOpinion, &result.Model.Provider, &result.Model.Model, &result.Model.Version, &result.Model.Runtime, &generatedAt, &submittedAt, &submittedByID, &submittedByKind, &primaryPermission, &primaryEvidenceID, &primaryEvidenceAvailability, &primaryEvidenceFreshness, &primaryFactOrdinal, &primaryTargetHoldingID); err != nil {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	var reservationCount int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_idempotency WHERE review_id=? AND portfolio_id=? AND principal_id=?`, reviewID, portfolioID, expectedPrincipalID).Scan(&reservationCount); err != nil || reservationCount != 1 {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	advisoryCalculationVersion := result.CalculationVersion
	var linkedCalculationVersion string
	if err := q.QueryRowContext(ctx, `SELECT calculation_version FROM review_snapshots WHERE portfolio_id=? AND snapshot_id=?`, portfolioID, result.SnapshotID).Scan(&linkedCalculationVersion); err != nil || linkedCalculationVersion != advisoryCalculationVersion {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	result.CalculationVersion = advisoryCalculationVersion
	result.GeneratedAt, result.SubmittedAt = generatedAt, submittedAt
	result.Status = status
	result.Scope = domain.ReviewScope{Type: scopeType, HoldingIDs: []string{}, InstrumentIDs: []string{}}
	result.Risks, result.Counterarguments, result.Conditions, result.Metrics, result.Evidence, result.SourcedFacts = []string{}, []string{}, []string{}, []domain.ReviewMetric{}, []domain.ReviewEvidence{}, []domain.SourcedFact{}
	if !validReviewID(result.ReviewID, 128) || result.PortfolioID != portfolioID || !validReviewID(result.SnapshotID, 128) || !validReviewID(result.CalculationVersion, 128) || (scopeType != "portfolio" && scopeType != "holding") || (status != "complete" && status != "incomplete") || !validRecommendation(result.Recommendation) || !validReviewText(result.Rationale, 8000) || !validReviewText(result.ModelOpinion, 8000) || !validCanonicalReviewTime(generatedAt) || !validCanonicalReviewTime(submittedAt) || !validReviewID(submittedByID, 128) || submittedByKind != "machine" || primaryPermission != "reviews:write" || !validateReviewModel(result.Model) || (expectedPrincipalID != "" && submittedByID != expectedPrincipalID) {
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
		seen := make(map[string]bool, len(fact.EvidenceIDs))
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
		foundTarget := false
		for _, holdingID := range result.Scope.HoldingIDs {
			if holdingID == primaryTargetHoldingID.String {
				foundTarget = true
				break
			}
		}
		if !foundTarget {
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
		if err := rows.Scan(&ordinal, &rowPortfolioID, &permission); err != nil {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
		if rowPortfolioID != portfolioID || ordinal != 1 || permission != "portfolio:read" {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Err(); err != nil {
		return domain.AdvisoryReview{}, err
	}
	if len(permissions) != 2 {
		return domain.AdvisoryReview{}, ErrReviewCorrupt
	}
	result.SubmittedBy = domain.PrincipalAttribution{PrincipalID: submittedByID, PrincipalType: "machine", Permissions: permissions}
	// Lifecycle reconstruction is deliberately normalized-only. The submitted
	// snapshot linkage is the immutable historical/current reference; consulting
	// mutable snapshot payloads here would make idempotent replay non-deterministic.
	current := result.SnapshotID
	result.Lifecycle = domain.ReviewLifecycle{HistoricalStatus: status, CurrentSnapshotID: &current, Outdated: false}
	if expectedFingerprint != "" {
		reconstructedFingerprint, err := ReviewFingerprint(domain.ReviewSubmission{
			SnapshotID: result.SnapshotID, CalculationVersion: result.CalculationVersion, Scope: result.Scope,
			Status: result.Status, Recommendation: result.Recommendation, Rationale: result.Rationale,
			Risks: result.Risks, Counterarguments: result.Counterarguments, Conditions: result.Conditions,
			Metrics: result.Metrics, SourcedFacts: result.SourcedFacts, ModelOpinion: result.ModelOpinion,
			Evidence: result.Evidence, Model: result.Model, GeneratedAt: result.GeneratedAt,
		})
		if err != nil || !sameSecretText(reconstructedFingerprint, expectedFingerprint) {
			return domain.AdvisoryReview{}, ErrReviewCorrupt
		}
	}
	return result, nil
}

func loadReviewTargets(ctx context.Context, q reviewQueryer, portfolioID, reviewID, snapshotID, calculationVersion string, scope *domain.ReviewScope) error {
	rows, err := q.QueryContext(ctx, `SELECT ordinal,portfolio_id,snapshot_id,calculation_version,holding_id,instrument_id FROM review_targets WHERE review_id=? ORDER BY ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 0
	seenHoldings, seenInstruments := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var ordinal int
		var rowPortfolioID, rowSnapshotID, rowCalculationVersion, holdingID, instrumentID string
		if err := rows.Scan(&ordinal, &rowPortfolioID, &rowSnapshotID, &rowCalculationVersion, &holdingID, &instrumentID); err != nil {
			return ErrReviewCorrupt
		}
		if rowPortfolioID != portfolioID || rowSnapshotID != snapshotID || rowCalculationVersion != calculationVersion || ordinal != want || !validReviewID(holdingID, 128) || !validReviewID(instrumentID, 128) || seenHoldings[holdingID] || seenInstruments[instrumentID] || want >= 100 {
			return ErrReviewCorrupt
		}
		var members int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM snapshot_holdings WHERE portfolio_id=? AND snapshot_id=? AND holding_id=? AND instrument_id=?`, portfolioID, snapshotID, holdingID, instrumentID).Scan(&members); err != nil {
			return err
		}
		if members != 1 {
			return ErrReviewCorrupt
		}
		seenHoldings[holdingID], seenInstruments[instrumentID] = true, true
		scope.HoldingIDs = append(scope.HoldingIDs, holdingID)
		scope.InstrumentIDs = append(scope.InstrumentIDs, instrumentID)
		want++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if scope.Type == "portfolio" && want != 0 {
		return ErrReviewCorrupt
	}
	if scope.Type == "holding" && want == 0 {
		return ErrReviewCorrupt
	}
	return nil
}

func loadReviewTextChildren(ctx context.Context, q reviewQueryer, table, column, portfolioID, reviewID string, target *[]string) error {
	rows, err := q.QueryContext(ctx, `SELECT ordinal,portfolio_id,`+column+` FROM `+table+` WHERE review_id=? ORDER BY ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 0
	for rows.Next() {
		var ordinal int
		var rowPortfolioID, value string
		if err := rows.Scan(&ordinal, &rowPortfolioID, &value); err != nil {
			return ErrReviewCorrupt
		}
		if rowPortfolioID != portfolioID || ordinal != want || !validReviewText(value, 2000) {
			return ErrReviewCorrupt
		}
		*target = append(*target, value)
		want++
	}
	return rows.Err()
}

func loadReviewMetrics(ctx context.Context, q reviewQueryer, portfolioID, reviewID string, target *[]domain.ReviewMetric) error {
	rows, err := q.QueryContext(ctx, `SELECT ordinal,portfolio_id,name,value,unit,as_of,source,deterministic FROM review_metrics WHERE review_id=? ORDER BY ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 0
	for rows.Next() {
		var ordinal, deterministic int
		var rowPortfolioID string
		var item domain.ReviewMetric
		if err := rows.Scan(&ordinal, &rowPortfolioID, &item.Name, &item.Value, &item.Unit, &item.AsOf, &item.Source, &deterministic); err != nil {
			return ErrReviewCorrupt
		}
		item.Deterministic = deterministic == 1
		if rowPortfolioID != portfolioID || ordinal != want || !validateReviewMetrics([]domain.ReviewMetric{item}) {
			return ErrReviewCorrupt
		}
		*target = append(*target, item)
		want++
	}
	return rows.Err()
}

func loadReviewEvidence(ctx context.Context, q reviewQueryer, portfolioID, reviewID string, target *[]domain.ReviewEvidence) error {
	rows, err := q.QueryContext(ctx, `SELECT ordinal,portfolio_id,evidence_id,url_host,url_suffix,title,published_at,accessed_at,availability,freshness,excerpt FROM review_evidence WHERE review_id=? ORDER BY ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 0
	seenEvidence := map[string]bool{}
	for rows.Next() {
		var ordinal int
		var rowPortfolioID string
		var item domain.ReviewEvidence
		var host, suffix string
		var published sql.NullString
		if err := rows.Scan(&ordinal, &rowPortfolioID, &item.EvidenceID, &host, &suffix, &item.Title, &published, &item.AccessedAt, &item.Availability, &item.Freshness, &item.Excerpt); err != nil {
			return ErrReviewCorrupt
		}
		if published.Valid {
			item.PublishedAt = &published.String
		}
		item.URL = "https://" + host + suffix
		storedHost, storedSuffix, urlErr := canonicalEvidenceURL(item.URL)
		if rowPortfolioID != portfolioID || seenEvidence[item.EvidenceID] || urlErr != nil || storedHost != host || storedSuffix != suffix || ordinal != want || !validateReviewEvidence([]domain.ReviewEvidence{item}) {
			return ErrReviewCorrupt
		}
		seenEvidence[item.EvidenceID] = true
		*target = append(*target, item)
		want++
	}
	return rows.Err()
}

func loadReviewFacts(ctx context.Context, q reviewQueryer, portfolioID, reviewID string, target *[]domain.SourcedFact) error {
	rows, err := q.QueryContext(ctx, `SELECT fact_ordinal,portfolio_id,statement FROM review_sourced_facts WHERE review_id=? ORDER BY fact_ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 0
	factIndexes := map[int]int{}
	for rows.Next() {
		var ordinal int
		var rowPortfolioID string
		var fact domain.SourcedFact
		if err := rows.Scan(&ordinal, &rowPortfolioID, &fact.Statement); err != nil {
			return ErrReviewCorrupt
		}
		if rowPortfolioID != portfolioID || ordinal != want || !validReviewText(fact.Statement, 2000) || ordinal >= 32 {
			return ErrReviewCorrupt
		}
		factIndexes[ordinal] = len(*target)
		*target = append(*target, fact)
		want++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	links, err := q.QueryContext(ctx, `SELECT portfolio_id,fact_ordinal,evidence_id,evidence_ordinal FROM review_fact_evidence WHERE review_id=? ORDER BY fact_ordinal,evidence_ordinal`, reviewID)
	if err != nil {
		return err
	}
	defer links.Close()
	linkOrdinals := map[int]int{}
	seenLinks := map[int]map[string]bool{}
	for links.Next() {
		var rowPortfolioID, evidenceID string
		var factOrdinal, evidenceOrdinal int
		if err := links.Scan(&rowPortfolioID, &factOrdinal, &evidenceID, &evidenceOrdinal); err != nil {
			return ErrReviewCorrupt
		}
		index, ok := factIndexes[factOrdinal]
		if rowPortfolioID != portfolioID || !ok || evidenceOrdinal != linkOrdinals[factOrdinal] || !validReviewID(evidenceID, 128) || evidenceOrdinal >= 16 {
			return ErrReviewCorrupt
		}
		if seenLinks[factOrdinal] == nil {
			seenLinks[factOrdinal] = map[string]bool{}
		}
		if seenLinks[factOrdinal][evidenceID] {
			return ErrReviewCorrupt
		}
		seenLinks[factOrdinal][evidenceID] = true
		(*target)[index].EvidenceIDs = append((*target)[index].EvidenceIDs, evidenceID)
		linkOrdinals[factOrdinal]++
	}
	if err := links.Err(); err != nil {
		return err
	}
	for _, fact := range *target {
		if len(fact.EvidenceIDs) == 0 || len(fact.EvidenceIDs) > 16 {
			return ErrReviewCorrupt
		}
	}
	return nil
}

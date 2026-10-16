package httpserver

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

var reviewPathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

func reviewRoutes(database *store.Store, authenticator *auth.Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if portfolioID, reviewID, ok := feedbackTarget(r); ok {
			feedbackRoute(database, authenticator, w, r, portfolioID, reviewID)
			return
		}
		portfolioID, reviewID, collection, ok := reviewTarget(r)
		if !ok {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
		requiresWrite := collection && r.Method == http.MethodPost
		if !reviewAuthenticate(w, r, database, authenticator, portfolioID, requiresWrite) {
			return
		}
		hasQuery := r.URL.RawQuery != "" || r.URL.ForceQuery
		if (collection && r.Method == http.MethodGet && r.URL.ForceQuery) || (collection && r.Method != http.MethodGet && hasQuery) || (!collection && hasQuery) {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
		if collection && r.Method == http.MethodGet {
			reviewList(database, w, r, portfolioID)
			return
		}
		if !collection && r.Method == http.MethodGet {
			reviewDetail(database, w, r, portfolioID, reviewID)
			return
		}
		if collection && r.Method == http.MethodPost {
			reviewSubmit(database, authenticator, w, r, portfolioID)
			return
		}
		if collection {
			w.Header().Set("Allow", "GET, POST")
			writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and POST are supported for this resource.")
		} else {
			w.Header().Set("Allow", "GET")
			writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported for this resource.")
		}
	})
}

func feedbackTarget(r *http.Request) (portfolioID, reviewID string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[4] != "reviews" || parts[6] != "feedback" || !validReviewPathID(parts[3]) || !validReviewPathID(parts[5]) {
		return "", "", false
	}
	return parts[3], parts[5], true
}

type feedbackSession struct{ csrfToken string }

func feedbackRoute(database *store.Store, authenticator *auth.Authenticator, w http.ResponseWriter, r *http.Request, portfolioID, reviewID string) {
	session, ok := feedbackAuthenticate(database, authenticator, w, r, portfolioID)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported for this resource.")
		return
	}
	csrfValues := r.Header.Values(auth.CSRFHeaderName)
	csrf := r.Header.Get(auth.CSRFHeaderName)
	if len(csrfValues) != 1 {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if database == nil || authenticator == nil || len(csrf) < 32 || len(csrf) > 512 || !authenticator.SameOrigin(r) || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.csrfToken)) != 1 {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The request origin or CSRF token is invalid.")
		return
	}
	feedbackSubmit(database, w, r, portfolioID, reviewID)
}

func feedbackAuthenticate(database *store.Store, authenticator *auth.Authenticator, w http.ResponseWriter, r *http.Request, portfolioID string) (feedbackSession, bool) {
	sessionCookies := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == auth.SessionCookieName {
			sessionCookies++
		}
	}
	_, bearerPresent, _ := auth.BearerToken(r)
	if sessionCookies != 1 || bearerPresent || authenticator == nil {
		writeCookieUnauthorized(w)
		return feedbackSession{}, false
	}
	s, err := authenticator.Session(r)
	if err != nil {
		writeCookieUnauthorized(w)
		return feedbackSession{}, false
	}
	if database == nil {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
		return feedbackSession{}, false
	}
	var exists int
	if err := database.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM portfolios WHERE id=?`, portfolioID).Scan(&exists); err != nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return feedbackSession{}, false
	}
	if exists != 1 {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
		return feedbackSession{}, false
	}
	return feedbackSession{csrfToken: s.CSRFToken}, true
}

func writeCookieUnauthorized(w http.ResponseWriter) {
	writePhaseDError(w, http.StatusUnauthorized, "unauthorized", "A browser session is required.")
}

func feedbackSubmit(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID, reviewID string) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	mediaType, _, mediaErr := parseReviewContentType(r.Header.Values("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writePhaseDError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}
	body, tooLarge, readErr := readReviewBody(w, r)
	if readErr != nil || tooLarge || !utf8.Valid(body) || hasDuplicateJSONKeys(body) {
		if tooLarge {
			writePhaseDError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "The request body is too large.")
		} else {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		}
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil || raw == nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	for key := range raw {
		if key != "decision" && key != "note" {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
	}
	if _, exists := raw["decision"]; !exists {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	var submission domain.FeedbackSubmission
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if err := store.ValidateFeedbackSubmission(submission); err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	fingerprint, err := store.FeedbackFingerprint(submission)
	if err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	result, _, err := database.SubmitReviewFeedback(r.Context(), "local-owner", portfolioID, reviewID, keys[0], fingerprint, submission)
	if err != nil {
		writeFeedbackError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func writeFeedbackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrFeedbackInvalid):
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
	case errors.Is(err, store.ErrFeedbackNotFound):
		writePhaseDError(w, http.StatusNotFound, "not_found", "The requested review was not found.")
	case errors.Is(err, store.ErrFeedbackIdempotencyConflict):
		writePhaseDError(w, http.StatusConflict, "idempotency_conflict", "The idempotency key conflicts with an earlier request.")
	case errors.Is(err, store.ErrFeedbackCorrupt):
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	default:
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	}
}

func reviewSubmit(database *store.Store, authenticator *auth.Authenticator, w http.ResponseWriter, r *http.Request, portfolioID string) {
	if database == nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	mediaType, _, mediaErr := parseReviewContentType(r.Header.Values("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writePhaseDError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return
	}
	body, tooLarge, readErr := readReviewBody(w, r)
	if readErr != nil {
		if tooLarge {
			writePhaseDError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "The request body is too large.")
		} else {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		}
		return
	}
	if !utf8.Valid(body) || hasDuplicateJSONKeys(body) {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	allowed := map[string]bool{"snapshot_id": true, "calculation_version": true, "scope": true, "status": true, "recommendation": true, "rationale": true, "risks": true, "counterarguments": true, "conditions": true, "metrics": true, "sourced_facts": true, "model_opinion": true, "evidence": true, "model": true, "generated_at": true}
	required := []string{"snapshot_id", "calculation_version", "scope", "status", "recommendation", "rationale", "risks", "counterarguments", "conditions", "metrics", "sourced_facts", "model_opinion", "evidence", "model", "generated_at"}
	for key := range raw {
		if !allowed[key] {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
	}
	for _, key := range required {
		value, exists := raw[key]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
	}
	if !validReviewScopeJSON(raw["scope"]) {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	var submission domain.ReviewSubmission
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submission); err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	fingerprint, err := store.ReviewFingerprint(submission)
	if err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	principal := authenticator.MachinePrincipal()
	result, replay, err := database.SubmitAdvisoryReview(r.Context(), principal, portfolioID, keys[0], fingerprint, submission)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/portfolios/"+portfolioID+"/reviews/"+result.ReviewID)
	_ = replay
	writeJSON(w, http.StatusCreated, result)
}

func reviewTarget(r *http.Request) (portfolioID, reviewID string, collection, ok bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "reviews" && validReviewPathID(parts[3]) {
		return parts[3], "", true, true
	}
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "reviews" && validReviewPathID(parts[3]) && validReviewPathID(parts[5]) {
		return parts[3], parts[5], false, true
	}
	return "", "", false, false
}

func validReviewScopeJSON(value json.RawMessage) bool {
	var raw map[string]json.RawMessage
	if len(value) == 0 || json.Unmarshal(value, &raw) != nil || raw == nil || len(raw) != 3 {
		return false
	}
	for key := range raw {
		if key != "type" && key != "holding_ids" && key != "instrument_ids" {
			return false
		}
	}
	var scopeType string
	var holdingIDs, instrumentIDs []string
	if err := json.Unmarshal(raw["type"], &scopeType); err != nil || scopeType == "" {
		return false
	}
	if err := json.Unmarshal(raw["holding_ids"], &holdingIDs); err != nil || holdingIDs == nil {
		return false
	}
	if err := json.Unmarshal(raw["instrument_ids"], &instrumentIDs); err != nil || instrumentIDs == nil {
		return false
	}
	return true
}

func validReviewPathID(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && reviewPathID.MatchString(value) && !strings.Contains(value, "%")
}
func validIdempotencyKey(value string) bool {
	return len(value) >= 16 && len(value) <= 128 && idempotencyKeyPattern.MatchString(value)
}

func parseReviewContentType(values []string) (string, map[string]string, error) {
	if len(values) != 1 {
		return "", nil, errors.New("content type count")
	}
	return mimeParseMediaType(values[0])
}

func mimeParseMediaType(value string) (string, map[string]string, error) {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		return "", nil, err
	}
	mediaType = strings.ToLower(mediaType)
	// JSON requests may explicitly declare UTF-8. Other charsets would make
	// the byte-level UTF-8 validation ambiguous, and unknown parameters are not
	// part of this endpoint's media-type contract.
	for name, parameter := range params {
		if name != "charset" || !strings.EqualFold(parameter, "utf-8") {
			return "", nil, errors.New("unsupported content type parameter")
		}
	}
	return mediaType, params, nil
}

func readReviewBody(w http.ResponseWriter, r *http.Request) ([]byte, bool, error) {
	r.Body = http.MaxBytesReader(w, r.Body, store.ReviewBodyLimit)
	body, err := io.ReadAll(io.LimitReader(r.Body, store.ReviewBodyLimit+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, true, err
		}
		return nil, false, err
	}
	if len(body) > store.ReviewBodyLimit {
		return nil, true, nil
	}
	return body, false, nil
}

func reviewAuthenticate(w http.ResponseWriter, r *http.Request, database *store.Store, authenticator *auth.Authenticator, portfolioID string, requireWrite bool) bool {
	_, cookieErr := r.Cookie(auth.SessionCookieName)
	cookiePresent := cookieErr == nil
	token, bearerPresent, bearerWellFormed := auth.BearerToken(r)
	if cookiePresent && bearerPresent {
		writeBearerUnauthorized(w)
		return false
	}
	if authenticator == nil {
		writeBearerUnauthorized(w)
		return false
	}
	if !bearerPresent {
		if !cookiePresent {
			writeBearerUnauthorized(w)
			return false
		}
		if _, err := authenticator.Session(r); err != nil || requireWrite {
			writeBearerUnauthorized(w)
			return false
		}
		return true
	}
	if !bearerWellFormed || !authenticator.MachineTokenValid(token) {
		writeBearerUnauthorized(w)
		return false
	}
	if database == nil {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
		return false
	}
	principal := authenticator.MachinePrincipal()
	allowed, err := database.MachinePrincipalAllowedForRuntime(r.Context(), principal.PrincipalID, portfolioID, principal.PortfolioID, principal.PortfolioRead)
	if err != nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return false
	}
	if !allowed {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
		return false
	}
	if requireWrite {
		allowed, err = database.MachineReviewPrincipalAllowedForRuntime(r.Context(), principal, portfolioID)
		if err != nil {
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
			return false
		}
		if !allowed {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return false
		}
	}
	return true
}

func writeReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
	case errors.Is(err, store.ErrReviewInvalid):
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
	case errors.Is(err, store.ErrReviewIncomplete):
		writePhaseDError(w, http.StatusUnprocessableEntity, "incomplete_review", "The submitted review cannot support the claimed completeness.")
	case errors.Is(err, store.ErrReviewIdempotencyConflict):
		writePhaseDError(w, http.StatusConflict, "idempotency_conflict", "The idempotency key conflicts with an earlier request.")
	case errors.Is(err, store.ErrPortfolioNotFound), errors.Is(err, store.ErrSnapshotNotFound), errors.Is(err, store.ErrReviewTargetNotFound):
		writePhaseDError(w, http.StatusNotFound, "not_found", "The requested portfolio, snapshot, or target was not found.")
	case errors.Is(err, store.ErrSnapshotCorrupt), errors.Is(err, store.ErrReviewCorrupt):
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	default:
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	}
}

func reviewNamespacePath(escapedPath string) bool {
	return strings.Contains(strings.ToLower(escapedPath), "/reviews")
}
func unsafeReviewPath(escapedPath string) bool {
	if strings.Contains(escapedPath, "%") || strings.Contains(escapedPath, "//") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	for _, part := range parts {
		if part == "." || part == ".." || part == "" {
			return true
		}
	}
	return strings.HasSuffix(escapedPath, "/")
}

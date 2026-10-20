package httpserver

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

const holdingMutationBodyLimit = 256 << 10

var holdingIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)
var holdingCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
var holdingDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,18})?$`)

func holdingMutationRoutes(database *store.Store, authenticator *auth.Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
		actor, browser, ok := authenticateHoldingMutation(w, r, database, authenticator)
		if !ok {
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if strings.Contains(r.URL.EscapedPath(), "%") || strings.Contains(r.URL.EscapedPath(), "//") || len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || !holdingIdentifier.MatchString(parts[3]) || parts[4] != "holdings" {
			writePhaseDError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
			return
		}
		portfolioID := parts[3]
		if len(parts) == 5 {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The requested method is not allowed.")
				return
			}
			if browser && !validateHoldingBrowserRequest(w, r, authenticator) || !browser && len(r.Header.Values(auth.CSRFHeaderName)) > 0 {
				return
			}
			createHoldingHTTP(database, w, r, portfolioID, actor)
			return
		}
		if len(parts) == 6 && holdingIdentifier.MatchString(parts[5]) {
			if r.Method != http.MethodDelete {
				w.Header().Set("Allow", http.MethodDelete)
				writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The requested method is not allowed.")
				return
			}
			if browser && !validateHoldingBrowserRequest(w, r, authenticator) || !browser && len(r.Header.Values(auth.CSRFHeaderName)) > 0 {
				return
			}
			removeHoldingHTTP(database, w, r, portfolioID, parts[5], actor)
			return
		}
		writePhaseDError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
	})
}

func authenticateHoldingMutation(w http.ResponseWriter, r *http.Request, database *store.Store, authenticator *auth.Authenticator) (store.MutationActor, bool, bool) {
	if authenticator == nil {
		writePhaseDError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
		return store.MutationActor{}, false, false
	}
	cookies := r.Cookies()
	sessionCount := 0
	for _, cookie := range cookies {
		if cookie.Name == auth.SessionCookieName {
			sessionCount++
		}
	}
	token, bearerPresent, bearerWellFormed := auth.BearerToken(r)
	if sessionCount > 0 && bearerPresent || sessionCount > 1 {
		writeHoldingUnauthorized(w)
		return store.MutationActor{}, false, false
	}
	if bearerPresent {
		if !bearerWellFormed || !authenticator.MachineTokenValid(token) {
			writeHoldingUnauthorized(w)
			return store.MutationActor{}, false, false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if len(parts) < 4 || !holdingIdentifier.MatchString(parts[3]) || database == nil {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return store.MutationActor{}, false, false
		}
		principal := authenticator.MachinePrincipal()
		allowed, err := database.MachineHoldingPrincipalAllowedForRuntime(r.Context(), principal, parts[3])
		if err != nil {
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
			return store.MutationActor{}, false, false
		}
		if !allowed {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return store.MutationActor{}, false, false
		}
		return store.MutationActor{Category: "machine", ID: principal.PrincipalID, Permissions: []string{"portfolio:read", "holdings:write"}}, false, true
	}
	if sessionCount != 1 {
		writeHoldingUnauthorized(w)
		return store.MutationActor{}, false, false
	}
	if _, err := authenticator.Session(r); err != nil {
		writeHoldingUnauthorized(w)
		return store.MutationActor{}, false, false
	}
	return store.MutationActor{Category: "browser_user", ID: "local-owner", Permissions: []string{"portfolio:read"}}, true, true
}

func validateHoldingBrowserRequest(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) bool {
	origins := r.Header.Values("Origin")
	csrf := r.Header.Values(auth.CSRFHeaderName)
	if len(origins) != 1 || !authenticator.CanonicalOriginMatches(origins[0]) || len(csrf) != 1 || csrf[0] == "" {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The request is not authorized.")
		return false
	}
	// RequireCSRF performs the session-bound comparison without accepting a
	// Referer fallback, which is intentionally stricter for holding writes.
	if !authenticator.CanonicalOriginMatches(origins[0]) {
		return false
	}
	session, err := authenticator.Session(r)
	if err != nil || len(csrf[0]) != len(session.CSRFToken) || !constantTimeEqual(csrf[0], session.CSRFToken) {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The request is not authorized.")
		return false
	}
	return true
}

func constantTimeEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func createHoldingHTTP(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID string, actor store.MutationActor) {
	contentTypes := r.Header.Values("Content-Type")
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(contentTypes) != 1 || mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writePhaseDError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "The request media type is not supported.")
		return
	}
	key, ok := holdingHeader(r, "Idempotency-Key")
	if !ok || !holdingIdentifier.MatchString(key) || len(key) < 16 {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	etag, ok := holdingHeader(r, "If-Match")
	if !ok {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	revision, ok := parseHoldingETag(etag, portfolioID)
	if !ok {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	body, tooLarge, err := readHoldingBody(r)
	if err != nil || tooLarge {
		if tooLarge {
			writePhaseDError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "The request body is too large.")
		} else {
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		}
		return
	}
	request, canonical, err := parseHoldingCreate(body)
	if err != nil {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	fingerprint := holdingFingerprint(http.MethodPost, r.URL.EscapedPath(), "create", etag, canonical)
	result, err := database.CreateHolding(r.Context(), store.HoldingCreateParams{PortfolioID: portfolioID, IfMatchRevision: revision, IdempotencyKey: key, RequestFingerprint: fingerprint, Actor: actor, InstrumentID: request.instrumentID, InlineInstrument: request.inline, Quantity: request.quantity, UnitCost: request.unitCost, CostBasis: request.costBasis, Currency: request.currency, AccountID: request.accountID, AcquisitionDate: request.acquisitionDate})
	if err != nil {
		writeHoldingMutationError(w, err)
		return
	}
	w.Header().Set("ETag", holdingETag(portfolioID, result.Receipt.PortfolioRevision))
	w.Header().Set("Location", "/api/v1/portfolios/"+portfolioID+"/holdings/"+result.Receipt.Holding.HoldingID)
	writeJSON(w, http.StatusCreated, result.Receipt)
}

func removeHoldingHTTP(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID, holdingID string, actor store.MutationActor) {
	key, ok := holdingHeader(r, "Idempotency-Key")
	if !ok || !holdingIdentifier.MatchString(key) || len(key) < 16 {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	etag, ok := holdingHeader(r, "If-Match")
	if !ok {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	revision, ok := parseHoldingETag(etag, portfolioID)
	if !ok {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if body, err := io.ReadAll(io.LimitReader(r.Body, 1)); err != nil || len(body) != 0 {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	canonical := []byte{}
	fingerprint := holdingFingerprint(http.MethodDelete, r.URL.EscapedPath(), "remove", etag, canonical)
	result, err := database.RemoveHolding(r.Context(), store.HoldingRemoveParams{PortfolioID: portfolioID, HoldingID: holdingID, IfMatchRevision: revision, IdempotencyKey: key, RequestFingerprint: fingerprint, Actor: actor})
	if err != nil {
		writeHoldingMutationError(w, err)
		return
	}
	w.Header().Set("ETag", holdingETag(portfolioID, result.Receipt.PortfolioRevision))
	writeJSON(w, http.StatusOK, result.Receipt)
}

func holdingHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	return func() (string, bool) {
		if len(values) != 1 || values[0] == "" {
			return "", false
		}
		return values[0], true
	}()
}

func readHoldingBody(r *http.Request) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, holdingMutationBodyLimit+1))
	return body, len(body) > holdingMutationBodyLimit, err
}

func parseHoldingETag(value, portfolioID string) (int, bool) {
	prefix := `"` + portfolioID + `-`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return 0, false
	}
	revision := strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
	if revision == "" || (len(revision) > 1 && revision[0] == '0') {
		return 0, false
	}
	parsed, err := strconv.Atoi(revision)
	return parsed, err == nil && parsed >= 0 && holdingETag(portfolioID, parsed) == value
}
func holdingETag(portfolioID string, revision int) string {
	return fmt.Sprintf(`"%s-%d"`, portfolioID, revision)
}

func holdingFingerprint(method, path, operation, etag string, body []byte) string {
	hash := sha256.Sum256([]byte(method + "\n" + path + "\n" + operation + "\n" + etag + "\n" + string(body)))
	return fmt.Sprintf("%x", hash[:])
}

func writeHoldingUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", auth.MachineBearerRealm)
	writePhaseDError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
}

func writeHoldingMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrStaleRevision):
		writePhaseDError(w, http.StatusConflict, "stale_revision", "The portfolio revision is stale.")
	case errors.Is(err, store.ErrIdempotencyConflict):
		writePhaseDError(w, http.StatusConflict, "idempotency_conflict", "The idempotency key conflicts with an earlier request.")
	case errors.Is(err, store.ErrHoldingNotActive):
		writePhaseDError(w, http.StatusConflict, "holding_not_active", "The holding is not active.")
	case errors.Is(err, store.ErrPortfolioNotFound), errors.Is(err, store.ErrHoldingNotFound), errors.Is(err, store.ErrInstrumentNotFound), errors.Is(err, store.ErrAccountNotFound):
		writePhaseDError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
	case errors.Is(err, store.ErrMutationUnauthorized):
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
	case errors.Is(err, store.ErrInvalidHoldingMutation), errors.Is(err, store.ErrInstrumentConflict):
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
	default:
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	}
}

type holdingCreateParsed struct {
	instrumentID                            string
	inline                                  *store.InlineInstrument
	quantity, unitCost, costBasis, currency string
	accountID, acquisitionDate              *string
}

func parseHoldingCreate(body []byte) (holdingCreateParsed, []byte, error) {
	if len(body) == 0 || !json.Valid(body) || hasDuplicateJSONKeys(body) {
		return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
	}
	allowed := map[string]bool{"instrument": true, "quantity": true, "unit_cost": true, "cost_basis": true, "currency": true, "account_id": true, "acquisition_date": true}
	for key := range raw {
		if !allowed[key] {
			return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
		}
	}
	for _, required := range []string{"instrument", "quantity", "unit_cost", "cost_basis", "currency"} {
		if _, ok := raw[required]; !ok {
			return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
		}
	}
	instrumentID, inline, err := parseHoldingInstrument(raw["instrument"])
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	quantity, err := parseHoldingDecimal(raw["quantity"], true)
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	unitCost, err := parseHoldingDecimal(raw["unit_cost"], false)
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	costBasis, err := parseHoldingDecimal(raw["cost_basis"], false)
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	if !decimalProductEqual(quantity, unitCost, costBasis) {
		return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
	}
	currency, err := parseHoldingString(raw["currency"], false)
	if err != nil || !holdingCurrency.MatchString(currency) {
		return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
	}
	accountID, err := parseOptionalHoldingString(raw, "account_id")
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	if accountID != nil && !holdingIdentifier.MatchString(*accountID) {
		return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
	}
	acquisitionDate, err := parseOptionalHoldingString(raw, "acquisition_date")
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	if acquisitionDate != nil {
		parsed, e := time.Parse("2006-01-02", *acquisitionDate)
		if e != nil || parsed.Year() < 1 || parsed.Format("2006-01-02") != *acquisitionDate {
			return holdingCreateParsed{}, nil, store.ErrInvalidHoldingMutation
		}
	}
	canonical := map[string]any{"instrument": map[string]any{}, "quantity": quantity, "unit_cost": unitCost, "cost_basis": costBasis, "currency": currency}
	if instrumentID != "" {
		canonical["instrument"].(map[string]any)["instrument_id"] = instrumentID
	} else {
		canonical["instrument"] = inlineMap(inline)
	}
	if accountID != nil {
		canonical["account_id"] = *accountID
	} else if _, exists := raw["account_id"]; exists {
		canonical["account_id"] = nil
	}
	if acquisitionDate != nil {
		canonical["acquisition_date"] = *acquisitionDate
	} else if _, exists := raw["acquisition_date"]; exists {
		canonical["acquisition_date"] = nil
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return holdingCreateParsed{}, nil, err
	}
	return holdingCreateParsed{instrumentID: instrumentID, inline: inline, quantity: quantity, unitCost: unitCost, costBasis: costBasis, currency: currency, accountID: accountID, acquisitionDate: acquisitionDate}, encoded, nil
}

func parseHoldingInstrument(raw json.RawMessage) (string, *store.InlineInstrument, error) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil || hasDuplicateJSONKeys(raw) {
		return "", nil, store.ErrInvalidHoldingMutation
	}
	if value, ok := fields["instrument_id"]; ok {
		if len(fields) != 1 {
			return "", nil, store.ErrInvalidHoldingMutation
		}
		id, err := parseHoldingString(value, false)
		if err != nil || !holdingIdentifier.MatchString(id) {
			return "", nil, store.ErrInvalidHoldingMutation
		}
		return id, nil, nil
	}
	allowed := map[string]bool{"symbol": true, "description": true, "currency": true, "asset_type": true, "exchange": true}
	for key := range fields {
		if !allowed[key] {
			return "", nil, store.ErrInvalidHoldingMutation
		}
	}
	symbol, err := parseHoldingString(fields["symbol"], false)
	if err != nil || len(symbol) > 64 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`).MatchString(symbol) {
		return "", nil, store.ErrInvalidHoldingMutation
	}
	currency, err := parseHoldingString(fields["currency"], false)
	if err != nil || !holdingCurrency.MatchString(currency) {
		return "", nil, store.ErrInvalidHoldingMutation
	}
	values := map[string]*string{}
	for _, name := range []string{"description", "asset_type", "exchange"} {
		if value, exists := fields[name]; exists {
			parsed, e := parseHoldingString(value, false)
			if e != nil || parsed == "" {
				return "", nil, store.ErrInvalidHoldingMutation
			}
			values[name] = &parsed
		}
	}
	if values["description"] != nil && len(*values["description"]) > 256 || values["asset_type"] != nil && (len(*values["asset_type"]) > 64 || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(*values["asset_type"])) || values["exchange"] != nil && (len(*values["exchange"]) > 64 || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(*values["exchange"])) {
		return "", nil, store.ErrInvalidHoldingMutation
	}
	inline := &store.InlineInstrument{Symbol: symbol, Currency: currency}
	if values["description"] != nil {
		inline.Description = *values["description"]
	}
	if values["asset_type"] != nil {
		inline.AssetType = *values["asset_type"]
	}
	if values["exchange"] != nil {
		inline.Exchange = *values["exchange"]
	}
	return "", inline, nil
}

func inlineMap(inline *store.InlineInstrument) map[string]any {
	result := map[string]any{"symbol": inline.Symbol, "currency": inline.Currency}
	if inline.Description != "" {
		result["description"] = inline.Description
	}
	if inline.AssetType != "" {
		result["asset_type"] = inline.AssetType
	}
	if inline.Exchange != "" {
		result["exchange"] = inline.Exchange
	}
	return result
}
func parseHoldingString(raw json.RawMessage, nullable bool) (string, error) {
	if nullable && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", store.ErrInvalidHoldingMutation
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", store.ErrInvalidHoldingMutation
	}
	return value, nil
}
func parseOptionalHoldingString(raw map[string]json.RawMessage, name string) (*string, error) {
	value, exists := raw[name]
	if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, nil
	}
	parsed, err := parseHoldingString(value, false)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
func parseHoldingDecimal(raw json.RawMessage, quantity bool) (string, error) {
	value, err := parseHoldingString(raw, false)
	if err != nil || len(value) == 0 || len(value) > 40 || !holdingDecimal.MatchString(value) {
		return "", store.ErrInvalidHoldingMutation
	}
	if quantity && strings.Trim(value, "0.") == "" {
		return "", store.ErrInvalidHoldingMutation
	}
	return value, nil
}
func decimalProductEqual(quantity, unitCost, costBasis string) bool {
	q, qs := decimalParts(quantity)
	u, us := decimalParts(unitCost)
	c, cs := decimalParts(costBasis)
	productScale := qs + us
	targetScale := productScale
	if cs > targetScale {
		targetScale = cs
	}
	product := new(bigInt).Mul(q, u)
	if targetScale > productScale {
		product.Mul(product, tenPow(targetScale-productScale))
	}
	if targetScale > cs {
		c.Mul(c, tenPow(targetScale-cs))
	}
	return product.Equal(c)
}

// Small decimal integer helpers keep HTTP validation independent of float64.
type bigInt struct{ value []byte }

func (n *bigInt) Mul(a, b *bigInt) *bigInt {
	n.value = multiplyDecimalBytes(a.value, b.value)
	return n
}
func (n *bigInt) Equal(other *bigInt) bool {
	return string(trimDecimalBytes(n.value)) == string(trimDecimalBytes(other.value))
}
func (n *bigInt) Set(value []byte) *bigInt { n.value = append([]byte(nil), value...); return n }
func decimalParts(value string) (*bigInt, int) {
	parts := strings.SplitN(value, ".", 2)
	scale := 0
	digits := parts[0]
	if len(parts) == 2 {
		scale = len(parts[1])
		digits += parts[1]
	}
	return (&bigInt{}).Set([]byte(digits)), scale
}
func tenPow(power int) *bigInt {
	value := []byte("1")
	for i := 0; i < power; i++ {
		value = append(value, '0')
	}
	return (&bigInt{}).Set(value)
}
func trimDecimalBytes(value []byte) []byte {
	value = bytes.TrimLeft(value, "0")
	if len(value) == 0 {
		return []byte("0")
	}
	return value
}
func multiplyDecimalBytes(left, right []byte) []byte {
	left = trimDecimalBytes(left)
	right = trimDecimalBytes(right)
	result := make([]int, len(left)+len(right))
	for i := len(left) - 1; i >= 0; i-- {
		for j := len(right) - 1; j >= 0; j-- {
			result[i+j+1] += int(left[i]-'0') * int(right[j]-'0')
		}
	}
	for i := len(result) - 1; i > 0; i-- {
		result[i-1] += result[i] / 10
		result[i] %= 10
	}
	start := 0
	for start < len(result)-1 && result[start] == 0 {
		start++
	}
	output := make([]byte, len(result)-start)
	for i := start; i < len(result); i++ {
		output[i-start] = byte(result[i]) + '0'
	}
	return output
}

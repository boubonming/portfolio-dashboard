package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	appconfig "github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"github.com/boubonming/portfolio-dashboard/internal/web"
)

// NewStaticHandler serves only public assets and health. It intentionally has
// no database or private API access.
func NewStaticHandler() (http.Handler, error) { return newHandler(nil, nil, appconfig.Config{}) }

// NewHandlerWithAuth is the production constructor. Authentication must be
// configured explicitly before private routes are registered.
func NewHandlerWithAuth(database *store.Store, config auth.Config) (http.Handler, error) {
	authenticator, err := auth.New(config)
	if err != nil {
		return nil, err
	}
	if database != nil {
		if err := database.SetActivityCursorSecret(authenticator.ActivityCursorSecret()); err != nil {
			return nil, err
		}
		if err := database.SetReviewCursorSecret(authenticator.ReviewCursorSecret()); err != nil {
			return nil, err
		}
		if err := database.RegisterMachinePrincipalIdentity(context.Background(), authenticator.MachinePrincipal()); err != nil {
			return nil, err
		}
	}
	return newHandler(database, authenticator, appconfig.Config{Auth: config})
}

// NewHandlerWithConfig is the production constructor that also wires the
// server-side provider clients. The legacy constructor above remains useful
// to callers that only need authenticated read routes.
func NewHandlerWithConfig(database *store.Store, configuration appconfig.Config) (http.Handler, error) {
	authenticator, err := auth.New(configuration.Auth)
	if err != nil {
		return nil, err
	}
	if database != nil {
		if err := database.SetActivityCursorSecret(authenticator.ActivityCursorSecret()); err != nil {
			return nil, err
		}
		if err := database.SetReviewCursorSecret(authenticator.ReviewCursorSecret()); err != nil {
			return nil, err
		}
		if err := database.RegisterMachinePrincipalIdentity(context.Background(), authenticator.MachinePrincipal()); err != nil {
			return nil, err
		}
	}
	return newHandler(database, authenticator, configuration)
}

func newHandler(database *store.Store, authenticator *auth.Authenticator, configuration appconfig.Config) (http.Handler, error) {
	files, err := web.Files()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	// Register the namespace roots explicitly. Without these exact patterns,
	// ServeMux redirects a slashless API prefix to its slash-suffixed handler,
	// which turns an API error into an HTML redirect response.
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/v1", apiNotFound)
	mux.HandleFunc("/api/v1/auth", apiNotFound)
	mux.HandleFunc("/api/v1/portfolios", apiNotFound)
	mux.HandleFunc("/api/v1/auth/", authRoutes(authenticator))
	if authenticator != nil {
		snapshot := noStorePrivate(snapshotRoutes(database, authenticator))
		mux.Handle("/api/v1/portfolios/{portfolioId}/snapshots/latest", snapshot)
		mux.Handle("/api/v1/portfolios/{portfolioId}/snapshots/{snapshotId}", snapshot)
		// Catch malformed snapshot targets before the broader portfolio handler
		// can turn them into a browser-only authentication response. This keeps
		// generic target validation ahead of authentication and prevents
		// ServeMux's path-cleaning redirect from disclosing route shape.
		mux.Handle("/api/v1/portfolios/{portfolioId}/snapshots/", snapshot)
		mux.Handle("/api/v1/portfolios/{portfolioId}/snapshots", snapshot)
		reviews := noStorePrivate(reviewRoutes(database, authenticator))
		mux.Handle("/api/v1/portfolios/{portfolioId}/reviews", reviews)
		mux.Handle("/api/v1/portfolios/{portfolioId}/reviews/", reviews)
		mutations := noStorePrivate(holdingMutationRoutes(database, authenticator))
		mux.Handle("/api/v1/portfolios/{portfolioId}/holdings", mutations)
		mux.Handle("/api/v1/portfolios/{portfolioId}/holdings/{holdingId}", mutations)
		overview := noStorePrivate(portfolioOverviewRoutes(database, authenticator))
		mux.Handle("/api/v1/portfolios/{portfolioId}/overview", overview)
		market := noStorePrivate(authenticator.RequireAuth(portfolioCSRF(authenticator, newMarketWorkflowEndpoint(database, configuration))))
		mux.Handle("/api/v1/portfolios/{portfolioId}/market-data", market)
		mux.Handle("/api/v1/portfolios/{portfolioId}/market-data/", market)
	}
	portfolio := http.Handler(portfolioRoutes(database))
	if authenticator != nil {
		portfolio = portfolioCSRF(authenticator, portfolio)
		portfolio = authenticator.RequireAuth(portfolio)
	}
	portfolio = noStorePrivate(portfolio)
	mux.Handle("/api/v1/portfolios/", portfolio)
	mux.HandleFunc("/api/", apiNotFound)
	mux.HandleFunc("/", spa(files))
	return privatePathGuard(mux), nil
}

// privatePathGuard runs before ServeMux. ServeMux canonicalizes paths that
// contain dot segments by issuing a redirect, which would bypass endpoint
// validation and disclose a route shape.
func privatePathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (snapshotNamespacePath(r.URL.EscapedPath()) && unsafeSnapshotPath(r.URL.EscapedPath())) || (reviewNamespacePath(r.URL.EscapedPath()) && unsafeReviewPath(r.URL.EscapedPath())) || (holdingNamespacePath(r.URL.EscapedPath()) && unsafeHoldingPath(r.URL.EscapedPath())) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Vary", "Cookie")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func snapshotNamespacePath(escapedPath string) bool {
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "snapshots" {
		return true
	}
	// Keep malformed/encoded variants in the same generic safety gate instead
	// of allowing the broader portfolio handler or ServeMux to respond first.
	return strings.Contains(strings.ToLower(escapedPath), "snapshots")
}

func unsafeSnapshotPath(escapedPath string) bool {
	if strings.Contains(escapedPath, "%") || strings.Contains(escapedPath, "//") {
		return true
	}
	for _, part := range strings.Split(strings.TrimPrefix(escapedPath, "/"), "/") {
		if part == "." || part == ".." || part == "" {
			return true
		}
	}
	return strings.HasSuffix(escapedPath, "/")
}

func holdingNamespacePath(escapedPath string) bool {
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		decodedPath = escapedPath
	}
	for _, candidate := range []string{path.Clean(escapedPath), path.Clean(decodedPath)} {
		parts := strings.Split(strings.TrimPrefix(candidate, "/"), "/")
		if holdingMutationShape(parts) {
			return true
		}
	}
	return false
}

func unsafeHoldingPath(escapedPath string) bool {
	if strings.Contains(escapedPath, "%") || escapedPath != path.Clean(escapedPath) {
		return true
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil || decodedPath != escapedPath || strings.HasSuffix(escapedPath, "/") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	return holdingMutationShape(parts) && len(parts) != 5 && len(parts) != 6
}

func holdingMutationShape(parts []string) bool {
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[3] == "" || parts[4] != "holdings" {
		return false
	}
	// The portfolio thesis API owns /holdings/{id}/thesis; leave that
	// established namespace to its own route validation.
	return len(parts) < 7 || parts[6] != "thesis"
}

func portfolioCSRF(authenticator *auth.Authenticator, next http.Handler) http.Handler {
	protected := authenticator.RequireCSRF(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Export is a read-only route. Let its own method dispatch report 405
		// before the global write-request CSRF check, without changing CSRF
		// requirements for thesis writes or any future state-changing route.
		if isPortfolioExportPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func isPortfolioExportPath(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	return len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[3] != "" && parts[4] == "export.csv"
}

func health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func authRoutes(authenticator *auth.Authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeJSONNoStore(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
				return
			}
			login(authenticator, w, r)
		case "/api/v1/auth/session":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeJSONNoStore(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
				return
			}
			s, err := authenticator.Session(r)
			if err != nil {
				writeJSONNoStore(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			writeJSONNoStore(w, http.StatusOK, auth.SessionResponse{Authenticated: true, CSRFToken: s.CSRFToken, ExpiresAt: s.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")})
		case "/api/v1/auth/logout":
			logout := authenticator.RequireAuth(authenticator.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = authenticator.Logout(w, r)
				writeJSONNoStore(w, http.StatusOK, map[string]bool{"authenticated": false})
			})))
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeJSONNoStore(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
				return
			}
			logout.ServeHTTP(w, r)
		default:
			writeJSONNoStore(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		}
	}
}

func login(authenticator *auth.Authenticator, w http.ResponseWriter, r *http.Request) {
	if !authenticator.SameOrigin(r) {
		writeJSONNoStore(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writeJSONNoStore(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var request auth.LoginRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Username) == "" || request.Password == "" {
		writeJSONNoStore(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if extra := decoder.Decode(&struct{}{}); extra != io.EOF {
		writeJSONNoStore(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	result, ok := authenticator.Login(w, r, request)
	if !ok {
		writeJSONNoStore(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSONNoStore(w, http.StatusOK, result)
}

func portfolioOverview(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[4] != "overview" || parts[3] == "" || r.URL.Path != "/api/v1/portfolios/"+parts[3]+"/overview" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if database == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		currency := r.URL.Query().Get("currency")
		view, err := database.PortfolioOverview(r.Context(), parts[3], currency)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrPortfolioNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
			case errors.Is(err, store.ErrInvalidReportingCurrency):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_reporting_currency"})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			}
			return
		}
		var revision int
		if err := database.DB.QueryRowContext(r.Context(), `SELECT holdings_revision FROM portfolios WHERE id=?`, parts[3]).Scan(&revision); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return
		}
		w.Header().Set("ETag", holdingETag(parts[3], revision))
		writeJSON(w, http.StatusOK, view)
	}
}

func portfolioOverviewRoutes(database *store.Store, authenticator *auth.Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticatePortfolioOverview(w, r, database, authenticator) {
			return
		}
		portfolioOverview(database).ServeHTTP(w, r)
	})
}

func authenticatePortfolioOverview(w http.ResponseWriter, r *http.Request, database *store.Store, authenticator *auth.Authenticator) bool {
	if authenticator == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return false
	}
	sessionCount := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == auth.SessionCookieName {
			sessionCount++
		}
	}
	token, bearerPresent, bearerWellFormed := auth.BearerToken(r)
	if sessionCount > 1 || (sessionCount == 1 && bearerPresent) {
		writeBearerUnauthorized(w)
		return false
	}
	if bearerPresent {
		if !bearerWellFormed || !authenticator.MachineTokenValid(token) {
			writeBearerUnauthorized(w)
			return false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if database == nil || len(parts) != 5 || !canonicalPathID.MatchString(parts[3]) {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return false
		}
		principal := authenticator.MachinePrincipal()
		allowed, err := database.MachinePrincipalAllowedForRuntime(r.Context(), principal.PrincipalID, parts[3], principal.PortfolioID, principal.PortfolioRead)
		if err != nil {
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
			return false
		}
		if !allowed {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return false
		}
		return true
	}
	if sessionCount == 1 {
		if _, err := authenticator.Session(r); err != nil {
			writeBearerUnauthorized(w)
			return false
		}
		return true
	}
	writeBearerUnauthorized(w)
	return false
}

func portfolioRoutes(database *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) >= 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "holdings" {
			portfolioThesis(database).ServeHTTP(w, r)
			return
		}
		if len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[3] != "" {
			switch parts[4] {
			case "export.csv":
				portfolioExport(database).ServeHTTP(w, r)
				return
			case "overview":
				portfolioOverview(database).ServeHTTP(w, r)
				return
			case "activity":
				portfolioActivity(database).ServeHTTP(w, r)
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	})
}

func portfolioThesis(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		valid := len(parts) >= 7 && len(parts) <= 8 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[3] != "" && parts[4] == "holdings" && parts[5] != "" && parts[6] == "thesis" && (len(parts) == 7 || parts[7] == "history")
		if !valid {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if database == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		var count int
		if err := database.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM portfolios WHERE id=?", parts[3]).Scan(&count); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return
		}
		if count == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
			return
		}
		if err := database.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM lots WHERE id=? AND portfolio_id=?", parts[5], parts[3]).Scan(&count); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return
		}
		if count == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "holding_not_found"})
			return
		}
		if len(parts) == 8 {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
				return
			}
			history, err := database.ThesisHistory(r.Context(), parts[3], parts[5])
			if err != nil {
				thesisHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": history})
			return
		}
		switch r.Method {
		case http.MethodGet:
			thesis, err := database.GetThesis(r.Context(), parts[3], parts[5])
			if err != nil {
				thesisHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, thesis)
		case http.MethodPut:
			if err := saveThesisRequest(database, w, r, parts[3], parts[5]); err != nil {
				thesisHTTPError(w, err)
			}
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		}
	}
}

func saveThesisRequest(database *store.Store, w http.ResponseWriter, r *http.Request, portfolioID, holdingID string) error {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		return fmt.Errorf("%w: content type", store.ErrInvalidThesis)
	}
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxThesisBodyBytes)
	body, err := io.ReadAll(io.LimitReader(r.Body, store.MaxThesisBodyBytes+1))
	if err != nil || len(body) > store.MaxThesisBodyBytes || !json.Valid(body) || hasDuplicateJSONKeys(body) {
		return fmt.Errorf("%w: malformed JSON", store.ErrInvalidThesis)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return fmt.Errorf("%w: malformed JSON", store.ErrInvalidThesis)
	}
	allowed := map[string]bool{"summary": true, "intended_holding_period": true, "allocation_limit": true, "reasons_to_hold": true, "risks": true, "reasons_to_sell": true, "conditions_to_change": true, "sources": true, "expected_revision": true}
	for key := range raw {
		if !allowed[key] {
			return fmt.Errorf("%w: unknown field", store.ErrInvalidThesis)
		}
	}
	for _, key := range []string{"summary", "intended_holding_period", "reasons_to_hold", "risks", "reasons_to_sell", "conditions_to_change", "sources", "expected_revision"} {
		if _, ok := raw[key]; !ok {
			return fmt.Errorf("%w: missing field", store.ErrInvalidThesis)
		}
		if key != "allocation_limit" && bytes.Equal(bytes.TrimSpace(raw[key]), []byte("null")) {
			return fmt.Errorf("%w: null field", store.ErrInvalidThesis)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("%w: malformed JSON", store.ErrInvalidThesis)
	}
	var request domain.ThesisUpdate
	strictDecoder := json.NewDecoder(bytes.NewReader(encoded))
	strictDecoder.DisallowUnknownFields()
	if err := strictDecoder.Decode(&request); err != nil {
		return fmt.Errorf("%w: malformed JSON", store.ErrInvalidThesis)
	}
	if extra := strictDecoder.Decode(&struct{}{}); extra != io.EOF {
		return fmt.Errorf("%w: trailing JSON", store.ErrInvalidThesis)
	}
	request.PortfolioID, request.HoldingID = portfolioID, holdingID
	thesis, err := database.SaveThesis(r.Context(), request)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, thesis)
	return nil
}

func hasDuplicateJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONToken(decoder, nil); err != nil {
		return true
	}
	_, err := decoder.Token()
	return err != io.EOF
}

func walkJSONToken(decoder *json.Decoder, first json.Token) error {
	var token json.Token
	var err error
	if first == nil {
		token, err = decoder.Token()
	} else {
		token = first
	}
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			if end, ok := key.(json.Delim); ok && end == '}' {
				return nil
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("object key is not string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate object key")
			}
			seen[name] = struct{}{}
			if err := walkJSONToken(decoder, nil); err != nil {
				return err
			}
		}
	case '[':
		for {
			next, err := decoder.Token()
			if err != nil {
				return err
			}
			if end, ok := next.(json.Delim); ok && end == ']' {
				return nil
			}
			if err := walkJSONToken(decoder, next); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
}

func thesisHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidThesis):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_thesis"})
	case errors.Is(err, store.ErrThesisConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "thesis_revision_conflict"})
	case errors.Is(err, store.ErrThesisNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "thesis_not_found"})
	case errors.Is(err, store.ErrHoldingNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "holding_not_found"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
	}
}

func portfolioActivity(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || parts[4] != "activity" || parts[3] == "" || r.URL.Path != "/api/v1/portfolios/"+parts[3]+"/activity" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if database == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		query, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pagination"})
			return
		}
		for key, values := range query {
			if (key != "limit" && key != "cursor") || len(values) != 1 || values[0] == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pagination"})
				return
			}
		}
		options, err := store.ParseActivityPagination(query.Get("limit"), query.Get("cursor"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pagination"})
			return
		}
		page, err := database.Activity(r.Context(), parts[3], options)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrPortfolioNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "portfolio_not_found"})
			case errors.Is(err, store.ErrInvalidActivityPagination):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_pagination"})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			}
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeJSONNoStore(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, value)
}

func noStorePrivate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Vary", "Cookie")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func spa(files fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested == "" {
			requested = "index.html"
		}
		if file, err := fs.ReadFile(files, requested); err == nil {
			if strings.HasSuffix(requested, ".js") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			if strings.HasSuffix(requested, ".css") {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			if strings.HasSuffix(requested, ".html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			}
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(file)
			}
			return
		}
		if strings.Contains(path.Base(requested), ".") || strings.HasPrefix(requested, "assets/") {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(files, "index.html")
		if err != nil {
			http.Error(w, "frontend unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	}
}

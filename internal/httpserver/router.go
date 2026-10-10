package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"github.com/boubonming/portfolio-dashboard/internal/web"
)

// NewStaticHandler serves only public assets and health. It intentionally has
// no database or private API access.
func NewStaticHandler() (http.Handler, error) { return newHandler(nil, nil) }

// NewHandlerWithAuth is the production constructor. Authentication must be
// configured explicitly before private routes are registered.
func NewHandlerWithAuth(database *store.Store, config auth.Config) (http.Handler, error) {
	authenticator, err := auth.New(config)
	if err != nil {
		return nil, err
	}
	return newHandler(database, authenticator)
}

func newHandler(database *store.Store, authenticator *auth.Authenticator) (http.Handler, error) {
	files, err := web.Files()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/api/v1/auth/", authRoutes(authenticator))
	portfolio := http.Handler(portfolioOverview(database))
	if authenticator != nil {
		portfolio = authenticator.RequireAuth(authenticator.RequireCSRF(portfolio))
	}
	portfolio = noStorePrivate(portfolio)
	mux.Handle("/api/v1/portfolios/", portfolio)
	mux.HandleFunc("/api/", apiNotFound)
	mux.HandleFunc("/", spa(files))
	return mux, nil
}

func health(w http.ResponseWriter, _ *http.Request) {
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
	origin := r.Header.Get("Origin")
	referer := r.Header.Get("Referer")
	if (origin == "" && referer == "") || (origin != "" && !authenticator.CanonicalOriginMatches(origin)) || (referer != "" && !authenticator.CanonicalOriginMatches(referer)) {
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
		writeJSON(w, http.StatusOK, view)
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

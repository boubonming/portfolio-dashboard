package httpserver

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/store"
	"github.com/boubonming/portfolio-dashboard/internal/web"
)

// NewHandler serves the static app and, when a store is supplied, the
// read-only API. The variadic form preserves the original static-only call.
func NewHandler(databases ...*store.Store) (http.Handler, error) {
	var database *store.Store
	if len(databases) > 0 {
		database = databases[0]
	}
	return newHandler(database)
}

// NewHandlerWithStore is the explicit constructor for application startup.
func NewHandlerWithStore(database *store.Store) (http.Handler, error) { return newHandler(database) }

func newHandler(database *store.Store) (http.Handler, error) {
	files, err := web.Files()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/api/v1/portfolios/", portfolioOverview(database))
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

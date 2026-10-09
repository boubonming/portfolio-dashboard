package httpserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/web"
)

func NewHandler() (http.Handler, error) {
	files, err := web.Files()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/api/", apiNotFound)
	mux.HandleFunc("/", spa(files))
	return mux, nil
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "not_found"})
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
				w.Write(file)
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
			w.Write(index)
		}
	}
}

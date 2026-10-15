package httpserver

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

var canonicalPathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)

const maxSnapshotPathID = 128

type snapshotEndpoint struct {
	database      *store.Store
	authenticator *auth.Authenticator
}

func snapshotRoutes(database *store.Store, authenticator *auth.Authenticator) http.Handler {
	handler := &snapshotEndpoint{database: database, authenticator: authenticator}
	return http.HandlerFunc(handler.serveHTTP)
}

func (h *snapshotEndpoint) serveHTTP(w http.ResponseWriter, r *http.Request) {
	portfolioID, snapshotID, latest, ok := snapshotTarget(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if r.URL.RawQuery != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if !h.authenticate(w, r, portfolioID) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if h.database == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		return
	}
	result, err := h.database.ReviewSnapshot(r.Context(), portfolioID, snapshotID, latest)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPortfolioNotFound), errors.Is(err, store.ErrSnapshotNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		case errors.Is(err, store.ErrSnapshotCorrupt):
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func snapshotTarget(r *http.Request) (portfolioID, snapshotID string, latest, ok bool) {
	path := r.URL.EscapedPath()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 7 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "snapshots" && parts[5] == "latest" && parts[3] != "" && parts[6] == "" {
		// The trailing empty component is never accepted; this branch exists only
		// to keep the shape check explicit for escaped request targets.
		return "", "", false, false
	}
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "snapshots" && parts[5] == "latest" {
		if len(parts[3]) > maxSnapshotPathID || !canonicalPathID.MatchString(parts[3]) || strings.Contains(parts[3], "%") {
			return "", "", false, false
		}
		return parts[3], "", true, true
	}
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "portfolios" && parts[4] == "snapshots" && canonicalPathID.MatchString(parts[3]) && canonicalPathID.MatchString(parts[5]) && !strings.Contains(parts[3]+parts[5], "%") {
		if len(parts[3]) > maxSnapshotPathID || len(parts[5]) > maxSnapshotPathID {
			return "", "", false, false
		}
		return parts[3], parts[5], false, true
	}
	return "", "", false, false
}

func (h *snapshotEndpoint) authenticate(w http.ResponseWriter, r *http.Request, portfolioID string) bool {
	_, cookieErr := r.Cookie(auth.SessionCookieName)
	cookiePresent := cookieErr == nil
	token, bearerPresent, bearerWellFormed := auth.BearerToken(r)
	if cookiePresent && bearerPresent {
		writeBearerUnauthorized(w)
		return false
	}
	if bearerPresent {
		if !bearerWellFormed || !h.authenticator.MachineTokenValid(token) {
			writeBearerUnauthorized(w)
			return false
		}
		if h.database == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return false
		}
		principal := h.authenticator.MachinePrincipal()
		allowed, err := h.database.MachinePrincipalAllowed(r.Context(), principal.PrincipalID, portfolioID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error"})
			return false
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return false
		}
		return true
	}
	if cookiePresent {
		if _, err := h.authenticator.Session(r); err != nil {
			writeBearerUnauthorized(w)
			return false
		}
		return true
	}
	writeBearerUnauthorized(w)
	return false
}

func writeBearerUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", auth.MachineBearerRealm)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

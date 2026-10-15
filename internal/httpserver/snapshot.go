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
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if r.URL.RawQuery != "" {
		writePhaseDError(w, http.StatusBadRequest, "invalid_request", "Request does not satisfy the API contract.")
		return
	}
	if !h.authenticate(w, r, portfolioID) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writePhaseDError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported for this resource.")
		return
	}
	if h.database == nil {
		writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		return
	}
	result, err := h.database.ReviewSnapshot(r.Context(), portfolioID, snapshotID, latest)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPortfolioNotFound), errors.Is(err, store.ErrSnapshotNotFound):
			writePhaseDError(w, http.StatusNotFound, "not_found", "The requested snapshot was not found.")
		case errors.Is(err, store.ErrSnapshotCorrupt):
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
		default:
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
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
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return false
		}
		principal := h.authenticator.MachinePrincipal()
		allowed, err := h.database.MachinePrincipalAllowed(r.Context(), principal.PrincipalID, portfolioID, principal.PortfolioRead)
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
	writePhaseDError(w, http.StatusUnauthorized, "unauthorized", "Authentication is required.")
}

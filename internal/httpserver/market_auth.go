package httpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

type marketWorkflowAuth struct {
	actor   store.MutationActor
	browser bool
}

type marketWorkflowAuthContextKey struct{}

func marketWorkflowRoutes(database *store.Store, authenticator *auth.Authenticator, endpoint http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestAuth, ok := authenticateMarketWorkflow(w, r, database, authenticator)
		if !ok {
			return
		}
		if requestAuth.browser {
			if !validateMarketBrowserRequest(w, r, authenticator) {
				return
			}
		} else if len(r.Header.Values(auth.CSRFHeaderName)) != 0 {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The request is not authorized.")
			return
		}
		ctx := context.WithValue(r.Context(), marketWorkflowAuthContextKey{}, requestAuth)
		endpoint.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authenticateMarketWorkflow(w http.ResponseWriter, r *http.Request, database *store.Store, authenticator *auth.Authenticator) (marketWorkflowAuth, bool) {
	if authenticator == nil {
		writeBearerUnauthorized(w)
		return marketWorkflowAuth{}, false
	}
	sessionCount := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == auth.SessionCookieName {
			sessionCount++
		}
	}
	token, bearerPresent, bearerWellFormed := auth.BearerToken(r)
	if sessionCount > 0 && bearerPresent || sessionCount > 1 {
		writeBearerUnauthorized(w)
		return marketWorkflowAuth{}, false
	}
	if bearerPresent {
		if !bearerWellFormed || !authenticator.MachineTokenValid(token) {
			writeBearerUnauthorized(w)
			return marketWorkflowAuth{}, false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "portfolios" || !canonicalPathID.MatchString(parts[3]) || parts[4] != "market-data" || database == nil {
			writePhaseDError(w, http.StatusNotFound, "not_found", "The requested resource was not found.")
			return marketWorkflowAuth{}, false
		}
		principal := authenticator.MachinePrincipal()
		refreshRoute := len(parts) == 6 && parts[5] == "refresh" && r.Method == http.MethodPost
		allowed, err := false, error(nil)
		if refreshRoute {
			allowed, err = database.MachinePrincipalAllowedForRuntime(r.Context(), principal.PrincipalID, parts[3], principal.PortfolioID, principal.PortfolioRead)
		} else {
			allowed, err = database.MachineHoldingPrincipalAllowedForRuntime(r.Context(), principal, parts[3])
		}
		if err != nil {
			writePhaseDError(w, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
			return marketWorkflowAuth{}, false
		}
		if !allowed {
			writePhaseDError(w, http.StatusForbidden, "forbidden", "The credential lacks the required permission.")
			return marketWorkflowAuth{}, false
		}
		permissions := []string{"portfolio:read", "holdings:write"}
		if refreshRoute {
			permissions = []string{"portfolio:read"}
		}
		return marketWorkflowAuth{actor: store.MutationActor{Category: "machine", ID: principal.PrincipalID, Permissions: permissions}}, true
	}
	if sessionCount != 1 {
		writeBearerUnauthorized(w)
		return marketWorkflowAuth{}, false
	}
	if _, err := authenticator.Session(r); err != nil {
		writeBearerUnauthorized(w)
		return marketWorkflowAuth{}, false
	}
	actorID, ok := authenticator.BrowserActorID(r)
	if !ok {
		writeBearerUnauthorized(w)
		return marketWorkflowAuth{}, false
	}
	return marketWorkflowAuth{actor: store.MutationActor{Category: "browser_user", ID: actorID, Permissions: []string{"portfolio:read"}}, browser: true}, true
}

func validateMarketBrowserRequest(w http.ResponseWriter, r *http.Request, authenticator *auth.Authenticator) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	session, err := authenticator.Session(r)
	if err != nil || !authenticator.SameOrigin(r) || r.Header.Get(auth.CSRFHeaderName) == "" || len(r.Header.Get(auth.CSRFHeaderName)) != len(session.CSRFToken) || !constantTimeEqual(r.Header.Get(auth.CSRFHeaderName), session.CSRFToken) {
		writePhaseDError(w, http.StatusForbidden, "forbidden", "The request origin or CSRF token is invalid.")
		return false
	}
	return true
}

func marketWorkflowAuthFromContext(ctx context.Context) (marketWorkflowAuth, bool) {
	value, ok := ctx.Value(marketWorkflowAuthContextKey{}).(marketWorkflowAuth)
	return value, ok
}

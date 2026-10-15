package httpserver

import (
	"net/http"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
)

func TestReviewHTTPRuntimeAndPersistedGrantMatrix(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	request := func(h http.Handler, key string) *http.Response {
		response := reviewBoundaryRequest(h, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, key, "application/json", body)
		return response.Result()
	}
	if _, err := database.DB.Exec(`UPDATE machine_principals SET permission_reviews_write=0 WHERE id=?`, "review-boundary-machine"); err != nil {
		t.Fatal(err)
	}
	response := request(handler, "runtime-write-disabled-key")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("persisted write disabled status=%d", response.StatusCode)
	}
	if _, err := database.DB.Exec(`UPDATE machine_principals SET permission_reviews_write=1,active=0 WHERE id=?`, "review-boundary-machine"); err != nil {
		t.Fatal(err)
	}
	response = request(handler, "runtime-revoked-key")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("persisted revoked status=%d", response.StatusCode)
	}
	if _, err := database.DB.Exec(`UPDATE machine_principals SET active=1,permission_portfolio_read=0 WHERE id=?`, "review-boundary-machine"); err != nil {
		t.Fatal(err)
	}
	response = request(handler, "runtime-read-disabled-key")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("persisted read disabled status=%d", response.StatusCode)
	}

	config := auth.Config{Username: "fixture-user", PasswordHash: sessionPasswordHash(t), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "review-boundary-machine", PortfolioID: "other-portfolio", BearerSecret: reviewBoundarySecret, PortfolioRead: true, ReviewsWrite: true}}
	rebound, err := NewHandlerWithAuth(database, config)
	if err != nil {
		t.Fatal(err)
	}
	response = request(rebound, "runtime-rebound-key")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("rebound configured portfolio status=%d", response.StatusCode)
	}

	for _, tc := range []struct{ name, portfolio string }{{"configured-mismatch", "foreign"}, {"foreign-request", "foreign"}} {
		t.Run(tc.name, func(t *testing.T) {
			response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/"+tc.portfolio+"/reviews", reviewBoundarySecret, "runtime-"+tc.name+"-key", "application/json", body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

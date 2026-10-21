package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const maximumHoldingProduct = "99999999999999999999999999999999999999980000000000000000000000000000000000000001"

func TestMachineHoldingRouteRoundTripsMaximumAndTinyExactProducts(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	secret := "roundtrip-machine-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
		Machine: auth.MachineConfig{PrincipalID: "roundtrip-machine", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, HoldingsWrite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target, body, key, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	overview := request(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", "", "", "")
	if overview.Code != http.StatusOK || overview.Header().Get("ETag") != `"portfolio-api-0"` {
		t.Fatalf("initial overview status=%d etag=%q body=%s", overview.Code, overview.Header().Get("ETag"), overview.Body.String())
	}
	maximumBody := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"9999999999999999999999999999999999999999","unit_cost":"9999999999999999999999999999999999999999","cost_basis":"99999999999999999999999999999999999999980000000000000000000000000000000000000001","currency":"USD"}`
	first := request(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", maximumBody, "roundtrip-maximum-key-0001", `"portfolio-api-0"`)
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"cost_basis":"`+maximumHoldingProduct+`"`) || first.Header().Get("ETag") != `"portfolio-api-1"` {
		t.Fatalf("maximum create status=%d etag=%q body=%s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	replay := request(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", maximumBody, "roundtrip-maximum-key-0001", `"portfolio-api-0"`)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("maximum replay status=%d body=%s first=%s", replay.Code, replay.Body.String(), first.Body.String())
	}
	readback := request(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", "", "", "")
	if readback.Code != http.StatusOK || readback.Header().Get("ETag") != `"portfolio-api-1"` || !strings.Contains(readback.Body.String(), `"quantity":"9999999999999999999999999999999999999999"`) || !strings.Contains(readback.Body.String(), `"cost_basis":"`+maximumHoldingProduct+`"`) {
		t.Fatalf("maximum readback status=%d etag=%q body=%s", readback.Code, readback.Header().Get("ETag"), readback.Body.String())
	}
	tinyBody := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"0.000000000000000001","unit_cost":"0.000000000000000001","cost_basis":"0.000000000000000000000000000000000001","currency":"USD"}`
	tiny := request(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", tinyBody, "roundtrip-tiny-key-0001", `"portfolio-api-1"`)
	if tiny.Code != http.StatusCreated || !strings.Contains(tiny.Body.String(), `"cost_basis":"0.000000000000000000000000000000000001"`) {
		t.Fatalf("tiny create status=%d body=%s", tiny.Code, tiny.Body.String())
	}
	var lots int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`, "portfolio-api").Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if lots != 3 {
		t.Fatalf("roundtrip lot count=%d, want 3 including fixture", lots)
	}
}

func TestMachineHoldingRevocationRemainsDeniedAcrossRegistrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "machine-restart.sqlite")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := canonicalReviewFixtureTimestamp(time.Now().UTC())
	if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, "restart-instrument", "RESTART", "Restart fixture", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_instruments(portfolio_id,instrument_id,created_at) VALUES(?,?,?)`, "portfolio-default", "restart-instrument", now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, "restart-machine", "portfolio-default", 1, 0, 0, 1, now); err != nil {
		t.Fatal(err)
	}
	secret := "restart-machine-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	config := auth.Config{Username: "fixture-user", PasswordHash: string(testPasswordHash(t)), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "restart-machine", PortfolioID: "portfolio-default", BearerSecret: secret, PortfolioRead: true, HoldingsWrite: true}}
	newHandler := func(database *store.Store) http.Handler {
		handler, err := NewHandlerWithAuth(database, config)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	request := func(handler http.Handler, method, target, body, key, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	handler := newHandler(database)
	body := `{"instrument":{"instrument_id":"restart-instrument"},"quantity":"1","unit_cost":"2","cost_basis":"2","currency":"USD"}`
	denied := request(handler, http.MethodPost, "/api/v1/portfolios/portfolio-default/holdings", body, "restart-denied-key-0001", `"portfolio-default-0"`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("pre-grant status=%d body=%s", denied.Code, denied.Body.String())
	}
	var lots, revision int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`, "portfolio-default").Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id=?`, "portfolio-default").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if lots != 0 || revision != 0 {
		t.Fatalf("rejected pre-grant mutation changed lots=%d revision=%d", lots, revision)
	}
	if changed, err := database.GrantMachineHoldings(ctx, "restart-machine", "portfolio-default"); err != nil || !changed {
		t.Fatalf("grant changed=%v err=%v", changed, err)
	}
	allowed := request(handler, http.MethodPost, "/api/v1/portfolios/portfolio-default/holdings", body, "restart-create-key-0001", `"portfolio-default-0"`)
	if allowed.Code != http.StatusCreated {
		t.Fatalf("post-grant status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	if changed, err := database.RevokeMachineHoldings(ctx, "restart-machine", "portfolio-default"); err != nil || !changed {
		t.Fatalf("revoke changed=%v err=%v", changed, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler = newHandler(database)
	afterRestart := request(handler, http.MethodPost, "/api/v1/portfolios/portfolio-default/holdings", body, "restart-denied-key-0002", `"portfolio-default-1"`)
	if afterRestart.Code != http.StatusForbidden {
		t.Fatalf("post-revocation restart status=%d body=%s", afterRestart.Code, afterRestart.Body.String())
	}
	otherScope := request(handler, http.MethodPost, "/api/v1/portfolios/portfolio-other/holdings", body, "restart-other-scope-key-0001", `"portfolio-other-0"`)
	if otherScope.Code != http.StatusForbidden {
		t.Fatalf("other-scope status=%d body=%s", otherScope.Code, otherScope.Body.String())
	}
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`, "portfolio-default").Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT holdings_revision FROM portfolios WHERE id=?`, "portfolio-default").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if lots != 1 || revision != 1 {
		t.Fatalf("rejected post-restart mutation changed lots=%d revision=%d", lots, revision)
	}
}

func testPasswordHash(t *testing.T) []byte {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

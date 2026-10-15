package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func machineFixtureHandler(t *testing.T) (http.Handler, func()) {
	t.Helper()
	database := apiFixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	machine := auth.MachineConfig{PrincipalID: "machine-fixture", PortfolioID: "portfolio-api", BearerSecret: "machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef", PortfolioRead: true}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true,
		SessionTTL: time.Hour, Machine: machine,
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return handler, func() { database.Close() }
}

func fixtureSessionSecret() string {
	return "YXBpLWZpeHR1cmUtc2Vzc2lvbi1zZWNyZXQtd2l0aC1kaXZlcnNpdHktMTIz"
}

func TestSnapshotMachineReadRequiresScopedPermission(t *testing.T) {
	handler, cleanup := machineFixtureHandler(t)
	defer cleanup()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/latest", nil)
	request.Header.Set("Authorization", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() == "" {
		t.Fatalf("machine latest status=%d body=%s", response.Code, response.Body.String())
	}
	for key, want := range map[string]string{"Cache-Control": "no-store", "Vary": "Cookie", "X-Content-Type-Options": "nosniff"} {
		if response.Header().Get(key) != want {
			t.Fatalf("header %s=%q, want %q", key, response.Header().Get(key), want)
		}
	}
	if !strings.Contains(response.Body.String(), `"state":"no_snapshot"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestSnapshotMachineAuthMatrixAndCanonicalTargets(t *testing.T) {
	handler, cleanup := machineFixtureHandler(t)
	defer cleanup()
	path := "/api/v1/portfolios/portfolio-api/snapshots/latest"
	request := func(method, target, authorization string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	for _, tc := range []struct {
		name          string
		authorization string
		status        int
		challenge     bool
	}{
		{name: "missing", status: http.StatusUnauthorized, challenge: true},
		{name: "malformed", authorization: "Bearer", status: http.StatusUnauthorized, challenge: true},
		{name: "invalid", authorization: "Bearer wrong-machine-secret", status: http.StatusUnauthorized, challenge: true},
		{name: "wrong method", authorization: "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef", status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(map[string]string{"wrong method": http.MethodPost}[tc.name], path, tc.authorization)
			if tc.name != "wrong method" {
				response = request(http.MethodGet, path, tc.authorization)
			}
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.challenge != (response.Header().Get("WWW-Authenticate") == auth.MachineBearerRealm) {
				t.Fatalf("challenge=%q", response.Header().Get("WWW-Authenticate"))
			}
			for key, want := range map[string]string{"Cache-Control": "no-store", "Vary": "Cookie", "X-Content-Type-Options": "nosniff"} {
				if response.Header().Get(key) != want {
					t.Fatalf("header %s=%q, want %q", key, response.Header().Get(key), want)
				}
			}
		})
	}
	response := request(http.MethodGet, path, "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	if response.Code != http.StatusOK {
		t.Fatalf("valid bearer status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/latest/", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing slash status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/../latest", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("traversal status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSnapshotMachineReadsCompleteIncompleteAndHistoricalBranches(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	ctx := context.Background()
	first, err := database.CreateSnapshot(ctx, store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "machine.read.v1", AsOf: time.Now().UTC(), MaxAge: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.CreateSnapshot(ctx, store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "machine.read.v1", AsOf: time.Now().UTC().Add(72 * time.Hour), MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReviewSnapshot(ctx, "portfolio-api", first.SnapshotID, false); err != nil {
		t.Fatalf("reconstruct complete snapshot: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	secret := "machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	handler, err := NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "machine-fixture", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) map[string]any {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	latest := get("/api/v1/portfolios/portfolio-api/snapshots/latest")
	if latest["state"] != "incomplete" || latest["complete"] != false {
		t.Fatalf("latest=%v", latest)
	}
	historical := get("/api/v1/portfolios/portfolio-api/snapshots/" + first.SnapshotID)
	if historical["state"] != "complete" || historical["snapshot_id"] != first.SnapshotID || historical["calculation_version"] != "machine.read.v1" {
		t.Fatalf("historical=%v", historical)
	}
	if _, ok := historical["holdings"].([]any); !ok {
		t.Fatalf("historical holdings=%T", historical["holdings"])
	}
	if latest["snapshot_id"] != second.SnapshotID {
		t.Fatalf("latest snapshot=%v want=%s", latest["snapshot_id"], second.SnapshotID)
	}
}

func TestSnapshotErrorsUsePhaseDEnvelope(t *testing.T) {
	handler, cleanup := machineFixtureHandler(t)
	defer cleanup()
	secret := "machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	tests := []struct {
		name      string
		method    string
		path      string
		authority string
		status    int
		code      string
	}{
		{name: "bad path", method: http.MethodGet, path: "/api/v1/portfolios/portfolio-api/snapshots/", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unauthorized", method: http.MethodGet, path: "/api/v1/portfolios/portfolio-api/snapshots/latest", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "not found", method: http.MethodGet, path: "/api/v1/portfolios/portfolio-api/snapshots/missing", authority: "Bearer " + secret, status: http.StatusNotFound, code: "not_found"},
		{name: "method", method: http.MethodPost, path: "/api/v1/portfolios/portfolio-api/snapshots/latest", authority: "Bearer " + secret, status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authority != "" {
				request.Header.Set("Authorization", tc.authority)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.code || body.Error.Message == "" || len(body.Error.RequestID) == 0 || len(body.Error.RequestID) > 128 {
				t.Fatalf("error envelope=%+v", body.Error)
			}
		})
	}
}

func TestSnapshotMachineGrantIntersectsRuntimeAndDatabaseState(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	secret := "machine-restart-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	base := auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour}
	base.Machine = auth.MachineConfig{PrincipalID: "machine-restart", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true}
	if _, err := NewHandlerWithAuth(database, base); err != nil {
		t.Fatal(err)
	}
	base.Machine.PortfolioRead = false
	second, err := NewHandlerWithAuth(database, base)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/latest", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	second.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("runtime-disabled grant status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := database.DB.Exec(`UPDATE machine_principals SET active=0 WHERE id=?`, "machine-restart"); err != nil {
		t.Fatal(err)
	}
	base.Machine.PortfolioRead = true
	third, err := NewHandlerWithAuth(database, base)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/latest", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response = httptest.NewRecorder()
	third.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("database-revoked grant status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSnapshotMachineConfiguredPortfolioBindingSurvivesRestart(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-rebound", "Rebound", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	secret := "machine-rebound-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	base := auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour}
	base.Machine = auth.MachineConfig{PrincipalID: "machine-rebound", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true}
	if _, err := NewHandlerWithAuth(database, base); err != nil {
		t.Fatal(err)
	}
	base.Machine.PortfolioID = "portfolio-rebound"
	restarted, err := NewHandlerWithAuth(database, base)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/snapshots/latest", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response := httptest.NewRecorder()
	restarted.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("old configured portfolio status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-rebound/snapshots/latest", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	response = httptest.NewRecorder()
	restarted.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("new portfolio without DB grant status=%d body=%s", response.Code, response.Body.String())
	}
}

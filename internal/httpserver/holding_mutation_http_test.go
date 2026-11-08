package httpserver

import (
	"context"
	"encoding/base64"
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

func TestMachineCanCreateAndReplayHoldingThroughMutationRoute(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	secret := "holding-machine-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("holding-fixture-session-secret-with-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
		Machine: auth.MachineConfig{PrincipalID: "holding-machine", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, HoldingsWrite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"12.5000","unit_cost":"10.00","cost_basis":"125.0000","currency":"USD"}`
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "machine-create-key-1")
		r.Header.Set("If-Match", `"portfolio-api-0"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	first := request()
	if first.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := request()
	if second.Code != http.StatusCreated || second.Body.String() != first.Body.String() {
		t.Fatalf("replay status=%d body=%s first=%s", second.Code, second.Body.String(), first.Body.String())
	}
	queryRequest := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings?unexpected=1", strings.NewReader(body))
	queryRequest.Header.Set("Authorization", "Bearer "+secret)
	queryRequest.Header.Set("Content-Type", "application/json")
	queryRequest.Header.Set("Idempotency-Key", "machine-query-key-1")
	queryRequest.Header.Set("If-Match", `"portfolio-api-1"`)
	queryResponse := httptest.NewRecorder()
	handler.ServeHTTP(queryResponse, queryRequest)
	if queryResponse.Code != http.StatusBadRequest {
		t.Fatalf("query variant status=%d body=%s", queryResponse.Code, queryResponse.Body.String())
	}
	if first.Header().Get("Location") == "" || first.Header().Get("ETag") != `"portfolio-api-1"` {
		t.Fatalf("headers location=%q etag=%q", first.Header().Get("Location"), first.Header().Get("ETag"))
	}
}

func TestHoldingMutationLostResponseReplayPreservesSnapshotReviewThesisAndActivity(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	snapshot, err := database.CreateSnapshot(ctx, store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "s1.replay.v1", AsOf: now, MaxAge: 2 * time.Hour})
	if err != nil || snapshot.SnapshotID == "" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	timestamp := canonicalReviewFixtureTimestamp(now)
	retainUntil := canonicalReviewFixtureTimestamp(now.AddDate(7, 0, 0))
	if _, err := database.DB.Exec(`INSERT INTO reviews(id,snapshot_id,instrument_id,status,rationale,payload,created_at) VALUES(?,?,?,?,?,?,?)`, "review-preserved", snapshot.SnapshotID, "instrument-api", "complete", "Preserve this review", `{"recommendation":"hold"}`, timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO portfolio_theses(id,portfolio_id,holding_id,instrument_id,summary,intended_holding_period,allocation_limit,reasons_to_hold,risks,reasons_to_sell,conditions_to_change,sources,current_revision,request_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "thesis-preserved", "portfolio-api", "lot-api", "instrument-api", "Preserve this thesis", "Long term", nil, `["history"]`, `[]`, `[]`, `[]`, `[]`, 1, strings.Repeat("a", 64), timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO thesis_revisions(id,thesis_id,portfolio_id,holding_id,instrument_id,revision,summary,intended_holding_period,allocation_limit,reasons_to_hold,risks,reasons_to_sell,conditions_to_change,sources,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "thesis-revision-preserved", "thesis-preserved", "portfolio-api", "lot-api", "instrument-api", 1, "Preserve this thesis", "Long term", nil, `["history"]`, `[]`, `[]`, `[]`, `[]`, strings.Repeat("b", 64), timestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, "audit-preserved", "fixture", "holding.note", "lot", "lot-api", `{ "note": "preserve" }`, timestamp, retainUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, "audit-preserved", "portfolio-api", retainUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO activity_events(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,sort_time,source_audit_event_id,retain_until) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, "activity-preserved", "portfolio-api", "administrative", "holding.note", "holding", "lot", "lot-api", "Preserved activity", "Preserved activity detail", timestamp, timestamp, "audit-preserved", retainUntil); err != nil {
		t.Fatal(err)
	}
	var snapshotPayload, reviewPayload, thesisSummary, auditPayload, activitySummary string
	for query, destination := range map[string]*string{
		`SELECT payload FROM snapshots WHERE id=?`:        &snapshotPayload,
		`SELECT payload FROM reviews WHERE id=?`:          &reviewPayload,
		`SELECT summary FROM portfolio_theses WHERE id=?`: &thesisSummary,
		`SELECT payload FROM audit_events WHERE id=?`:     &auditPayload,
		`SELECT summary FROM activity_events WHERE id=?`:  &activitySummary,
	} {
		args := []any{""}
		switch query {
		case `SELECT payload FROM snapshots WHERE id=?`:
			args[0] = snapshot.SnapshotID
		case `SELECT payload FROM reviews WHERE id=?`:
			args[0] = "review-preserved"
		case `SELECT summary FROM portfolio_theses WHERE id=?`:
			args[0] = "thesis-preserved"
		case `SELECT payload FROM audit_events WHERE id=?`:
			args[0] = "audit-preserved"
		default:
			args[0] = "activity-preserved"
		}
		if err := database.DB.QueryRow(query, args...).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}

	secret := "lost-response-machine-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: fixtureSessionSecret(), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour, Machine: auth.MachineConfig{PrincipalID: "lost-response-machine", PortfolioID: "portfolio-api", BearerSecret: secret, PortfolioRead: true, HoldingsWrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target, body, key, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	body := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"0.000000000000000001","unit_cost":"2.00","cost_basis":"0.000000000000000002","currency":"USD"}`
	firstCreate := request(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", body, "lost-create-key-000001", `"portfolio-api-0"`)
	if firstCreate.Code != http.StatusCreated {
		t.Fatalf("first create status=%d body=%s", firstCreate.Code, firstCreate.Body.String())
	}
	// Deliberately discard the first response as a lost browser response.
	createReplay := request(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", body, "lost-create-key-000001", `"portfolio-api-0"`)
	if createReplay.Code != http.StatusCreated || createReplay.Body.String() != firstCreate.Body.String() {
		t.Fatalf("create replay status=%d body=%s first=%s", createReplay.Code, createReplay.Body.String(), firstCreate.Body.String())
	}
	var created struct {
		Holding struct {
			HoldingID string `json:"holding_id"`
			Quantity  string `json:"quantity"`
			UnitCost  string `json:"unit_cost"`
			CostBasis string `json:"cost_basis"`
		} `json:"holding"`
		PortfolioRevision int `json:"portfolio_revision"`
	}
	if err := json.Unmarshal(firstCreate.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Holding.Quantity != "0.000000000000000001" || created.Holding.UnitCost != "2.00" || created.Holding.CostBasis != "0.000000000000000002" || created.PortfolioRevision != 1 {
		t.Fatalf("create receipt=%+v", created)
	}
	var lots, createAudits, createActivities, createReceipts int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`:                                                &lots,
		`SELECT COUNT(*) FROM audit_events WHERE action='holding_created'`:                              &createAudits,
		`SELECT COUNT(*) FROM activity_events WHERE action='holding_created'`:                           &createActivities,
		`SELECT COUNT(*) FROM holding_mutation_idempotency WHERE portfolio_id=? AND operation='create'`: &createReceipts,
	} {
		if strings.Contains(query, "portfolio_id=?") {
			if err := database.DB.QueryRow(query, "portfolio-api").Scan(destination); err != nil {
				t.Fatal(err)
			}
		} else if err := database.DB.QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if lots != 2 || createAudits != 1 || createActivities != 1 || createReceipts != 1 {
		t.Fatalf("create replay counts lots=%d audits=%d activities=%d receipts=%d", lots, createAudits, createActivities, createReceipts)
	}
	removeKey := "lost-remove-key-000001"
	firstRemove := request(http.MethodDelete, "/api/v1/portfolios/portfolio-api/holdings/"+created.Holding.HoldingID, "", removeKey, `"portfolio-api-1"`)
	if firstRemove.Code != http.StatusOK {
		t.Fatalf("first remove status=%d body=%s", firstRemove.Code, firstRemove.Body.String())
	}
	removeReplay := request(http.MethodDelete, "/api/v1/portfolios/portfolio-api/holdings/"+created.Holding.HoldingID, "", removeKey, `"portfolio-api-1"`)
	if removeReplay.Code != http.StatusOK || removeReplay.Body.String() != firstRemove.Body.String() {
		t.Fatalf("remove replay status=%d body=%s first=%s", removeReplay.Code, removeReplay.Body.String(), firstRemove.Body.String())
	}
	if err := database.DB.QueryRow(`SELECT lifecycle FROM lots WHERE id=?`, created.Holding.HoldingID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "removed" {
		t.Fatalf("replayed archive lifecycle=%q", body)
	}
	if err := database.DB.QueryRow(`SELECT lifecycle FROM lots WHERE id='lot-api'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "active" {
		t.Fatalf("same-symbol fixture lot lifecycle=%q", body)
	}
	var removeAudits, removeActivities, removeReceipts int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM audit_events WHERE action='holding_removed'`:                              &removeAudits,
		`SELECT COUNT(*) FROM activity_events WHERE action='holding_removed'`:                           &removeActivities,
		`SELECT COUNT(*) FROM holding_mutation_idempotency WHERE portfolio_id=? AND operation='remove'`: &removeReceipts,
	} {
		if strings.Contains(query, "portfolio_id=?") {
			if err := database.DB.QueryRow(query, "portfolio-api").Scan(destination); err != nil {
				t.Fatal(err)
			}
		} else if err := database.DB.QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if removeAudits != 1 || removeActivities != 1 || removeReceipts != 1 {
		t.Fatalf("remove replay counts audits=%d activities=%d receipts=%d", removeAudits, removeActivities, removeReceipts)
	}
	var gotSnapshotPayload, gotReviewPayload, gotThesisSummary, gotAuditPayload, gotActivitySummary string
	if err := database.DB.QueryRow(`SELECT payload FROM snapshots WHERE id=?`, snapshot.SnapshotID).Scan(&gotSnapshotPayload); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT payload FROM reviews WHERE id='review-preserved'`).Scan(&gotReviewPayload); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT summary FROM portfolio_theses WHERE id='thesis-preserved'`).Scan(&gotThesisSummary); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT payload FROM audit_events WHERE id='audit-preserved'`).Scan(&gotAuditPayload); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.QueryRow(`SELECT summary FROM activity_events WHERE id='activity-preserved'`).Scan(&gotActivitySummary); err != nil {
		t.Fatal(err)
	}
	if snapshotPayload == "" || reviewPayload == "" || thesisSummary == "" || auditPayload == "" || activitySummary == "" || gotSnapshotPayload != snapshotPayload || gotReviewPayload != reviewPayload || gotThesisSummary != thesisSummary || gotAuditPayload != auditPayload || gotActivitySummary != activitySummary {
		t.Fatal("historical snapshot, review, thesis, audit, or activity changed or was empty during replay")
	}
}

func TestBrowserHoldingMutationRequiresExactOriginAndSessionCSRF(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("browser-holding-session-secret-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("session body=%s", loginResponse.Body.String())
	}
	body := `{"instrument":{"instrument_id":"instrument-api"},"quantity":"1.00","unit_cost":"2.00","cost_basis":"2.00","currency":"USD"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", strings.NewReader(body))
	request.AddCookie(loginResponse.Result().Cookies()[0])
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Idempotency-Key", "browser-create-key-1")
	request.Header.Set("If-Match", `"portfolio-api-0"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Idempotency-Key", "browser-create-key-2")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHoldingMutationRejectsQueryBeforeAuthenticationOrBodyParsing(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("holding-query-session-secret-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings?unexpected=1", strings.NewReader("not-json"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var lots int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM lots WHERE portfolio_id=?`, "portfolio-api").Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if lots != 1 {
		t.Fatalf("query variant changed lots: %d", lots)
	}
}

func TestHoldingMutationUnauthorizedAlwaysEmitsBearerChallenge(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-only-value"), bcrypt.MinCost+6)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(database, auth.Config{
		Username: "fixture-user", PasswordHash: string(hash), SessionSecret: base64.StdEncoding.EncodeToString([]byte("holding-challenge-session-secret-diversity-123")), PublicOrigin: "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/portfolios/portfolio-api/holdings", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != auth.MachineBearerRealm {
		t.Fatalf("status=%d challenge=%q body=%s", response.Code, response.Header().Get("WWW-Authenticate"), response.Body.String())
	}
}

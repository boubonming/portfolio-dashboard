package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
)

func scheduleRawRequest(handler http.Handler, cookie *http.Cookie, csrf, method, target, body, contentType, origin, etag string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://example.test"+target, strings.NewReader(body))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set(auth.CSRFHeaderName, csrf)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func scheduleDurableCounts(t *testing.T, database *sql.DB) [3]int {
	t.Helper()
	var schedules, audits, activities int
	if err := database.QueryRow(`SELECT COUNT(*) FROM market_refresh_schedules`).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='market.refresh.schedule.updated'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM activity_events WHERE action='market.refresh.schedule.updated'`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	return [3]int{schedules, audits, activities}
}

func TestScheduleHTTPAuthenticationHeadersMethodsAndPathMatrix(t *testing.T) {
	runtime := &recordingScheduleRuntime{}
	handler, database, cookie, csrf := marketHTTPFixtureWithClientsAndRuntime(t, nil, runtime)
	defer database.Close()
	const schedulePath = "/api/v1/portfolios/portfolio-market-http/market-data/schedule"
	checkPrivateHeaders := func(t *testing.T, response *httptest.ResponseRecorder) {
		t.Helper()
		for name, want := range map[string]string{"Cache-Control": "no-store", "Vary": "Cookie", "X-Content-Type-Options": "nosniff"} {
			if got := response.Header().Get(name); got != want {
				t.Fatalf("%s=%q, want %q (status=%d body=%s)", name, got, want, response.Code, response.Body.String())
			}
		}
	}

	get := marketRequest(handler, cookie, csrf, http.MethodGet, schedulePath, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatalf("GET status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
	checkPrivateHeaders(t, get)
	var payload map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &payload); err != nil || payload["portfolio_id"] != "portfolio-market-http" || payload["etag"] != get.Header().Get("ETag") {
		t.Fatalf("GET payload=%s err=%v", get.Body.String(), err)
	}
	etag := get.Header().Get("ETag")
	put := scheduleSaveRequest(handler, cookie, csrf, etag, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore","reporting_currency":""}`)
	if put.Code != http.StatusOK || put.Header().Get("ETag") == etag {
		t.Fatalf("PUT status=%d old_etag=%q new_etag=%q body=%s", put.Code, etag, put.Header().Get("ETag"), put.Body.String())
	}
	checkPrivateHeaders(t, put)

	unauthenticated := httptest.NewRequest(http.MethodGet, "https://example.test"+schedulePath, nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}
	checkPrivateHeaders(t, unauthenticatedResponse)

	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		response := scheduleRawRequest(handler, cookie, csrf, method, schedulePath, "", "", "https://example.test", "")
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, PUT" {
			t.Fatalf("method=%s status=%d allow=%q body=%s", method, response.Code, response.Header().Get("Allow"), response.Body.String())
		}
		checkPrivateHeaders(t, response)
	}
	query := marketRequest(handler, cookie, csrf, http.MethodGet, schedulePath+"?raw=1", "")
	if query.Code != http.StatusBadRequest || !strings.Contains(query.Body.String(), "invalid_query") {
		t.Fatalf("raw query status=%d body=%s", query.Code, query.Body.String())
	}
	checkPrivateHeaders(t, query)
	for _, test := range []struct {
		target string
		status int
	}{
		{target: "/api/v1/portfolios/portfolio%2Dmarket%2Dhttp/market-data/schedule", status: http.StatusBadRequest},
		{target: "/api/v1/portfolios/portfolio-market-http/market-data/schedule/extra", status: http.StatusNotFound},
		{target: "/api/v1/portfolios/portfolio-market-http/market-data/schedule/../schedule", status: http.StatusBadRequest},
	} {
		response := marketRequest(handler, cookie, csrf, http.MethodGet, test.target, "")
		if response.Code != test.status {
			t.Fatalf("target=%q status=%d, want %d body=%s", test.target, response.Code, test.status, response.Body.String())
		}
		checkPrivateHeaders(t, response)
	}

	machineHandler, machineDatabase := marketMachineFixtureWithMachine(t, nil, auth.MachineConfig{
		PrincipalID: "schedule-machine", PortfolioID: "market-machine-portfolio", BearerSecret: marketMachineSecretValue(), PortfolioRead: true, MarketRefresh: true,
	}, false)
	defer machineDatabase.Close()
	machineRequest := machineMarketRequest(machineHandler, http.MethodGet, "/api/v1/portfolios/market-machine-portfolio/market-data/schedule", "", nil)
	if machineRequest.Code != http.StatusForbidden {
		t.Fatalf("machine schedule status=%d body=%s", machineRequest.Code, machineRequest.Body.String())
	}
	if got := scheduleDurableCounts(t, machineDatabase.DB); got != [3]int{} {
		t.Fatalf("machine denial changed schedule durable state: %v", got)
	}
}

func TestScheduleHTTPInvalidBodiesContentTypeAndFieldsAreAtomic(t *testing.T) {
	runtime := &recordingScheduleRuntime{}
	handler, database, cookie, csrf := marketHTTPFixtureWithClientsAndRuntime(t, nil, runtime)
	defer database.Close()
	const schedulePath = "/api/v1/portfolios/portfolio-market-http/market-data/schedule"
	etag := scheduleResponseETag(t, handler, cookie, csrf)
	beforeCounts := scheduleDurableCounts(t, database.DB)
	beforeCalls := runtime.calls
	invalidBodies := []string{"", `null`, `[]`, `{}`, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore","unknown":1}`, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"} {}`, `{"enabled":true,"enabled":false,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`}
	for _, body := range invalidBodies {
		response := scheduleSaveRequest(handler, cookie, csrf, etag, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%s", body, response.Code, response.Body.String())
		}
		if got := scheduleDurableCounts(t, database.DB); got != beforeCounts {
			t.Fatalf("body=%q changed durable counts: before=%v after=%v", body, beforeCounts, got)
		}
		if runtime.calls != beforeCalls {
			t.Fatalf("body=%q reached runtime: calls=%d before=%d", body, runtime.calls, beforeCalls)
		}
	}
	for _, contentType := range []string{"", "text/plain", "application/json-patch+json"} {
		response := scheduleRawRequest(handler, cookie, csrf, http.MethodPut, schedulePath, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`, contentType, "https://example.test", etag)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("content type=%q status=%d body=%s", contentType, response.Code, response.Body.String())
		}
	}
	for _, csrfValue := range []string{"", "wrong-csrf"} {
		response := scheduleRawRequest(handler, cookie, csrfValue, http.MethodPut, schedulePath, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`, "application/json", "https://example.test", etag)
		if response.Code != http.StatusForbidden {
			t.Fatalf("csrf=%q status=%d body=%s", csrfValue, response.Code, response.Body.String())
		}
	}
	wrongOrigin := scheduleRawRequest(handler, cookie, csrf, http.MethodPut, schedulePath, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`, "application/json", "https://evil.example", etag)
	if wrongOrigin.Code != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", wrongOrigin.Code, wrongOrigin.Body.String())
	}
	for _, test := range []struct {
		name   string
		update string
	}{
		{"disabled with cron", `{"enabled":false,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`},
		{"enabled without cron", `{"enabled":true,"cron":"","timezone":"Asia/Singapore"}`},
		{"whitespace cron", `{"enabled":true,"cron":" 0 6 * * *","timezone":"Asia/Singapore"}`},
		{"macro cron", `{"enabled":true,"cron":"@daily","timezone":"Asia/Singapore"}`},
		{"four cron fields", `{"enabled":true,"cron":"0 6 * *","timezone":"Asia/Singapore"}`},
		{"six cron fields", `{"enabled":true,"cron":"0 0 6 * * *","timezone":"Asia/Singapore"}`},
		{"invalid timezone", `{"enabled":true,"cron":"0 6 * * *","timezone":"Not/IANA"}`},
		{"lower currency", `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore","reporting_currency":"usd"}`},
		{"short currency", `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore","reporting_currency":"US"}`},
		{"long currency", `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore","reporting_currency":"USDD"}`},
	} {
		response := scheduleSaveRequest(handler, cookie, csrf, etag, test.update)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", test.name, response.Code, response.Body.String())
		}
		if got := scheduleDurableCounts(t, database.DB); got != beforeCounts {
			t.Fatalf("%s changed durable counts: before=%v after=%v", test.name, beforeCounts, got)
		}
		if runtime.calls != beforeCalls {
			t.Fatalf("%s reached runtime: calls=%d before=%d", test.name, runtime.calls, beforeCalls)
		}
	}
	for name, suppliedETag := range map[string]string{"missing If-Match": "", "stale If-Match": `"stale"`} {
		response := scheduleRawRequest(handler, cookie, csrf, http.MethodPut, schedulePath, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`, "application/json", "https://example.test", suppliedETag)
		if response.Code != http.StatusPreconditionFailed {
			t.Fatalf("%s status=%d body=%s", name, response.Code, response.Body.String())
		}
		if got := scheduleDurableCounts(t, database.DB); got != beforeCounts {
			t.Fatalf("%s changed durable counts: before=%v after=%v", name, beforeCounts, got)
		}
	}
	if got := scheduleResponseETag(t, handler, cookie, csrf); got != etag {
		t.Fatalf("invalid requests changed ETag from %q to %q", etag, got)
	}
}

func TestScheduleHTTPConcurrentPUTUsesSingleCASCommit(t *testing.T) {
	runtime := &recordingScheduleRuntime{}
	handler, database, cookie, csrf := marketHTTPFixtureWithClientsAndRuntime(t, nil, runtime)
	defer database.Close()
	etag := scheduleResponseETag(t, handler, cookie, csrf)
	const body = `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`
	responses := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			responses <- scheduleSaveRequest(handler, cookie, csrf, etag, body)
		}()
	}
	group.Wait()
	close(responses)
	statuses := map[int]int{}
	for response := range responses {
		statuses[response.Code]++
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusPreconditionFailed] != 1 {
		t.Fatalf("concurrent statuses=%v", statuses)
	}
	var revision int
	if err := database.DB.QueryRow(`SELECT revision FROM market_refresh_schedules WHERE portfolio_id=?`, "portfolio-market-http").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("revision=%d, want one committed update", revision)
	}
	if got := scheduleDurableCounts(t, database.DB); got != [3]int{1, 1, 1} {
		t.Fatalf("concurrent durable counts=%v", got)
	}
}

func TestScheduleHTTPCallerCancellationAndActivityFailureRollback(t *testing.T) {
	runtime := &recordingScheduleRuntime{}
	handler, database, cookie, csrf := marketHTTPFixtureWithClientsAndRuntime(t, nil, runtime)
	defer database.Close()
	const schedulePath = "/api/v1/portfolios/portfolio-market-http/market-data/schedule"
	etag := scheduleResponseETag(t, handler, cookie, csrf)
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(cancelledContext, http.MethodGet, "https://example.test"+schedulePath, nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://example.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("cancelled GET status=%d body=%s", response.Code, response.Body.String())
	}
	if got := scheduleDurableCounts(t, database.DB); got != [3]int{} {
		t.Fatalf("cancelled GET changed durable state: %v", got)
	}
	if _, err := database.DB.Exec(`CREATE TRIGGER fail_schedule_activity BEFORE INSERT ON activity_events WHEN NEW.action='market.refresh.schedule.updated' BEGIN SELECT RAISE(ABORT, 'fixture activity failure'); END`); err != nil {
		t.Fatal(err)
	}
	failed := scheduleSaveRequest(handler, cookie, csrf, etag, `{"enabled":true,"cron":"0 6 * * *","timezone":"Asia/Singapore"}`)
	if failed.Code != http.StatusInternalServerError || !strings.Contains(failed.Body.String(), `"error":"internal_error"`) {
		t.Fatalf("activity failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	if got := scheduleDurableCounts(t, database.DB); got != [3]int{} {
		t.Fatalf("activity failure changed durable state: %v", got)
	}
	if got := scheduleResponseETag(t, handler, cookie, csrf); got != etag {
		t.Fatalf("activity failure changed ETag from %q to %q", etag, got)
	}
}

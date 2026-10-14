package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestPortfolioExportCSVUsesOverviewDecimalsAndStableLotRows(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	when := time.Now().UTC()
	if _, err := database.CreateSnapshot(context.Background(), store.SnapshotRequest{PortfolioID: "portfolio-api", ReportingCurrency: "MYR", CalculationVersion: "api.test.v1", AsOf: when, MaxAge: 2 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	authenticatedAPIHandler(t, database).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	if got := response.Header().Get("Content-Disposition"); got != `attachment; filename="portfolio-export-MYR.csv"` {
		t.Fatalf("content disposition=%q", got)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers cache=%q nosniff=%q", response.Header().Get("Cache-Control"), response.Header().Get("X-Content-Type-Options"))
	}
	reader := csv.NewReader(strings.NewReader(response.Body.String()))
	headers, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	wantHeaders := []string{"portfolio_id", "portfolio_name", "reporting_currency", "snapshot_state", "snapshot_id", "calculation_version", "snapshot_as_of", "snapshot_created_at", "snapshot_complete", "lot_id", "symbol", "name", "description", "quantity", "source_currency", "unit_cost", "cost_basis", "latest_price", "latest_value", "reporting_value", "quote_source", "market_timestamp", "freshness_state", "freshness_age", "broker", "acquisition_date", "source_row", "missing_broker", "missing_acquisition_date", "data_quality_flags"}
	if strings.Join(headers, "\x00") != strings.Join(wantHeaders, "\x00") {
		t.Fatalf("headers=%v", headers)
	}
	row, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if row[0] != "portfolio-api" || row[2] != "MYR" || row[9] != "lot-api" || row[13] != "0.125" || row[16] != "0.15" || row[17] != "123.45" || row[18] != "15.43125" || row[19] != "69.440625" {
		t.Fatalf("row=%v", row)
	}
	if _, err := reader.Read(); err != io.EOF {
		t.Fatalf("unexpected extra row: %v", err)
	}
}

func TestPortfolioExportCSVRejectsAnonymousAndNonCanonicalQueries(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	handler := authenticatedAPIHandler(t, database)
	anonymousHandler, err := NewHandlerWithAuth(database, authConfigForExportTest(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		code int
	}{
		{"anonymous", "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", http.StatusUnauthorized},
		{"missing currency", "/api/v1/portfolios/portfolio-api/export.csv", http.StatusBadRequest},
		{"lowercase currency", "/api/v1/portfolios/portfolio-api/export.csv?currency=myr", http.StatusBadRequest},
		{"unsupported currency", "/api/v1/portfolios/portfolio-api/export.csv?currency=EUR", http.StatusBadRequest},
		{"duplicate currency", "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR&currency=USD", http.StatusBadRequest},
		{"unknown parameter", "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR&download=1", http.StatusBadRequest},
		{"malformed path", "/api/v1/portfolios/portfolio-api/export.csv/extra?currency=MYR", http.StatusNotFound},
		{"wrong method", "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.name == "wrong method" {
				request = httptest.NewRequest(http.MethodHead, test.path, nil)
			}
			requestHandler := handler
			if test.name == "anonymous" {
				requestHandler = anonymousHandler
			}
			response := httptest.NewRecorder()
			requestHandler.ServeHTTP(response, request)
			if response.Code != test.code || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
}

func authConfigForExportTest(t *testing.T) auth.Config {
	t.Helper()
	return auth.Config{
		Username: "fixture-user", PasswordHash: sessionPasswordHash(t),
		SessionSecret: base64.StdEncoding.EncodeToString([]byte("export-api-session-secret-with-diversity-123")),
		PublicOrigin:  "https://example.test", CookieSecure: true, SessionTTL: time.Hour,
	}
}

func TestPortfolioExportCSVEscapesFormulaTextAndKeepsCSVData(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "export.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{"portfolio-safe", "Portfolio", "MYR", now, now}},
		{`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, []any{"instrument-safe", "=SUM(A1)", "Name, \"quoted\"\n東京", "MYR", now}},
		{`INSERT INTO accounts(id,portfolio_id,broker,created_at) VALUES(?,?,?,?)`, []any{"account-safe", "portfolio-safe", "+broker", now}},
		{`INSERT INTO lots(id,portfolio_id,instrument_id,account_id,quantity,unit_cost,cost_basis,currency,source_row,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"lot-safe", "portfolio-safe", "instrument-safe", "account-safe", "0.000000000000000001", "1.2300", "0.000000000000000001", "MYR", 7, now}},
	} {
		if _, err := database.DB.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	authenticatedAPIHandler(t, database).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-safe/export.csv?currency=MYR", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	reader := csv.NewReader(strings.NewReader(response.Body.String()))
	if _, err := reader.Read(); err != nil {
		t.Fatal(err)
	}
	row, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if row[10] != "'=SUM(A1)" || row[12] != "Name, \"quoted\"\n東京" || row[24] != "'+broker" {
		t.Fatalf("escaped row=%q", row)
	}
	if row[13] != "0.000000000000000001" || row[16] != "0.000000000000000001" || row[25] != "" {
		t.Fatalf("exact fields row=%q", row)
	}
}

func TestPortfolioExportCSVZeroHoldingsIsHeaderOnly(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-empty", "Empty", "MYR", time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	authenticatedAPIHandler(t, database).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-empty/export.csv?currency=MYR", nil))
	reader := csv.NewReader(strings.NewReader(response.Body.String()))
	if _, err := reader.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(); err != io.EOF {
		t.Fatalf("expected header-only CSV, got %v", err)
	}
	if got := response.Body.String(); !strings.HasSuffix(got, "\r\n") || strings.Contains(strings.TrimSuffix(got, "\r\n"), "\n") {
		t.Fatalf("header-only CSV must use RFC 4180 CRLF records: %q", got)
	}
}

func TestPortfolioExportCSVRejectsEscapedPathAliasesAndUnsupportedMethodsBeforeCSRF(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	handler := authenticatedAPIHandler(t, database)
	for _, test := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "escaped portfolio id", method: http.MethodGet, path: "/api/v1/portfolios/%70ortfolio-api/export.csv?currency=MYR", want: http.StatusNotFound},
		{name: "post has route method error before csrf", method: http.MethodPost, path: "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.method == http.MethodPost && response.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("allow=%q body=%s", response.Header().Get("Allow"), response.Body.String())
			}
		})
	}
}

func TestPortfolioExportCSVContentDispositionParsesAsAttachment(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	response := httptest.NewRecorder()
	authenticatedAPIHandler(t, database).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", nil))
	mediaType, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	if err != nil || mediaType != "attachment" || params["filename"] != "portfolio-export-MYR.csv" {
		t.Fatalf("content disposition=%q media_type=%q params=%v err=%v", response.Header().Get("Content-Disposition"), mediaType, params, err)
	}
}

func TestExportTextEscapesFormulaPrefixesAndPreservesCanonicalValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "equals", value: "=SUM(A1)", want: "'=SUM(A1)"},
		{name: "plus", value: "+1", want: "'+1"},
		{name: "minus", value: "-1", want: "'-1"},
		{name: "at", value: "@cmd", want: "'@cmd"},
		{name: "tab", value: "	=SUM(A1)", want: "'	=SUM(A1)"},
		{name: "carriage return", value: "\r=SUM(A1)", want: "'\r=SUM(A1)"},
		{name: "line feed", value: "\n=SUM(A1)", want: "'\n=SUM(A1)"},
		{name: "unicode byte order mark", value: "\ufeff=SUM(A1)", want: "'\ufeff=SUM(A1)"},
		{name: "zero width space", value: "\u200b=SUM(A1)", want: "'\u200b=SUM(A1)"},
		{name: "ordinary text", value: "Alpha", want: "Alpha"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exportText(test.value); got != test.want {
				t.Fatalf("exportText(%q)=%q, want %q", test.value, got, test.want)
			}
		})
	}
	if got := exportTimestamp("2026-01-02T12:00:00.000000000Z"); got != "2026-01-02T12:00:00.000000000Z" {
		t.Fatalf("canonical timestamp changed: %q", got)
	}
	date := "2026-01-02"
	if got := optionalDate(&date); got != date {
		t.Fatalf("canonical date changed: %q", got)
	}
}

func TestPortfolioExportCSVKeepsThirtyThreeLotsAndDuplicateSymbols(t *testing.T) {
	database := apiFixture(t)
	defer database.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index := 1; index <= 30; index++ {
		instrumentID := fmt.Sprintf("instrument-export-%02d", index)
		if _, err := database.DB.Exec(`INSERT INTO instruments(id,symbol,description,currency,created_at) VALUES(?,?,?,?,?)`, instrumentID, fmt.Sprintf("SYM%02d", index), "Fixture", "MYR", now); err != nil {
			t.Fatal(err)
		}
		if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,source_row,created_at) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("lot-export-%02d", index), "portfolio-api", instrumentID, "1", "MYR", index+1, now); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= 2; index++ {
		if _, err := database.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,source_row,created_at) VALUES(?,?,?,?,?,?,?)`, fmt.Sprintf("lot-export-duplicate-%02d", index), "portfolio-api", fmt.Sprintf("instrument-export-%02d", index), "2", "MYR", index+31, now); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	authenticatedAPIHandler(t, database).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/export.csv?currency=MYR", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	reader := csv.NewReader(strings.NewReader(response.Body.String()))
	if _, err := reader.Read(); err != nil {
		t.Fatal(err)
	}
	rows := 0
	symbols := map[string]int{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		rows++
		symbols[row[10]]++
	}
	if rows != 33 || len(symbols) != 31 || symbols["SYM01"] != 2 || symbols["SYM02"] != 2 {
		t.Fatalf("rows=%d symbols=%d duplicates=%v", rows, len(symbols), symbols)
	}
}

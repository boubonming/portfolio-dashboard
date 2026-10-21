package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMachineBearerCanReadPortfolioOverviewWithStrongHoldingETag(t *testing.T) {
	handler, cleanup := machineFixtureHandler(t)
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", nil)
	request.Header.Set("Authorization", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("machine overview status=%d body=%s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("ETag"), `"portfolio-api-0"`; got != want {
		t.Fatalf("overview ETag=%q, want %q", got, want)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestMachineOverviewRejectsDuplicateAuthorizationAndSessionCredentials(t *testing.T) {
	handler, cleanup := machineFixtureHandler(t)
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/overview", nil)
	request.Header.Add("Authorization", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	request.Header.Add("Authorization", "Bearer machine-fixture-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ-abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status=%d body=%s", response.Code, response.Body.String())
	}
}

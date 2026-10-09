package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPortfolio(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		wantError  bool
		wantSymbol string
	}{
		{
			name:       "valid snapshot normalizes values",
			contents:   `{"as_of":" 2026-10-01 ","currency":" sgd ","holdings":[{"symbol":" NOVA ","name":" Nova Foods ","quantity":2,"average_cost":10,"current_price":12,"category":" Equities "}]}`,
			wantSymbol: "NOVA",
		},
		{name: "malformed JSON", contents: `{"as_of":"2026-10-01",`, wantError: true},
		{name: "unknown field", contents: `{"as_of":"2026-10-01","currency":"USD","holdings":[],"extra":true}`, wantError: true},
		{name: "invalid holding", contents: `{"as_of":"2026-10-01","currency":"USD","holdings":[{"symbol":"A","name":"A","quantity":-1,"average_cost":1,"current_price":1,"category":"Other"}]}`, wantError: true},
		{name: "trailing JSON value", contents: `{"as_of":"2026-10-01","currency":"USD","holdings":[]} {}`, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "portfolio.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := LoadPortfolio(path)
			if (err != nil) != test.wantError {
				t.Fatalf("LoadPortfolio() error = %v, wantError %v", err, test.wantError)
			}
			if !test.wantError {
				if snapshot.Currency != "SGD" || snapshot.Holdings[0].Symbol != test.wantSymbol {
					t.Fatalf("normalized snapshot = %#v", snapshot)
				}
			}
		})
	}
}

func TestHTTPRoutes(t *testing.T) {
	handler, err := NewHandler(PortfolioSnapshot{
		AsOf: "2026-10-01", Currency: "USD",
		Holdings: []Holding{{Symbol: "A", Name: "Asset A", Quantity: 2, AverageCost: 10, CurrentPrice: 12, Category: "Equities"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		bodyPart    string
		contentType string
	}{
		{name: "dashboard", method: http.MethodGet, path: "/", status: http.StatusOK, bodyPart: "Portfolio overview", contentType: "text/html"},
		{name: "portfolio API", method: http.MethodGet, path: "/api/portfolio", status: http.StatusOK, bodyPart: `"total_market_value":24`, contentType: "application/json"},
		{name: "health", method: http.MethodGet, path: "/healthz", status: http.StatusOK, bodyPart: "healthy", contentType: "text/plain"},
		{name: "missing route", method: http.MethodGet, path: "/not-found", status: http.StatusNotFound},
		{name: "method not allowed", method: http.MethodPost, path: "/healthz", status: http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, server.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			bodyBytes, _ := io.ReadAll(response.Body)
			body := string(bodyBytes)
			if test.bodyPart != "" && !strings.Contains(body, test.bodyPart) {
				t.Errorf("body does not contain %q: %s", test.bodyPart, body)
			}
			if test.contentType != "" && !strings.HasPrefix(response.Header.Get("Content-Type"), test.contentType) {
				t.Errorf("Content-Type = %q, want prefix %q", response.Header.Get("Content-Type"), test.contentType)
			}
		})
	}
}

func TestPortfolioAPIIsJSON(t *testing.T) {
	handler, err := NewHandler(PortfolioSnapshot{AsOf: "2026-10-01", Currency: "USD", Holdings: []Holding{}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/portfolio", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response PortfolioResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("API body is not JSON: %v", err)
	}
	if response.Currency != "USD" || response.Holdings == nil || response.Allocations == nil {
		t.Fatalf("decoded response = %#v", response)
	}
}

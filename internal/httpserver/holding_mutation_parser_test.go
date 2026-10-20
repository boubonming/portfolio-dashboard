package httpserver

import "testing"

func TestHoldingCreateParserUsesExactDecimalArithmetic(t *testing.T) {
	valid := []byte(`{"instrument":{"instrument_id":"instrument-test"},"quantity":"12.5","unit_cost":"10","cost_basis":"125.0000","currency":"USD"}`)
	if _, _, err := parseHoldingCreate(valid); err != nil {
		t.Fatalf("valid decimal rejected: %v", err)
	}
	invalid := []byte(`{"instrument":{"instrument_id":"instrument-test"},"quantity":"12.5","unit_cost":"10","cost_basis":"125.0001","currency":"USD"}`)
	if _, _, err := parseHoldingCreate(invalid); err == nil {
		t.Fatal("inexact product accepted")
	}
	for _, body := range []string{
		`{"instrument":{"instrument_id":"instrument-test"},"quantity":1,"unit_cost":"1","cost_basis":"1","currency":"USD"}`,
		`{"instrument":{"instrument_id":"instrument-test"},"quantity":"01","unit_cost":"1","cost_basis":"1","currency":"USD"}`,
		`{"instrument":{"instrument_id":"instrument-test"},"quantity":"1e1","unit_cost":"1","cost_basis":"10","currency":"USD"}`,
	} {
		if _, _, err := parseHoldingCreate([]byte(body)); err == nil {
			t.Fatalf("noncanonical decimal accepted: %s", body)
		}
	}
}

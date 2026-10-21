package httpserver

import "testing"

func TestParseHoldingCreateAcceptsExactProductBeyondMoneyScale(t *testing.T) {
	body := []byte(`{"instrument":{"symbol":"TINY","currency":"USD"},"quantity":"0.000000000000000001","unit_cost":"0.01","cost_basis":"0.00000000000000000001","currency":"USD"}`)
	parsed, _, err := parseHoldingCreate(body)
	if err != nil {
		t.Fatalf("tiny exact holding rejected: %v", err)
	}
	if parsed.costBasis != "0.00000000000000000001" {
		t.Fatalf("cost basis=%q", parsed.costBasis)
	}
}

func TestParseHoldingCreateAcceptsMaxInputProductAndRejectsOverlongCostBasis(t *testing.T) {
	body := []byte(`{"instrument":{"symbol":"MAX","currency":"USD"},"quantity":"999999999999999999999.999999999999999999","unit_cost":"2.000000000000000000","cost_basis":"1999999999999999999999.999999999999999998","currency":"USD"}`)
	if _, _, err := parseHoldingCreate(body); err != nil {
		t.Fatalf("maximum accepted product rejected: %v", err)
	}
	overlong := []byte(`{"instrument":{"symbol":"MAX","currency":"USD"},"quantity":"1.000000000000000001","unit_cost":"1.000000000000000001","cost_basis":"1.0000000000000000020000000000000000001","currency":"USD"}`)
	if _, _, err := parseHoldingCreate(overlong); err == nil {
		t.Fatal("cost basis beyond 36 fractional digits was accepted")
	}
}

func TestParseHoldingCreateAcceptsMaximum80DigitIntegerProduct(t *testing.T) {
	body := []byte(`{"instrument":{"symbol":"MAXINT","currency":"USD"},"quantity":"9999999999999999999999999999999999999999","unit_cost":"9999999999999999999999999999999999999999","cost_basis":"99999999999999999999999999999999999999980000000000000000000000000000000000000001","currency":"USD"}`)
	parsed, _, err := parseHoldingCreate(body)
	if err != nil {
		t.Fatalf("maximum 80-digit product rejected: %v", err)
	}
	if parsed.costBasis != "99999999999999999999999999999999999999980000000000000000000000000000000000000001" {
		t.Fatalf("cost basis=%q", parsed.costBasis)
	}
}

func TestParseHoldingCreateRejectsCostBasisBeyond80Characters(t *testing.T) {
	body := []byte(`{"instrument":{"symbol":"TOOLONG","currency":"USD"},"quantity":"1","unit_cost":"1","cost_basis":"99999999999999999999999999999999999999999999999999999999999999999999999999999999.1","currency":"USD"}`)
	if _, _, err := parseHoldingCreate(body); err == nil {
		t.Fatal("cost basis beyond 80 characters was accepted")
	}
}

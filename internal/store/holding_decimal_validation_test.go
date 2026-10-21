package store

import (
	"errors"
	"testing"
)

func TestValidateHoldingDecimalsPreservesExactProductBounds(t *testing.T) {
	if err := validateHoldingDecimals("0.000000000000000001", "0.01", "0.00000000000000000001"); err != nil {
		t.Fatalf("tiny exact product rejected: %v", err)
	}
	if err := validateHoldingDecimals("1.000000000000000001", "1.000000000000000001", "1.0000000000000000020000000000000000001"); !errors.Is(err, ErrInvalidHoldingMutation) {
		t.Fatalf("overlong cost basis error=%v", err)
	}
	if err := validateHoldingDecimals("1e1", "1", "10"); !errors.Is(err, ErrInvalidHoldingMutation) {
		t.Fatalf("exponent notation error=%v", err)
	}
}

func TestValidateHoldingDecimalsAcceptsMaximum80DigitIntegerProduct(t *testing.T) {
	if err := validateHoldingDecimals("9999999999999999999999999999999999999999", "9999999999999999999999999999999999999999", "99999999999999999999999999999999999999980000000000000000000000000000000000000001"); err != nil {
		t.Fatalf("maximum 80-digit product rejected: %v", err)
	}
}

func TestValidateHoldingDecimalsRejectsCostBasisBeyond80Characters(t *testing.T) {
	if err := validateHoldingDecimals("1", "1", "99999999999999999999999999999999999999999999999999999999999999999999999999999999.1"); !errors.Is(err, ErrInvalidHoldingMutation) {
		t.Fatalf("overlong cost basis error=%v", err)
	}
}

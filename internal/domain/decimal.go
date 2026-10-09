package domain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var decimalLiteral = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

// Decimal is an exact base-10 value backed by a rational number. It never
// converts through float64. Division is rounded explicitly by the caller.
type Decimal struct{ rat *big.Rat }

func ParseDecimal(value string) (Decimal, error) {
	value = strings.TrimSpace(value)
	if value == "" || !decimalLiteral.MatchString(value) {
		return Decimal{}, fmt.Errorf("invalid decimal %q", value)
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok {
		return Decimal{}, fmt.Errorf("invalid decimal %q", value)
	}
	return Decimal{rat: r}, nil
}

func MustDecimal(value string) Decimal {
	d, err := ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return d
}

func ZeroDecimal() Decimal { return MustDecimal("0") }

func (d Decimal) valid() bool { return d.rat != nil }
func (d Decimal) Rat() *big.Rat {
	if !d.valid() {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.rat)
}
func (d Decimal) Sign() int {
	if !d.valid() {
		return 0
	}
	return d.rat.Sign()
}
func (d Decimal) Add(other Decimal) Decimal {
	return Decimal{rat: new(big.Rat).Add(d.Rat(), other.Rat())}
}
func (d Decimal) Sub(other Decimal) Decimal {
	return Decimal{rat: new(big.Rat).Sub(d.Rat(), other.Rat())}
}
func (d Decimal) Mul(other Decimal) Decimal {
	return Decimal{rat: new(big.Rat).Mul(d.Rat(), other.Rat())}
}

// Div rounds half away from zero to scale decimal places.
func (d Decimal) Div(other Decimal, scale int) (Decimal, error) {
	if other.Sign() == 0 {
		return Decimal{}, fmt.Errorf("division by zero")
	}
	if scale < 0 || scale > 100 {
		return Decimal{}, fmt.Errorf("invalid decimal scale %d", scale)
	}
	r := new(big.Rat).Quo(d.Rat(), other.Rat())
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	n := new(big.Rat).Mul(r, new(big.Rat).SetInt(factor))
	q := roundRat(n)
	return Decimal{rat: new(big.Rat).SetFrac(q, factor)}, nil
}

func roundRat(r *big.Rat) *big.Int {
	n := new(big.Int).Set(r.Num())
	d := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	if rem.Sign() != 0 && new(big.Int).Abs(rem).Mul(new(big.Int).Abs(rem), big.NewInt(2)).Cmp(d) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q
}

// String returns a canonical non-exponent decimal, preserving exact finite
// decimal values and removing insignificant trailing zeroes.
func (d Decimal) String() string {
	if !d.valid() {
		return ""
	}
	if d.rat.Sign() == 0 {
		return "0"
	}
	negative := d.rat.Sign() < 0
	n := new(big.Int).Abs(d.rat.Num())
	den := new(big.Int).Set(d.rat.Denom())
	// A finite decimal has no prime factors other than 2 and 5. Values from
	// multiplication of finite decimals satisfy this; keep a bounded exact
	// fallback for any rational produced by a caller.
	for new(big.Int).Mod(den, big.NewInt(2)).Sign() == 0 {
		den.Div(den, big.NewInt(2))
	}
	for new(big.Int).Mod(den, big.NewInt(5)).Sign() == 0 {
		den.Div(den, big.NewInt(5))
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return d.rat.FloatString(18)
	}
	// Determine the decimal scale and scale the numerator to that precision.
	original := d.rat.Denom()
	scale := 0
	pow := big.NewInt(1)
	for new(big.Int).Mod(pow, original).Sign() != 0 {
		pow.Mul(pow, big.NewInt(10))
		scale++
	}
	n.Mul(n, new(big.Int).Quo(pow, original))
	digits := n.String()
	if scale == 0 {
		if negative {
			return "-" + digits
		}
		return digits
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	point := len(digits) - scale
	out := digits[:point] + "." + strings.TrimRight(digits[point:], "0")
	out = strings.TrimRight(out, ".")
	if negative {
		return "-" + out
	}
	return out
}

func (d Decimal) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }
func (d *Decimal) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decimal must be a JSON string: %w", err)
	}
	parsed, err := ParseDecimal(value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

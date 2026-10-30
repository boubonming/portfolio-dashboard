package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MachineBearerRealm = `Bearer realm="portfolio-dashboard"`
	MachineSecretMin   = 32
	MachineSecretMax   = 512
)

var ErrInvalidMachineConfig = errors.New("invalid machine authentication configuration")
var machineID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)

// MachineConfig describes the one machine principal. BearerSecret is kept in
// process memory only; it is deliberately not serializable or exposed by any
// HTTP response.
type MachineConfig struct {
	PrincipalID   string `json:"principal_id"`
	PortfolioID   string `json:"portfolio_id"`
	BearerSecret  string `json:"-"`
	PortfolioRead bool   `json:"portfolio_read"`
	ReviewsWrite  bool   `json:"reviews_write"`
	HoldingsWrite bool   `json:"holdings_write"`
}

// MachinePrincipal is the non-secret database identity and effective scope.
// It is the only machine configuration shape handed to persistence or HTTP
// authorization code.
type MachinePrincipal struct {
	PrincipalID   string
	PortfolioID   string
	PortfolioRead bool
	ReviewsWrite  bool
	HoldingsWrite bool
}

// Validate permits the machine credential to be disabled by leaving every
// field empty, but rejects every partial or weak configuration.
func (c MachineConfig) Validate() error {
	empty := c.PrincipalID == "" && c.PortfolioID == "" && c.BearerSecret == ""
	if empty && !c.PortfolioRead && !c.ReviewsWrite && !c.HoldingsWrite {
		return nil
	}
	if c.PrincipalID == "" || c.PortfolioID == "" || c.BearerSecret == "" {
		return fmt.Errorf("%w: principal, portfolio, and bearer secret must be configured together", ErrInvalidMachineConfig)
	}
	for name, value := range map[string]string{"principal id": c.PrincipalID, "portfolio id": c.PortfolioID} {
		if len(value) < 1 || len(value) > 128 || !machineID.MatchString(value) || strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: invalid %s", ErrInvalidMachineConfig, name)
		}
	}
	secret := []byte(c.BearerSecret)
	if len(secret) < MachineSecretMin || len(secret) > MachineSecretMax {
		return fmt.Errorf("%w: bearer secret must contain 32 to 512 bytes", ErrInvalidMachineConfig)
	}
	seen := map[byte]struct{}{}
	for _, b := range secret {
		if b < 0x21 || b > 0x7e {
			return fmt.Errorf("%w: bearer secret must contain printable ASCII only", ErrInvalidMachineConfig)
		}
		seen[b] = struct{}{}
	}
	if len(seen) < 16 {
		return fmt.Errorf("%w: bearer secret has insufficient character diversity", ErrInvalidMachineConfig)
	}
	return nil
}

func (c MachineConfig) Enabled() bool { return c.BearerSecret != "" }

func (c MachineConfig) Principal() MachinePrincipal {
	return MachinePrincipal{PrincipalID: c.PrincipalID, PortfolioID: c.PortfolioID, PortfolioRead: c.PortfolioRead, ReviewsWrite: c.ReviewsWrite, HoldingsWrite: c.HoldingsWrite}
}

const (
	machineDiagnosticFieldMax  = 128
	machineDiagnosticOutputMax = 512
)

// String deliberately omits the bearer value. MachineConfig is passed through
// startup and test diagnostics, where a default fmt rendering must not become
// a credential disclosure path.
func (c MachineConfig) String() string { return c.redactedString() }

// GoString keeps Go-syntax diagnostics from bypassing String and exposing the
// in-memory credential with %#v.
func (c MachineConfig) GoString() string { return c.redactedString() }

// Format prevents fmt verbs other than %s from falling back to reflection,
// which would include BearerSecret. Formatting is intentionally based only on
// the bounded redacted representation; unknown verbs never receive raw data.
func (c MachineConfig) Format(state fmt.State, verb rune) {
	value := c.redactedString()
	switch verb {
	case 'q':
		value = strconv.Quote(value)
	case 'x':
		value = hex.EncodeToString([]byte(value))
	case 'X':
		value = strings.ToUpper(hex.EncodeToString([]byte(value)))
	}
	if precision, ok := state.Precision(); ok {
		value = truncateRunes(value, precision)
	}
	value = truncateBytes(value, machineDiagnosticOutputMax)

	width := utf8.RuneCountInString(value)
	if requested, ok := state.Width(); ok {
		width = requested
		if width > machineDiagnosticOutputMax {
			width = machineDiagnosticOutputMax
		}
	}
	padding := width - utf8.RuneCountInString(value)
	if padding < 0 {
		padding = 0
	}
	if state.Flag('-') {
		value += strings.Repeat(" ", padding)
	} else {
		value = strings.Repeat(" ", padding) + value
	}
	_, _ = state.Write([]byte(truncateBytes(value, machineDiagnosticOutputMax)))
}

func (c MachineConfig) redactedString() string {
	value := "MachineConfig{principal_id:" + strconv.Quote(truncateRunes(c.PrincipalID, machineDiagnosticFieldMax)) +
		" portfolio_id:" + strconv.Quote(truncateRunes(c.PortfolioID, machineDiagnosticFieldMax)) +
		" bearer_secret:<redacted> portfolio_read:" + strconv.FormatBool(c.PortfolioRead) +
		" reviews_write:" + strconv.FormatBool(c.ReviewsWrite) +
		" holdings_write:" + strconv.FormatBool(c.HoldingsWrite) + "}"
	return truncateBytes(value, machineDiagnosticOutputMax)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range value {
		if count == limit {
			return value[:index]
		}
		count++
	}
	return value
}

func truncateBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func validMachineSecret(secret string) bool {
	b := []byte(secret)
	if len(b) < MachineSecretMin || len(b) > MachineSecretMax {
		return false
	}
	for _, value := range b {
		if value < 0x21 || value > 0x7e {
			return false
		}
	}
	return true
}

// MachineTokenValid compares fixed-size digests so the comparison itself does
// not disclose the configured secret length.
func (c MachineConfig) MachineTokenValid(token string) bool {
	if !c.Enabled() || !validMachineSecret(c.BearerSecret) || !validMachineSecret(token) {
		return false
	}
	configured := sha256.Sum256([]byte(c.BearerSecret))
	provided := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(configured[:], provided[:]) == 1
}

func BearerToken(r *http.Request) (token string, present bool, wellFormed bool) {
	header := r.Header.Values("Authorization")
	present = len(header) > 0
	if !present {
		return "", false, false
	}
	if len(header) != 1 {
		return "", true, false
	}
	value := header[0]
	if !strings.HasPrefix(value, "Bearer ") || !validMachineSecret(value[7:]) {
		return "", true, false
	}
	return value[7:], true, true
}

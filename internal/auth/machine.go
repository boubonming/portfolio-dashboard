package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
}

// MachinePrincipal is the non-secret database identity and effective scope.
// It is the only machine configuration shape handed to persistence or HTTP
// authorization code.
type MachinePrincipal struct {
	PrincipalID   string
	PortfolioID   string
	PortfolioRead bool
}

// Validate permits the machine credential to be disabled by leaving every
// field empty, but rejects every partial or weak configuration.
func (c MachineConfig) Validate() error {
	empty := c.PrincipalID == "" && c.PortfolioID == "" && c.BearerSecret == ""
	if empty && !c.PortfolioRead {
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
	return MachinePrincipal{PrincipalID: c.PrincipalID, PortfolioID: c.PortfolioID, PortfolioRead: c.PortfolioRead}
}

// String deliberately omits the bearer value. MachineConfig is passed through
// startup and test diagnostics, where a default fmt rendering must not become
// a credential disclosure path.
func (c MachineConfig) String() string {
	return fmt.Sprintf("MachineConfig{principal_id:%q portfolio_id:%q bearer_secret:<redacted> portfolio_read:%t}", c.PrincipalID, c.PortfolioID, c.PortfolioRead)
}

// MachineTokenValid compares fixed-size digests so the comparison itself does
// not disclose the configured secret length.
func (c MachineConfig) MachineTokenValid(token string) bool {
	configured := sha256.Sum256([]byte(c.BearerSecret))
	provided := sha256.Sum256([]byte(token))
	return c.Enabled() && subtle.ConstantTimeCompare(configured[:], provided[:]) == 1
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
	if !strings.HasPrefix(value, "Bearer ") || strings.TrimSpace(value[7:]) != value[7:] || value[7:] == "" || strings.ContainsAny(value[7:], " \t\r\n") {
		return "", true, false
	}
	return value[7:], true, true
}

package config

import "testing"

func TestLoadMachinePermissionPresenceMatrixFailsClosed(t *testing.T) {
	secret := "machine-secret-with-enough-diversity-0123456789-ABCDEF"
	cases := []struct {
		name         string
		principal    string
		portfolio    string
		secret       string
		readGrant    string
		writeGrant   string
		holdingGrant string
		marketGrant  string
		wantError    bool
	}{
		{name: "disabled", wantError: false},
		{name: "portfolio read only", principal: "machine", portfolio: "portfolio", secret: secret, readGrant: "true", wantError: false},
		{name: "full review grants", principal: "machine", portfolio: "portfolio", secret: secret, readGrant: "true", writeGrant: "true", wantError: false},
		{name: "holding grant without read", principal: "machine", portfolio: "portfolio", secret: secret, holdingGrant: "true", wantError: true},
		{name: "holding grant with read", principal: "machine", portfolio: "portfolio", secret: secret, readGrant: "true", holdingGrant: "true", wantError: false},
		{name: "market refresh with read", principal: "machine", portfolio: "portfolio", secret: secret, readGrant: "true", marketGrant: "true", wantError: false},
		{name: "market refresh without read", principal: "machine", portfolio: "portfolio", secret: secret, marketGrant: "true", wantError: true},
		{name: "write only", principal: "machine", portfolio: "portfolio", secret: secret, writeGrant: "true", wantError: true},
		{name: "read grant without identity", readGrant: "true", wantError: true},
		{name: "write grant without identity", writeGrant: "true", wantError: true},
		{name: "identity without read grant", principal: "machine", portfolio: "portfolio", secret: secret, wantError: true},
		{name: "identity with empty secret", principal: "machine", portfolio: "portfolio", readGrant: "true", writeGrant: "true", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", tc.principal)
			t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", tc.portfolio)
			t.Setenv("PORTFOLIO_MACHINE_BEARER_SECRET", tc.secret)
			t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", tc.readGrant)
			t.Setenv("PORTFOLIO_MACHINE_REVIEWS_WRITE", tc.writeGrant)
			t.Setenv("PORTFOLIO_MACHINE_HOLDINGS_WRITE", tc.holdingGrant)
			t.Setenv("PORTFOLIO_MACHINE_MARKET_REFRESH", tc.marketGrant)
			_, err := Load()
			if (err != nil) != tc.wantError {
				t.Fatalf("Load error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}

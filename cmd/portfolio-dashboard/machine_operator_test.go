package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestMachineHoldingCLIUsesExplicitGrantAndRevokeLifecycle(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "operator.sqlite")
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, "cli-machine", "portfolio-default", 1, 0, 0, 1, "2026-01-01T00:00:00.000000000Z"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTFOLIO_MACHINE_PRINCIPAL_ID", "cli-machine")
	t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_ID", "portfolio-default")
	t.Setenv("PORTFOLIO_MACHINE_PORTFOLIO_READ", "true")
	t.Setenv("PORTFOLIO_MACHINE_HOLDINGS_WRITE", "true")

	args := []string{"-db", databasePath, "-principal", "cli-machine", "-portfolio", "portfolio-default", "-confirm", "GRANT HOLDINGS"}
	if err := runCommand("machine-grant-holdings", args); err != nil {
		t.Fatalf("grant command: %v", err)
	}
	if err := runCommand("machine-grant-holdings", args); err != nil {
		t.Fatalf("idempotent grant command: %v", err)
	}
	inspectArgs := []string{"-db", databasePath, "-principal", "cli-machine", "-portfolio", "portfolio-default"}
	if err := runCommand("machine-inspect", inspectArgs); err != nil {
		t.Fatalf("inspect command: %v", err)
	}
	if err := runCommand("machine-revoke-holdings", []string{"-db", databasePath, "-principal", "cli-machine", "-portfolio", "portfolio-default", "-confirm", "REVOKE HOLDINGS"}); err != nil {
		t.Fatalf("revoke command: %v", err)
	}
	database, err = store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var enabled int
	if err := database.DB.QueryRow(`SELECT permission_holdings_write FROM machine_principals WHERE id='cli-machine'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("CLI revocation did not persist: %d", enabled)
	}
}

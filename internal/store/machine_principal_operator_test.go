package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMachineHoldingGrantIsExplicitAuditedAndRevocationSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-grant.sqlite")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	foreignKeyErrors := 0
	for rows.Next() {
		foreignKeyErrors++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if foreignKeyErrors != 0 {
		t.Fatalf("foreign_key_check returned %d rows", foreignKeyErrors)
	}
	principal := "operator-machine"
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, principal, "portfolio-default", 1, 0, 0, 1, "2026-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	changed, err := database.GrantMachineHoldings(context.Background(), principal, "portfolio-default")
	if err != nil || !changed {
		t.Fatalf("grant changed=%v err=%v", changed, err)
	}
	changed, err = database.GrantMachineHoldings(context.Background(), principal, "portfolio-default")
	if err != nil || changed {
		t.Fatalf("repeat grant changed=%v err=%v", changed, err)
	}
	var action, payload string
	if err := database.DB.QueryRow(`SELECT action,payload FROM audit_events WHERE action='machine_holdings_grant' ORDER BY created_at DESC LIMIT 1`).Scan(&action, &payload); err != nil {
		t.Fatal(err)
	}
	if action != "machine_holdings_grant" || payload == "" {
		t.Fatalf("grant audit=%q %q", action, payload)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if revoked, err := database.RevokeMachineHoldings(context.Background(), principal, "portfolio-default"); err != nil || !revoked {
		t.Fatalf("revoke changed=%v err=%v", revoked, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var write int
	if err := database.DB.QueryRow(`SELECT permission_holdings_write FROM machine_principals WHERE id=?`, principal).Scan(&write); err != nil {
		t.Fatal(err)
	}
	if write != 0 {
		t.Fatalf("revoked write grant after reopen=%d", write)
	}
}

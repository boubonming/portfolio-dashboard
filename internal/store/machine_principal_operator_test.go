package store

import (
	"context"
	"encoding/json"
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

func TestMachineHoldingAuditPayloadTruthfullyRecordsAllTransitionsAndPortfolio(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "machine-audit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	principal := "audit-machine"
	portfolio := "portfolio-default"
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, principal, portfolio, 1, 0, 0, 1, "2026-01-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		name    string
		enabled bool
		result  string
	}{
		{name: "grant", enabled: true, result: "granted"},
		{name: "already grant", enabled: true, result: "already_granted"},
		{name: "revoke", enabled: false, result: "revoked"},
		{name: "already revoke", enabled: false, result: "already_revoked"},
	}
	for _, transition := range transitions {
		var changed bool
		if transition.enabled {
			changed, err = database.GrantMachineHoldings(context.Background(), principal, portfolio)
		} else {
			changed, err = database.RevokeMachineHoldings(context.Background(), principal, portfolio)
		}
		if err != nil {
			t.Fatalf("%s: %v", transition.name, err)
		}
		wantChanged := transition.result == "granted" || transition.result == "revoked"
		if changed != wantChanged {
			t.Fatalf("%s changed=%v, want %v", transition.name, changed, wantChanged)
		}
		var action, payload, auditPortfolio string
		if err := database.DB.QueryRow(`SELECT ae.action,ae.payload,aep.portfolio_id FROM audit_events ae JOIN audit_event_portfolios aep ON aep.audit_event_id=ae.id WHERE ae.entity_id=? ORDER BY ae.created_at DESC, ae.id DESC LIMIT 1`, principal).Scan(&action, &payload, &auditPortfolio); err != nil {
			t.Fatalf("%s audit lookup: %v", transition.name, err)
		}
		var parsed struct {
			PrincipalID   string `json:"principal_id"`
			PortfolioID   string `json:"portfolio_id"`
			HoldingsWrite bool   `json:"holdings_write"`
			Result        string `json:"result"`
		}
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			t.Fatalf("%s payload=%q: %v", transition.name, payload, err)
		}
		if parsed.PrincipalID != principal || parsed.PortfolioID != portfolio || parsed.HoldingsWrite != transition.enabled || parsed.Result != transition.result {
			t.Fatalf("%s payload=%+v", transition.name, parsed)
		}
		wantAction := "machine_holdings_grant"
		if !transition.enabled {
			wantAction = "machine_holdings_revoke"
		}
		if action != wantAction || auditPortfolio != portfolio {
			t.Fatalf("%s action=%q portfolio=%q", transition.name, action, auditPortfolio)
		}
	}
}

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

func TestMachineHoldingOperatorAuditsAndOwnersAreImmutable(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "machine-audit-immutable.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const now = "2026-01-02T12:00:00.000000000Z"
	principal := "immutable-machine"
	if _, err := database.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "operator-other", "Other", "USD", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, principal, "portfolio-default", 1, 0, 0, 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GrantMachineHoldings(context.Background(), principal, "portfolio-default"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RevokeMachineHoldings(context.Background(), principal, "portfolio-default"); err != nil {
		t.Fatal(err)
	}

	otherAuditID := "unrelated-audit"
	if _, err := database.DB.Exec(`INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, otherAuditID, "operator", "unrelated_operator_event", "machine_principal", principal, `{}`, now, "2033-01-02T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"machine_holdings_grant", "machine_holdings_revoke"} {
		var auditID, actor, entityType, entityID, payload, createdAt, retainUntil string
		if err := database.DB.QueryRow(`SELECT id,actor,entity_type,entity_id,payload,created_at,retain_until FROM audit_events WHERE action=? ORDER BY id LIMIT 1`, action).Scan(&auditID, &actor, &entityType, &entityID, &payload, &createdAt, &retainUntil); err != nil {
			t.Fatalf("%s audit: %v", action, err)
		}
		mutations := []struct {
			name  string
			query string
			args  []any
		}{
			{name: "actor", query: `UPDATE audit_events SET actor=? WHERE id=?`, args: []any{"different-actor", auditID}},
			{name: "action", query: `UPDATE audit_events SET action=? WHERE id=?`, args: []any{"unrelated_operator_event", auditID}},
			{name: "entity type", query: `UPDATE audit_events SET entity_type=? WHERE id=?`, args: []any{"other_entity", auditID}},
			{name: "entity id", query: `UPDATE audit_events SET entity_id=? WHERE id=?`, args: []any{"other-principal", auditID}},
			{name: "payload", query: `UPDATE audit_events SET payload=? WHERE id=?`, args: []any{`{"changed":true}`, auditID}},
			{name: "created timestamp", query: `UPDATE audit_events SET created_at=? WHERE id=?`, args: []any{"2027-01-02T12:00:00.000000000Z", auditID}},
			{name: "retention timestamp", query: `UPDATE audit_events SET retain_until=? WHERE id=?`, args: []any{"2034-01-02T12:00:00.000000000Z", auditID}},
			{name: "delete", query: `DELETE FROM audit_events WHERE id=?`, args: []any{auditID}},
			{name: "replace", query: `INSERT OR REPLACE INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at,retain_until) VALUES(?,?,?,?,?,?,?,?)`, args: []any{auditID, actor, action, entityType, entityID, payload, createdAt, retainUntil}},
		}
		for _, mutation := range mutations {
			t.Run(action+"/"+mutation.name, func(t *testing.T) {
				if _, err := database.DB.Exec(mutation.query, mutation.args...); err == nil {
					t.Fatal("immutable audit mutation unexpectedly succeeded")
				}
				var gotActor, gotAction, gotEntityType, gotEntityID, gotPayload, gotCreatedAt, gotRetainUntil string
				if err := database.DB.QueryRow(`SELECT actor,action,entity_type,entity_id,payload,created_at,retain_until FROM audit_events WHERE id=?`, auditID).Scan(&gotActor, &gotAction, &gotEntityType, &gotEntityID, &gotPayload, &gotCreatedAt, &gotRetainUntil); err != nil {
					t.Fatal(err)
				}
				if gotActor != actor || gotAction != action || gotEntityType != entityType || gotEntityID != entityID || gotPayload != payload || gotCreatedAt != createdAt || gotRetainUntil != retainUntil {
					t.Fatalf("audit changed to actor=%q action=%q entity_type=%q entity_id=%q payload=%q created_at=%q retain_until=%q", gotActor, gotAction, gotEntityType, gotEntityID, gotPayload, gotCreatedAt, gotRetainUntil)
				}
			})
		}

		var portfolioID, ownerRetention string
		if err := database.DB.QueryRow(`SELECT portfolio_id,retain_until FROM audit_event_portfolios WHERE audit_event_id=?`, auditID).Scan(&portfolioID, &ownerRetention); err != nil {
			t.Fatalf("%s owner: %v", action, err)
		}
		ownerMutations := []struct {
			name  string
			query string
			args  []any
		}{
			{name: "portfolio", query: `UPDATE audit_event_portfolios SET portfolio_id=? WHERE audit_event_id=?`, args: []any{"operator-other", auditID}},
			{name: "event id", query: `UPDATE audit_event_portfolios SET audit_event_id=? WHERE audit_event_id=?`, args: []any{otherAuditID, auditID}},
			{name: "retention timestamp", query: `UPDATE audit_event_portfolios SET retain_until=? WHERE audit_event_id=?`, args: []any{"2034-01-02T12:00:00.000000000Z", auditID}},
			{name: "delete", query: `DELETE FROM audit_event_portfolios WHERE audit_event_id=?`, args: []any{auditID}},
			{name: "replace", query: `INSERT OR REPLACE INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) VALUES(?,?,?)`, args: []any{auditID, portfolioID, ownerRetention}},
		}
		for _, mutation := range ownerMutations {
			t.Run(action+" owner/"+mutation.name, func(t *testing.T) {
				if _, err := database.DB.Exec(mutation.query, mutation.args...); err == nil {
					t.Fatal("immutable audit owner mutation unexpectedly succeeded")
				}
				var gotPortfolio, gotRetention string
				if err := database.DB.QueryRow(`SELECT portfolio_id,retain_until FROM audit_event_portfolios WHERE audit_event_id=?`, auditID).Scan(&gotPortfolio, &gotRetention); err != nil {
					t.Fatal(err)
				}
				if gotPortfolio != portfolioID || gotRetention != ownerRetention {
					t.Fatalf("audit owner changed to portfolio=%q retain_until=%q", gotPortfolio, gotRetention)
				}
			})
		}
	}

	if _, err := database.DB.Exec(`UPDATE audit_events SET actor=? WHERE id=?`, "changed", otherAuditID); err != nil {
		t.Fatalf("unrelated audit update was blocked: %v", err)
	}
	if _, err := database.DB.Exec(`DELETE FROM audit_events WHERE id=?`, otherAuditID); err != nil {
		t.Fatalf("unrelated audit delete was blocked: %v", err)
	}
}

func TestMachineHoldingOperatorAuditFailuresRollBackPermissionAndOwnership(t *testing.T) {
	for _, failure := range []struct {
		name  string
		setup string
	}{
		{name: "audit insert", setup: `CREATE TRIGGER test_machine_audit_insert_failure BEFORE INSERT ON audit_events WHEN NEW.action IN ('machine_holdings_grant','machine_holdings_revoke') BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`},
		{name: "owner insert", setup: `CREATE TRIGGER test_machine_owner_insert_failure BEFORE INSERT ON audit_event_portfolios WHEN EXISTS (SELECT 1 FROM audit_events WHERE id=NEW.audit_event_id AND action IN ('machine_holdings_grant','machine_holdings_revoke')) BEGIN SELECT RAISE(ABORT,'injected owner failure'); END`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			database, err := Open(context.Background(), filepath.Join(t.TempDir(), "machine-audit-failure.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			principal := "failure-machine"
			if _, err := database.DB.Exec(`INSERT INTO machine_principals(id,portfolio_id,permission_portfolio_read,permission_reviews_write,permission_holdings_write,active,created_at) VALUES(?,?,?,?,?,?,?)`, principal, "portfolio-default", 1, 0, 0, 1, "2026-01-02T12:00:00.000000000Z"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.Exec(failure.setup); err != nil {
				t.Fatal(err)
			}
			if _, err := database.GrantMachineHoldings(context.Background(), principal, "portfolio-default"); err == nil {
				t.Fatal("injected operator audit failure unexpectedly succeeded")
			}
			var permission, audits, owners int
			if err := database.DB.QueryRow(`SELECT permission_holdings_write FROM machine_principals WHERE id=?`, principal).Scan(&permission); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='machine_holdings_grant'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if err := database.DB.QueryRow(`SELECT COUNT(*) FROM audit_event_portfolios`).Scan(&owners); err != nil {
				t.Fatal(err)
			}
			if permission != 0 || audits != 0 || owners != 0 {
				t.Fatalf("failed grant left permission=%d audits=%d owners=%d", permission, audits, owners)
			}
		})
	}
}

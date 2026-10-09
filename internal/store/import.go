package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/importer"
)

const defaultPortfolioID = "portfolio-default"

// Apply persists a validated preview atomically. The caller must verify the
// source hash and approval before invoking this method.
func (s *Store) Apply(ctx context.Context, preview domain.Preview, sourcePath, expectedSHA256, reportingCurrency string) (domain.ImportResult, error) {
	if err := importer.ValidateApproved(preview); err != nil {
		return domain.ImportResult{}, err
	}
	if preview.SourceSHA256 == "" || preview.SourceSHA256 != expectedSHA256 {
		return domain.ImportResult{}, fmt.Errorf("source hash mismatch: got %s, expected %s", preview.SourceSHA256, expectedSHA256)
	}
	if reportingCurrency == "" {
		reportingCurrency = "MYR"
	}

	var result domain.ImportResult
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin import transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var priorHash string
	var priorStatus string
	err = tx.QueryRowContext(ctx, "SELECT source_sha256, status FROM import_runs WHERE source_sha256 = ?", expectedSHA256).Scan(&priorHash, &priorStatus)
	if err == nil {
		if priorStatus == "applied" {
			return domain.ImportResult{Status: "already_applied", AlreadyApplied: true, SourceSHA256: priorHash}, nil
		}
		return result, fmt.Errorf("source hash has an existing non-applied import run")
	}
	if err != sql.ErrNoRows {
		return result, fmt.Errorf("check prior import: %w", err)
	}
	var otherApplied int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM import_runs WHERE status = 'applied' AND source_sha256 <> ?", expectedSHA256).Scan(&otherApplied); err != nil {
		return result, fmt.Errorf("check existing imports: %w", err)
	}
	if otherApplied != 0 {
		return result, fmt.Errorf("a different source hash is already applied; preview and explicitly approve a replacement workflow first")
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO portfolios(id, name, reporting_currency, created_at, updated_at)
		VALUES (?, 'Personal Portfolio', ?, ?, ?) ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at`,
		defaultPortfolioID, reportingCurrency, now, now); err != nil {
		return result, fmt.Errorf("persist portfolio: %w", err)
	}
	runID := "import-" + expectedSHA256
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_runs(id, portfolio_id, source_path, source_sha256, status, created_at)
		VALUES (?, ?, ?, ?, 'applied', ?)`, runID, defaultPortfolioID, sourcePath, expectedSHA256, now); err != nil {
		return result, fmt.Errorf("persist import run: %w", err)
	}

	items := 0
	lots := 0
	for _, lot := range preview.Lots {
		payload, err := json.Marshal(lot)
		if err != nil {
			return result, fmt.Errorf("marshal source row %d: %w", lot.SourceRow, err)
		}
		classification := lot.Classification
		issue := strings.Join(lot.Issues, "; ")
		itemID := deterministicID("item", expectedSHA256, strconv.Itoa(lot.SourceRow), classification)
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_items(id, import_run_id, source_row, symbol, classification, payload, issue, source_sha256)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, itemID, runID, lot.SourceRow, lot.Symbol, classification, payload, issue, expectedSHA256); err != nil {
			return result, fmt.Errorf("persist import item row %d: %w", lot.SourceRow, err)
		}
		items++
		if !lot.Included {
			continue
		}
		instrumentID := deterministicID("instrument", lot.Symbol, lot.Currency)
		if _, err := tx.ExecContext(ctx, `INSERT INTO instruments(id, symbol, description, currency, asset_type, exchange, created_at)
			VALUES (?, ?, ?, ?, NULL, NULL, ?) ON CONFLICT(symbol, currency) DO NOTHING`,
			instrumentID, lot.Symbol, lot.Description, lot.Currency, now); err != nil {
			return result, fmt.Errorf("persist instrument %s: %w", lot.Symbol, err)
		}
		lotID := deterministicID("lot", expectedSHA256, strconv.Itoa(lot.SourceRow))
		sourceRef := sourcePath + "#row=" + strconv.Itoa(lot.SourceRow)
		if _, err := tx.ExecContext(ctx, `INSERT INTO lots(id, portfolio_id, instrument_id, account_id, quantity, unit_cost, cost_basis, currency, acquisition_date, source_row, source_ref, source_sha256, created_at)
			VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, lotID, defaultPortfolioID, instrumentID,
			lot.Quantity, nullableString(lot.UnitCost), nullableString(lot.CostBasis), lot.Currency, nullableString(lot.AcquisitionDate), lot.SourceRow, sourceRef, expectedSHA256, now); err != nil {
			return result, fmt.Errorf("persist lot row %d: %w", lot.SourceRow, err)
		}
		lots++
	}
	for _, unsupported := range preview.Unsupported {
		payload, err := json.Marshal(unsupported)
		if err != nil {
			return result, fmt.Errorf("marshal unsupported row %d: %w", unsupported.SourceRow, err)
		}
		itemID := deterministicID("item", expectedSHA256, strconv.Itoa(unsupported.SourceRow), "unsupported")
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_items(id, import_run_id, source_row, symbol, classification, payload, issue, source_sha256)
			VALUES (?, ?, ?, NULL, 'unsupported', ?, ?, ?)`, itemID, runID, unsupported.SourceRow, payload, unsupported.Reason, expectedSHA256); err != nil {
			return result, fmt.Errorf("persist unsupported row %d: %w", unsupported.SourceRow, err)
		}
		items++
	}
	auditPayload, _ := json.Marshal(map[string]any{"source_sha256": expectedSHA256, "source_table_rows": preview.Summary.SourceTableRows, "lots": lots, "aggregate_rows": preview.Summary.AggregateRows, "distinct_symbols": preview.Summary.DistinctSymbols})
	auditID := deterministicID("audit", expectedSHA256)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id, actor, action, entity_type, entity_id, payload, created_at)
		VALUES (?, 'approved-import', 'portfolio.import.approved', 'import_run', ?, ?, ?)`, auditID, runID, auditPayload, now); err != nil {
		return result, fmt.Errorf("persist audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit import transaction: %w", err)
	}
	return domain.ImportResult{Status: "applied", ImportRunID: runID, PortfolioID: defaultPortfolioID, SourceSHA256: expectedSHA256, LotsImported: lots, ItemsImported: items}, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func deterministicID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))
}

// Status returns only aggregate import metadata and never lot values.
func (s *Store) Status(ctx context.Context) (domain.ImportStatus, error) {
	var status domain.ImportStatus
	queries := []struct {
		dest *int
		sql  string
	}{
		{&status.PortfolioCount, "SELECT COUNT(*) FROM portfolios"},
		{&status.InstrumentCount, "SELECT COUNT(*) FROM instruments"},
		{&status.LotCount, "SELECT COUNT(*) FROM lots"},
		{&status.ImportRunCount, "SELECT COUNT(*) FROM import_runs"},
		{&status.ImportItemCount, "SELECT COUNT(*) FROM import_items"},
		{&status.AuditEventCount, "SELECT COUNT(*) FROM audit_events"},
	}
	for _, query := range queries {
		if err := s.DB.QueryRowContext(ctx, query.sql).Scan(query.dest); err != nil {
			return status, err
		}
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT DISTINCT currency FROM instruments ORDER BY currency")
	if err != nil {
		return status, err
	}
	defer rows.Close()
	for rows.Next() {
		var currency string
		if err := rows.Scan(&currency); err != nil {
			return status, err
		}
		status.Currencies = append(status.Currencies, currency)
	}
	if err := rows.Err(); err != nil {
		return status, err
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT source_sha256, created_at FROM import_runs WHERE status='applied' ORDER BY created_at DESC LIMIT 1").Scan(&status.SourceSHA256, &status.ImportedAt); err == sql.ErrNoRows {
		return status, nil
	} else if err != nil {
		return status, err
	}
	return status, nil
}

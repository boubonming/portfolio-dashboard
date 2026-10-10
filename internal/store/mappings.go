package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/marketdata"
)

type ProviderMappingInput struct {
	InstrumentID   string
	Provider       marketdata.Provider
	ProviderSymbol string
	QuoteCurrency  string
	ActiveFrom     string
	Provenance     string
}

func (s *Store) AddProviderMapping(ctx context.Context, input ProviderMappingInput) (domain.ProviderMapping, error) {
	provider, err := marketdata.ParseProvider(input.Provider.String())
	if err != nil || provider == marketdata.ProviderManual {
		return domain.ProviderMapping{}, fmt.Errorf("unsupported mapping provider")
	}
	instrumentID := strings.TrimSpace(input.InstrumentID)
	providerSymbol := strings.TrimSpace(input.ProviderSymbol)
	currency := strings.ToUpper(strings.TrimSpace(input.QuoteCurrency))
	provenance := strings.TrimSpace(input.Provenance)
	if instrumentID == "" || providerSymbol == "" || currency == "" || provenance == "" {
		return domain.ProviderMapping{}, fmt.Errorf("instrument, provider symbol, quote currency, and provenance are required")
	}
	active := strings.TrimSpace(input.ActiveFrom)
	if active == "" {
		active = time.Now().UTC().Format(time.RFC3339Nano)
	}
	parsed, err := time.Parse(time.RFC3339Nano, active)
	if err != nil {
		return domain.ProviderMapping{}, fmt.Errorf("invalid active-from timestamp: %w", err)
	}
	active = parsed.UTC().Format(time.RFC3339Nano)
	var instrumentCurrency string
	if err := s.DB.QueryRowContext(ctx, "SELECT currency FROM instruments WHERE id = ?", instrumentID).Scan(&instrumentCurrency); err == sql.ErrNoRows {
		return domain.ProviderMapping{}, fmt.Errorf("unknown instrument %s", instrumentID)
	} else if err != nil {
		return domain.ProviderMapping{}, err
	}
	if strings.ToUpper(instrumentCurrency) != currency {
		return domain.ProviderMapping{}, fmt.Errorf("mapping quote currency %s does not match instrument currency %s", currency, instrumentCurrency)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := deterministicID("mapping", instrumentID, provider.String(), providerSymbol, currency, active)
	mapping := domain.ProviderMapping{ID: id, InstrumentID: instrumentID, Provider: provider.String(), ProviderSymbol: providerSymbol, QuoteCurrency: currency, ActiveFrom: active, Provenance: provenance, CreatedAt: now}
	payload, _ := json.Marshal(mapping)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProviderMapping{}, fmt.Errorf("begin provider mapping: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO instrument_provider_mappings(id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, instrumentID, provider.String(), providerSymbol, currency, active, provenance, now)
	if err != nil {
		return domain.ProviderMapping{}, fmt.Errorf("persist provider mapping: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return domain.ProviderMapping{}, fmt.Errorf("inspect provider mapping: %w", err)
	}
	if inserted == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at FROM instrument_provider_mappings WHERE id = ?`, id).Scan(&mapping.ID, &mapping.InstrumentID, &mapping.Provider, &mapping.ProviderSymbol, &mapping.QuoteCurrency, &mapping.ActiveFrom, &mapping.Provenance, &mapping.CreatedAt); err != nil {
			return domain.ProviderMapping{}, err
		}
		if err := tx.Commit(); err != nil {
			return domain.ProviderMapping{}, err
		}
		return mapping, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor,action,entity_type,entity_id,payload,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, deterministicID("audit", "mapping", id), "administrative-mapping", "market.mapping.added", "provider_mapping", id, payload, now); err != nil {
		return domain.ProviderMapping{}, fmt.Errorf("audit provider mapping: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.ProviderMapping{}, err
	}
	return mapping, nil
}

func (s *Store) ListProviderMappings(ctx context.Context, instrumentID string) ([]domain.ProviderMapping, error) {
	query := `SELECT id,instrument_id,provider,provider_symbol,quote_currency,active_from,provenance,created_at FROM instrument_provider_mappings`
	args := []any{}
	if strings.TrimSpace(instrumentID) != "" {
		query += " WHERE instrument_id = ?"
		args = append(args, strings.TrimSpace(instrumentID))
	}
	query += " ORDER BY instrument_id, provider, active_from"
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mappings []domain.ProviderMapping
	for rows.Next() {
		var mapping domain.ProviderMapping
		if err := rows.Scan(&mapping.ID, &mapping.InstrumentID, &mapping.Provider, &mapping.ProviderSymbol, &mapping.QuoteCurrency, &mapping.ActiveFrom, &mapping.Provenance, &mapping.CreatedAt); err != nil {
			return nil, err
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

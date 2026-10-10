package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
)

func TestManualObservationsValidateAppendAndAudit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("e", 64))
	if _, err = s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	first, err := s.AddQuote(ctx, QuoteInput{Symbol: "SYMA", Currency: "USD", Price: "1.25", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic fixture"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddQuote(ctx, QuoteInput{Symbol: "SYMA", Currency: "USD", Price: "1.30", Source: "manual", MarketAt: time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano), Basis: "close", Provenance: "synthetic fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("dated quote observations must have distinct IDs")
	}
	if _, err = s.AddQuote(ctx, QuoteInput{Symbol: "SYMA", Currency: "USD", Price: "0", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic"}); err == nil {
		t.Fatal("zero quote accepted")
	}
	if _, err = s.AddQuote(ctx, QuoteInput{Symbol: "SYMA", Currency: "USD", Price: "1", Source: "manual", MarketAt: at, Basis: "close", Provenance: ""}); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if _, err = s.AddFXRate(ctx, FXInput{BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.5", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddFXRate(ctx, FXInput{BaseCurrency: "MYR", QuoteCurrency: "USD", Rate: "0.22", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic fixture"}); err == nil || !strings.Contains(err.Error(), "orientation") {
		t.Fatalf("reverse FX orientation accepted: %v", err)
	}
	if _, err = s.AddFXRate(ctx, FXInput{BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.6", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic fixture"}); err == nil {
		t.Fatal("same-time directed FX duplicate accepted")
	}
	if _, err = s.AddFXRate(ctx, FXInput{BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4.6", Source: "manual", MarketAt: time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano), Basis: "close", Provenance: "synthetic fixture"}); err != nil {
		t.Fatalf("later same-direction FX history rejected: %v", err)
	}
	var audits int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action LIKE 'market.%'").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 4 {
		t.Fatalf("market audits = %d", audits)
	}
}

func TestSnapshotIsolatesPortfolioLotsAndProvenance(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	firstHash := strings.Repeat("h", 64)
	preview := approvedPreview(firstHash)
	if _, err = s.Apply(ctx, preview, "first.md", firstHash, "MYR"); err != nil {
		t.Fatal(err)
	}
	secondHash := strings.Repeat("i", 64)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = s.DB.Exec(`INSERT INTO portfolios(id,name,reporting_currency,created_at,updated_at) VALUES(?,?,?,?,?)`, "portfolio-second", "Second Portfolio", "MYR", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO instruments(id,symbol,currency,created_at) VALUES(?,?,?,?)`, "instrument-second", "SECOND", "USD", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO lots(id,portfolio_id,instrument_id,quantity,currency,source_ref,source_sha256,created_at) VALUES(?,?,?,?,?,?,?,?)`, "lot-second", "portfolio-second", "instrument-second", "7", "USD", "second.md#row=1", secondHash, now); err != nil {
		t.Fatal(err)
	}
	asOf := time.Now().UTC()
	firstStatus, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: defaultPortfolioID, ReportingCurrency: "MYR", CalculationVersion: "isolation.v1", AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	secondStatus, err := s.CreateSnapshot(ctx, SnapshotRequest{PortfolioID: "portfolio-second", ReportingCurrency: "MYR", CalculationVersion: "isolation.v1", AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := s.SnapshotShow(ctx, firstStatus.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := s.SnapshotShow(ctx, secondStatus.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	firstPayload := firstRaw.(snapshotPayload)
	secondPayload := secondRaw.(snapshotPayload)
	if len(firstPayload.Input.Lots) != 33 || len(secondPayload.Input.Lots) != 1 {
		t.Fatalf("portfolio lot counts = %d and %d", len(firstPayload.Input.Lots), len(secondPayload.Input.Lots))
	}
	if firstPayload.Input.Lots[0].SourceSHA256 != firstHash || secondPayload.Input.Lots[0].ID != "lot-second" || secondPayload.Input.Lots[0].SourceSHA256 != secondHash {
		t.Fatalf("portfolio payloads leaked or lost provenance: first=%+v second=%+v", firstPayload.Input.Lots[0], secondPayload.Input.Lots[0])
	}
	for _, lot := range firstPayload.Input.Lots {
		if lot.ID == "lot-second" || lot.SourceSHA256 == secondHash {
			t.Fatalf("second portfolio lot leaked into first snapshot: %+v", lot)
		}
	}
}

func TestSnapshotCompleteAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("f", 64))
	if _, err = s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	seen := map[string]bool{}
	for _, lot := range preview.Lots {
		if !lot.Included || seen[lot.Symbol] {
			continue
		}
		seen[lot.Symbol] = true
		if _, err = s.AddQuote(ctx, QuoteInput{Symbol: lot.Symbol, Currency: lot.Currency, Price: "2", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AddFXRate(ctx, FXInput{BaseCurrency: "USD", QuoteCurrency: "MYR", Rate: "4", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	request := SnapshotRequest{ReportingCurrency: "MYR", CalculationVersion: "test.v1", AsOf: time.Now().UTC(), MaxAge: time.Hour * 2}
	first, err := s.CreateSnapshot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Complete || first.ReportingTotal == "" {
		t.Fatalf("first snapshot = %+v", first)
	}
	second, err := s.CreateSnapshot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotID != first.SnapshotID {
		t.Fatalf("not idempotent: %s vs %s", first.SnapshotID, second.SnapshotID)
	}
	versioned, err := s.CreateSnapshot(ctx, SnapshotRequest{ReportingCurrency: "MYR", CalculationVersion: "test.v2", AsOf: request.AsOf, MaxAge: request.MaxAge})
	if err != nil {
		t.Fatal(err)
	}
	if versioned.SnapshotID == first.SnapshotID || versioned.CalculationVersion != "test.v2" {
		t.Fatalf("calculation version did not bind snapshot: %+v", versioned)
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM snapshots").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("snapshots = %d", count)
	}
	if _, err = s.DB.Exec("UPDATE snapshots SET payload = '{}' WHERE id = ?", first.SnapshotID); err == nil {
		t.Fatal("snapshot update was allowed")
	}
	aggregate, err := s.SnapshotShow(ctx, first.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := aggregate.(domain.SnapshotStatus); !ok {
		t.Fatalf("aggregate type = %T", aggregate)
	}
}

func TestSnapshotMissingDependencyIsIncomplete(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	preview := approvedPreview(strings.Repeat("g", 64))
	if _, err = s.Apply(ctx, preview, "fixture.md", preview.SourceSHA256, "MYR"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err = s.AddQuote(ctx, QuoteInput{Symbol: "SYMA", Currency: "USD", Price: "2", Source: "manual", MarketAt: at, Basis: "close", Provenance: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	status, err := s.CreateSnapshot(ctx, SnapshotRequest{ReportingCurrency: "MYR", AsOf: time.Now().UTC(), MaxAge: time.Hour * 2})
	if err != nil {
		t.Fatal(err)
	}
	if status.Complete || status.ReportingTotal != "" || status.MissingCount == 0 {
		t.Fatalf("status = %+v", status)
	}
}

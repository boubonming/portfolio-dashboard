# Portfolio Dashboard

Private, single-user portfolio dashboard scaffold. The Obsidian holdings note remains authoritative until the user reviews and explicitly approves the import preview.

The application currently serves a minimal React shell and a health endpoint. It does not expose state-changing portfolio HTTP endpoints, make market-data calls, access credentials, or deploy anything.

## Prerequisites

- Go 1.23 or newer
- Node.js and npm

## Frontend checks and production build

Run from the repository root:

```sh
npm --prefix frontend ci
npm --prefix frontend run typecheck
npm --prefix frontend run test -- --run
npm --prefix frontend run build
```

The Vite build writes generated assets to `internal/web/dist/`, which the Go server embeds. Build the frontend before Go commands that compile the embedded assets.

## Go checks

```sh
go test ./... -count=1
go vet ./...
go build ./cmd/portfolio-dashboard
```

## Run locally

```sh
PORTFOLIO_BIND_ADDRESS=:8080 \
PORTFOLIO_DATA_PATH=./portfolio-dashboard.sqlite \
go run ./cmd/portfolio-dashboard
```

Configuration is environment-driven. `PORTFOLIO_REPORTING_CURRENCY` defaults to `MYR`; timestamps stored by the application are UTC and human-facing dates use Singapore time in later phases.

## Non-mutating import preview

Preview the authoritative note without changing the note or SQLite:

```sh
go run ./cmd/portfolio-dashboard import-preview \
  -input /path/to/Portfolio.md > preview.json
```

The command writes only JSON to stdout. It preserves source symbols, decimal strings, currencies, cost fields, raw source rows and provenance. Aggregate NVDA and INTC rows are classified and excluded from included lots; missing fields and unsupported rows are reported explicitly. Run it repeatedly to obtain the same deterministic output for an unchanged source. Do not commit a preview containing private holdings values.

## Approved transactional import

Applying an import is a separate, explicit operation. First inspect the preview and verify its `source_sha256` and reconciliation summary. Then pass that exact hash and the explicit approval flag:

```sh
go run ./cmd/portfolio-dashboard import-apply \
  -input /path/to/Portfolio.md \
  -db /path/to/portfolio-dashboard.sqlite \
  -expected-sha256 941a742487ba9ec830192a4efea3b54435086af97844c0c1d00ab63a83aa9a27 \
  -approve
```

Apply re-runs the parser and fails closed before any database write when approval is absent, the source hash differs, unsupported rows exist, the approved 35/33/2/31 reconciliation counts differ, or a required symbol, currency or decimal invariant is invalid. The initial import stores MYR as the reporting currency, retains original instrument currencies, keeps each of the 33 included source lots (including separate duplicate-symbol lots), and records the two aggregate rows as excluded import items. Missing broker and acquisition/opening dates remain anomalies; they are not silently invented.

The import is one SQLite transaction. The source hash and source row are retained as provenance, schema uniqueness constraints prevent duplicate runs/items/lots/audit events, and re-applying the same approved hash returns `already_applied` without writing duplicates. A different already-applied hash is rejected; obtain and review a new preview before explicitly approving a replacement workflow. The Obsidian note is never rewritten by these commands.

Use the read-only status command for aggregate inspection without dumping private lot values:

```sh
go run ./cmd/portfolio-dashboard import-status \
  -db /path/to/portfolio-dashboard.sqlite
```

Status reports portfolio, instrument, lot, import-item and audit-event counts, currencies, source hash and import timestamp. It does not include quantities, cost values or source-row text.

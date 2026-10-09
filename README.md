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

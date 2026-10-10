# Portfolio Dashboard

Private, single-user portfolio dashboard scaffold. The Obsidian holdings note has completed reconciliation and its import has been explicitly approved; the resulting imported SQLite portfolio is authoritative for calculations.

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

## Phase B: dated observations and immutable snapshots

This slice performs exact accounting with a standard-library `math/big` rational
decimal implementation. Values are parsed directly from decimal strings, stored
as canonical SQLite `TEXT`, and serialized as JSON strings; no `float64` path is
used. Multiplication is exact. FX division uses an explicit 18-decimal,
half-away-from-zero rounding rule. Display rounding is deliberately outside the
ledger calculation.

The provider boundary now includes credential-safe fake-server-tested adapters
for Finnhub, Alpha Vantage, CoinGecko and Open Exchange Rates. Credentials are
optional process-start configuration and are never required by tests.

Add a quote (the symbol/currency pair must already exist in the approved import):

```sh
go run ./cmd/portfolio-dashboard quote-add -db /path/to/portfolio-dashboard.sqlite \
  -symbol 1295 -currency MYR -price 4.75 -source manual \
  -market-at 2026-10-09T08:00:00Z -basis close \
  -provenance 'broker statement or reviewed source reference'
```

Add an explicitly directed FX observation:

```sh
go run ./cmd/portfolio-dashboard fx-add -db /path/to/portfolio-dashboard.sqlite \
  -base USD -quote MYR -rate 4.20 -source manual \
  -market-at 2026-10-09T08:00:00Z -basis close \
  -provenance 'dated source reference'
```

Both commands reject malformed or non-positive decimals, unknown instruments,
ambiguous currency pairs, future market timestamps beyond the controlled
tolerance, and missing source/basis/provenance. Writes are append-only and
produce audit events. Market timestamps up to and including five minutes ahead
of the accounting as-of time are accepted and classified as fresh with zero
age; timestamps beyond that tolerance are rejected at ingestion and classified
as future/invalid during valuation. The first accepted orientation of an
unordered FX pair (for example, USD/MYR) becomes canonical; the reverse
orientation is rejected, while later observations in the same direction remain
append-only. Multiple observations for one directed pair at the same market
timestamp are rejected.

Create and inspect a snapshot without dumping private lot values:

```sh
go run ./cmd/portfolio-dashboard snapshot-create -db /path/to/portfolio-dashboard.sqlite \
  -reporting-currency MYR -as-of 2026-10-09T12:00:00Z -max-age 36h
go run ./cmd/portfolio-dashboard snapshot-status -db /path/to/portfolio-dashboard.sqlite
go run ./cmd/portfolio-dashboard snapshot-show -db /path/to/portfolio-dashboard.sqlite \
  -snapshot SNAPSHOT_ID
```

Use `-include-lots` on `snapshot-show` only when explicitly required. A snapshot
binds the imported lot/source hashes, selected quote and FX IDs, reporting
currency, calculation version, UTC creation/as-of timestamps and presentation
timezone metadata (`Asia/Singapore`). Identical calculation inputs and version
return the existing immutable snapshot; a new calculation version receives a
new snapshot ID instead of mutating history.

Fresh observations are classified in UTC as `fresh`, `stale`, `future`,
`invalid`, or `missing`; SGT is presentation metadata only. Missing or stale
quotes/rates never become zero. The aggregate status reports original-currency
subtotals and source timestamps while setting `complete: false` and omitting a
reporting total whenever any dependency is unavailable. A complete reporting
total is emitted only when every lot has a valid quote and a unique explicit FX
path to the selected reporting currency. Inverted FX paths are supported, while
multiple possible paths are rejected as ambiguous.

## Free-provider mappings and refresh

The administrative refresh uses only explicitly recorded mappings. It never
guesses an international ticker, sends holdings to a provider, or falls back to
an unofficial scraper. Add a mapping only after checking the provider's exact
symbol/coin ID and record the source and activation time:

```sh
go run ./cmd/portfolio-dashboard mapping-add -db portfolio.sqlite \
  -instrument INSTRUMENT_ID -provider finnhub \
  -provider-symbol AAPL -quote-currency USD \
  -active-from 2026-10-09T00:00:00Z \
  -provenance 'provider documentation checked on 2026-10-09'
go run ./cmd/portfolio-dashboard mapping-list -db portfolio.sqlite
```

Mappings are append-only and versioned by `active-from`; a new symbol or
listing is a new mapping row. The accepted quote providers are Finnhub for
mapped US securities, Alpha Vantage `GLOBAL_QUOTE` for mapped international
symbols, and CoinGecko for mapped coin IDs. Open Exchange Rates is used for
USD-base FX rates and does not need an instrument ticker mapping. Manual quote
and FX observations remain available through `quote-add` and `fx-add`.

For a default refresh, each instrument uses the active mapping with the latest
`active-from` timestamp across all selected quote providers, so a newer mapping
can migrate an instrument to another provider. An explicit provider subset
applies the same rule within that subset. If two different providers share the
latest timestamp, the mapping is reported as explicitly ambiguous and the
instrument is not quoted until the mapping is resolved; provider ordering is
never used as a tie-breaker.

At process start, credentials are read only from `FINNHUB_API_KEY`,
`ALPHA_VANTAGE_API_KEY`, and `OPEN_EXCHANGE_RATES_APP_ID`. CoinGecko uses its
public free endpoint in this slice. Credentials are not persisted, printed,
included in error messages, audit payloads, or frontend settings. Optional
`PORTFOLIO_*_BASE_URL` variables are for local fake-server tests or an
operator-approved endpoint: `PORTFOLIO_FINNHUB_BASE_URL`,
`PORTFOLIO_ALPHA_VANTAGE_BASE_URL`, `PORTFOLIO_COINGECKO_BASE_URL`, and
`PORTFOLIO_OPEN_EXCHANGE_RATES_BASE_URL`; production defaults use the provider
HTTPS APIs.
There are no live-key tests in this repository: adapter tests must use local
`httptest` servers and the refresh command must be exercised with fake
endpoints only.

Run a bounded, aggregate-only refresh after mappings are reviewed:

```sh
go run ./cmd/portfolio-dashboard market-refresh -db portfolio.sqlite \
  -portfolio portfolio-default -providers finnhub,coingecko,open_exchange_rates
```

Provider quotas are independent. CoinGecko requests are batched by quote
currency and Open Exchange Rates obtains all required USD-base rates in one
call; cross-rates use exact decimal division. Refresh runs retain successful
observations, append a redacted audit/run record, and report `incomplete` with
missing mappings or classified provider failures rather than fabricating
values. Repeating an identical provider observation is idempotent. A subsequent
snapshot remains incomplete when a refresh gap leaves a required quote or FX
rate missing/stale; it never presents a partial total as complete.

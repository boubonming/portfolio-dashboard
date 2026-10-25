# Portfolio Dashboard

Private, single-user portfolio dashboard. The canonical SQLite database is authoritative for calculations; fresh databases start empty and current holdings enter through authenticated UI/API workflows. Authenticated holding create/archive handlers and their frontend controls are shipped.

The application serves the React dashboard, health endpoint, authenticated portfolio read APIs, and portfolio-scoped holding create/archive endpoints. It does not access brokerage credentials, place trades, or deploy anything.

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
PORTFOLIO_BIND_ADDRESS=127.0.0.1:8080 \
PORTFOLIO_PUBLIC_ORIGIN=http://127.0.0.1:8080 \
PORTFOLIO_DATA_PATH=./portfolio-dashboard.sqlite \
PORTFOLIO_ENV=development PORTFOLIO_COOKIE_SECURE=false \
go run ./cmd/portfolio-dashboard
```

Configuration is environment-driven. `PORTFOLIO_REPORTING_CURRENCY` defaults to `MYR`; timestamps stored by the application are UTC and human-facing dates use Singapore time in later phases.

### Optional scheduled market refresh

The in-process scheduler is disabled when `PORTFOLIO_MARKET_REFRESH_CRON` is
blank. To enable a strict standard five-field cron expression, set the IANA
timezone (default `Asia/Singapore`), bounded portfolio ID (default
`portfolio-default`), and optionally an exact uppercase reporting currency.
For example, `PORTFOLIO_MARKET_REFRESH_CRON='0 5 * * 1-5'` runs on Singapore
weekday mornings. It does not run at startup or backfill missed slots. Each
slot uses the same durable refresh, immutable snapshot, audit, Activity,
missing-mapping, and replay workflow as the browser and machine endpoints.
Scheduled work is attributed as `scheduler` / `market-refresh-scheduler`,
does not use or expose the Hermes bearer, and is stopped with the server.
Invalid enabled configuration fails before HTTP serving; provider failures are
bounded and do not stop later slots.

The HTTP server fails closed unless authentication is configured explicitly:
`PORTFOLIO_AUTH_USERNAME`, a bcrypt `PORTFOLIO_AUTH_PASSWORD_HASH` (cost 10 or
higher), `PORTFOLIO_PUBLIC_ORIGIN` (one exact `https://host[:port]` origin), and
an independent random `PORTFOLIO_SESSION_SECRET` encoded as at least 32 random
bytes are required. Generate a new reviewable secret without exposing an
existing one with `openssl rand -base64 32`; place that command's output only in
secret injection. Sessions are short-lived, signed, in-memory and carried
only by an `HttpOnly`, `SameSite=Strict` cookie. Production must use TLS and
the default secure cookie setting. For local HTTP development only, explicitly
set `PORTFOLIO_ENV=development PORTFOLIO_COOKIE_SECURE=false`; there are no
default credentials or secrets. CSRF tokens are returned by session bootstrap,
kept in frontend memory, and required on unsafe same-origin API requests.

Generate the bcrypt password hash interactively; never put the plaintext
password on the command line:

```sh
htpasswd -nBC 12 portfolio-admin
```

Enter and confirm the password at the prompts. The output includes
`portfolio-admin:` followed by the bcrypt value. Copy only the bcrypt portion
after the first colon, not `portfolio-admin:`. Store the complete bcrypt value
in single quotes in `.env`, for example
`PORTFOLIO_AUTH_PASSWORD_HASH='REPLACE_WITH_COMPLETE_BCRYPT_VALUE'`. Never
paste the hash or plaintext password into chat, and never commit either one.

The optional least-privilege machine identity is an external Hermes bearer
identity, not an embedded runtime. Configure all seven settings together:
`PORTFOLIO_MACHINE_PRINCIPAL_ID` (stable non-secret audit identity),
`PORTFOLIO_MACHINE_PORTFOLIO_ID` (bound portfolio),
`PORTFOLIO_MACHINE_BEARER_SECRET` (independent random printable secret),
`PORTFOLIO_MACHINE_PORTFOLIO_READ` (scoped snapshot/context/read grant),
`PORTFOLIO_MACHINE_REVIEWS_WRITE` (immutable advisory review submission
 grant), `PORTFOLIO_MACHINE_MARKET_REFRESH` (independent market-data
 refresh trigger grant). The fail-closed disabled state has the three
 identity/secret values blank and all four grants `false`; enabling a
 capability requires its explicit grant. A partial configuration,
invalid grant, or bearer secret shorter than 32 printable random bytes fails
closed. The bearer secret is never written to SQLite, logs, URLs, frontend
assets, or diagnostics; only the non-secret principal binding is registered in
the database. This identity can read the bound portfolio and submit immutable
advisory reviews, but cannot edit holdings or trade. Market-provider settings
are optional and independent of this machine identity.

### Explicit machine holding grant lifecycle

`PORTFOLIO_MACHINE_HOLDINGS_WRITE=true` is a runtime authorization bound. It
does not promote an existing persisted principal, because startup deliberately
preserves a prior operator revocation. After confirming the exact configured
principal and portfolio, an operator must make the transition explicitly:

```sh
PORTFOLIO_MACHINE_PRINCIPAL_ID=the-configured-id \
PORTFOLIO_MACHINE_PORTFOLIO_ID=portfolio-default \
PORTFOLIO_MACHINE_PORTFOLIO_READ=true \
PORTFOLIO_MACHINE_HOLDINGS_WRITE=true \
go run ./cmd/portfolio-dashboard machine-grant-holdings \
  -db /path/to/portfolio-dashboard.sqlite \
  -principal the-configured-id -portfolio portfolio-default \
  -confirm 'GRANT HOLDINGS'
go run ./cmd/portfolio-dashboard machine-inspect \
  -db /path/to/portfolio-dashboard.sqlite \
  -principal the-configured-id -portfolio portfolio-default
```

The grant command requires the runtime read/write configuration and exact
non-secret identity binding, accepts only an existing active read-enabled row,
and writes a secret-free audit event in the same transaction. It never creates
or reactivates a principal, grants review scope, or changes holdings. Repeating
it reports `already_granted`. To revoke without raw SQL, use the same exact
binding and `-confirm 'REVOKE HOLDINGS'`; inspect should then report
`holdings_write:false`. Reopening the database or restarting the service cannot
undo that persisted revocation; a new deliberate grant is required.

### Explicit machine market-refresh grant lifecycle

`PORTFOLIO_MACHINE_MARKET_REFRESH=true` is independent of holding mutation. The
canonical refresh endpoint requires this runtime flag, the persisted
`market_data:refresh` grant, and the persisted portfolio-read grant for the
exact configured principal and portfolio. Grant or revoke it only after
confirming the same binding and runtime read prerequisite:

```sh
PORTFOLIO_MACHINE_PRINCIPAL_ID=the-configured-id \
PORTFOLIO_MACHINE_PORTFOLIO_ID=portfolio-default \
PORTFOLIO_MACHINE_PORTFOLIO_READ=true \
PORTFOLIO_MACHINE_MARKET_REFRESH=true \
go run ./cmd/portfolio-dashboard machine-grant-market-refresh \
  -db /path/to/portfolio-dashboard.sqlite \
  -principal the-configured-id -portfolio portfolio-default \
  -confirm 'GRANT MARKET DATA REFRESH'
go run ./cmd/portfolio-dashboard machine-revoke-market-refresh \
  -db /path/to/portfolio-dashboard.sqlite \
  -principal the-configured-id -portfolio portfolio-default \
  -confirm 'REVOKE MARKET DATA REFRESH'
```

Transitions are idempotent, append a secret-free operator audit event with
portfolio ownership, and never create, reactivate, or grant holding authority.
`machine-inspect` reports the persisted `market_data_refresh` state without
exposing the bearer secret.

`Store.Open` runs the forward migrations on startup. Existing databases must
complete migration 015 before these commands are used; it preserves existing
rows and makes both machine grant/revoke audit events and their portfolio
ownership associations append-only.

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

Add a quote for a symbol/currency pair already present in the canonical database:

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

Use `-include-lots` on `snapshot-show` only when explicitly required. A snapshot binds the selected quote and FX IDs, canonical holding state,
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

The normal owner workflow is in the authenticated **Market data** view. It
shows every active instrument by symbol, description and quote currency, with
its current exact mapping or an explicit `Not mapped` state. Enter exact
provider symbols/coin IDs in the table, review the provider and provenance,
then use **Save all mappings** once. The submission is atomic, append-only and
optimistically concurrency checked; it does not require copying an internal
instrument ID or running one command per holding.

Provider keys authorize access; mappings identify exact listings. A key never
proves that a ticker is the right international listing, ADR, or crypto ID, so
the UI never guesses or silently reuses a holding symbol. Mappings are
one-time/revisioned setup, not a recurring refresh requirement. Open Exchange
Rates supplies FX and does not need an instrument ticker mapping.

Use the single **Refresh prices** action in that same view. It performs the
bounded provider refresh and then creates the immutable snapshot for the
selected reporting currency. The result reports provider readiness, missing
instruments by human label, quote/FX freshness, and complete/incomplete state;
partial values are never shown as a complete total. Secrets remain server-side
and only readiness names/booleans are exposed.

The browser refresh endpoint requires an `Idempotency-Key` and stores its
canonical request fingerprint and committed response receipt in SQLite. The
receipt namespace is actor category, bounded actor ID, portfolio, operation and
key; the authenticated browser uses the non-secret `local-owner` actor ID.
Replaying the same key and request after a process restart returns the original
receipt without another provider call or snapshot effect. Reusing a key for a
different reporting currency is a conflict, and pending or failed operations
never claim a complete valuation. Refresh audit and Activity rows remain
portfolio-owned and append-only.

The status panel distinguishes configuration, authentication, rate-limit,
timeout, transport, invalid-value/timestamp, currency/symbol mismatch,
mapping, and persistence failures. It also names missing, stale, invalid and
future quote/FX dependencies when the server provides enough instrument or
currency context; incomplete snapshots withhold totals.

The existing `mapping-add`, `mapping-list`, `market-refresh` and
`snapshot-create` commands remain available for operators and tests. They are
not required for ordinary owner setup. The accepted quote providers are
Finnhub for mapped US securities, Alpha Vantage `GLOBAL_QUOTE` for mapped
international symbols, and CoinGecko for mapped coin IDs. Manual quote and FX
observations remain available through `quote-add` and `fx-add`.

For a default refresh, each instrument uses the active mapping with the latest
`active-from` timestamp across all selected quote providers, so a newer mapping
can migrate an instrument to another provider. An explicit provider subset
applies the same rule within that subset. If two different providers share the
latest timestamp, the mapping is reported as explicitly ambiguous and the
instrument is not quoted until the mapping is resolved; provider ordering is
never used as a tie-breaker.

The authenticated browser setup also exposes `POST /api/v1/portfolios/{portfolioId}/market-data/discover`. It contacts configured CoinGecko `/api/v3/search`, Finnhub `/api/v1/search`, or Alpha Vantage `SYMBOL_SEARCH` endpoints with only the holding symbol/search query. Returned identifiers and metadata are bounded, unverified candidates: owners must choose or edit an exact listing and explicitly confirm the full batch before any mapping is saved. Discovery is setup- and ETag-bound, performs no mapping or audit writes, and never sends provider credentials to the browser or portfolio/lot data to provider search requests; provider authentication stays server-side.

The same four market-data routes accept the explicitly scoped machine bearer alternative. Setup, discovery, and mapping remain limited to the exact configured principal and portfolio, a valid singleton bearer, `PORTFOLIO_MACHINE_PORTFOLIO_READ=true`, `PORTFOLIO_MACHINE_HOLDINGS_WRITE=true`, and the persisted portfolio-read plus holdings-write grant. The canonical refresh route instead requires `PORTFOLIO_MACHINE_PORTFOLIO_READ=true`, `PORTFOLIO_MACHINE_MARKET_REFRESH=true`, and the persisted `market_data:refresh` grant; it does not grant holding mutation authority. Machine discovery remains read-only; mapping and refresh retain the machine principal in audit/activity and refresh idempotency records. Machine requests omit browser CSRF and never receive provider credentials, session secrets, or owner-session privileges; missing or revoked runtime/persisted grants fail closed.

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

For operators who need the preserved CLI path, run a bounded, aggregate-only refresh after mappings are reviewed:

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

## Private OCI deployment package

The production packaging source is documented in [`docs/private-deployment.md`](docs/private-deployment.md). It includes a digest-pinned multi-stage `Dockerfile`, non-root/read-only runtime settings, the restricted `/healthz` healthcheck command, and the root [`compose.yaml`](compose.yaml) plus [`.env.example`](.env.example) deployment samples. Compose creates and manages the named `portfolio-data` volume automatically, so no host data directory or ownership setup is required; the database is `/data/portfolio-dashboard.sqlite` inside the service. Copy `.env.example` to `.env`, set mode `0600`, and run `docker compose -f compose.yaml ...`; Compose reads the root `.env` automatically. The default host binding is loopback-only (`PORTFOLIO_HOST_IP=127.0.0.1`); keep that setting when the reverse proxy runs directly on the host. When a containerized or external reverse proxy reaches a host-published port, set `PORTFOLIO_HOST_IP` to the specific current trusted interface address and configure the proxy upstream to that same address plus `PORTFOLIO_HOST_PORT`; retrieve the current interface address from live infrastructure rather than copying a stale example. Never use `0.0.0.0` unless explicitly accepting exposure on every host interface. With `PORTFOLIO_HOST_PORT=8086`, the upstream remains HTTP `<trusted-host-interface>:8086`; TLS and the exact HTTPS public origin remain deliberate reverse-proxy settings. Compose uses the repository-owned `ghcr.io/boubonming/portfolio-dashboard:main` image literally. Ordinary `docker compose up -d` uses the locally cached `:main` image when present; updates are deliberate, with `docker compose pull` followed by `docker compose up -d`. The `main` tag is mutable. The GitHub Actions workflow builds both Linux amd64 and arm64 on pull requests without publishing, and merged-main publication updates `main` and also publishes the full-SHA tag for the same OCI index with provenance and SBOM attestations. The deploy template [`deploy/compose.private.example.yml`](deploy/compose.private.example.yml) and compatibility env template remain available for existing links.

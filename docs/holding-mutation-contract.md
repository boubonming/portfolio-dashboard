# Portfolio holding mutation contract

Status: runtime holding create/archive handlers, the existing numbered migration, and authenticated frontend holding controls are shipped. This document does not add an import, brokerage, trading, or quote-refresh workflow.

This document defines the release contract implemented by the runtime. The SQL artifact and harness remain executable scratch-database feasibility evidence after baseline SQL migrations 001-008; runtime verification is provided by the Store and HTTP acceptance tests. The runtime initializes one empty canonical portfolio, after which an authenticated browser session or an explicitly authorized Hermes machine may create a lot or logically archive a lot.

The no-import-surface invariant is absolute for the target release: portfolio state is authoritative only in the canonical database, and holdings enter it through the authenticated UI or the portfolio-scoped holding API below. No external document, parser, import preview/apply/status operation, source hash, source row, or source reference is required or accepted by this contract. Legacy importer commands, source identity, and frontend/export consumers remain explicitly tracked removal work; they are not part of the shipped holding mutation authority.

## 1. Canonical API surface

The canonical paths are:

- `POST /api/v1/portfolios/{portfolioId}/holdings` creates one active lot.
- `DELETE /api/v1/portfolios/{portfolioId}/holdings/{holdingId}` archives one active lot.
- `GET /api/v1/portfolios/{portfolioId}/overview` returns the current read model and the exact `ETag` holdings revision witness used by both mutations. The canonical `portfolio-default` portfolio is always present after initialization, even when it has no holdings or snapshot.

There are no query variants, trailing-slash variants, encoded-slash identifiers, or alternate verbs. `portfolioId` and `holdingId` use the existing identifier grammar. A canonical path with an unsupported method returns 405 and its exact `Allow` value; an encoded or non-canonical path is not normalized into a mutation route.

The OpenAPI operation security list is an OR: a request authenticates as either the browser session or the machine bearer. Authorization then applies the actor-specific rules below. Both actors call the same future store operation, not separate browser and machine implementations.

Every private success and error response has `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, and `Vary: Cookie` (including machine errors to prevent authentication-state confusion). Create additionally returns `Location`; both mutations return the new `ETag`.

## 2. Authentication and authorization

### Browser session

A browser mutation requires the existing authenticated `portfolio_session` cookie, an `Origin` header exactly matching the configured application origin, and an `X-CSRF-Token` bound to that session. The cookie is HttpOnly, Secure in deployment, SameSite=Strict, and the CSRF token is returned only through the authenticated session bootstrap. Missing, malformed, expired, or revoked session is 401. Missing, invalid, cross-session, or replayed CSRF is 403. Missing, multiple, `null`, or non-matching Origin is 403; Referer is not an authority fallback. Browser callers must not send a bearer token, and a browser mutation does not need `holdings:write`.

The browser principal is the persisted `local-owner` principal. Its immutable permission witness is `portfolio:read`; the UI may create and remove only within the authenticated portfolio scope.

### Machine bearer

The machine request has exactly one `Authorization: Bearer <opaque-token>` header. The token is bounded to the configured singleton principal and is compared in constant time. It is never persisted, logged, returned, rendered, placed in a URL, or included in an audit payload. The configured principal must be active in SQLite, have the exact requested portfolio binding, and have both `portfolio:read` and the new independently configurable `holdings:write` grant. `holdings:write` defaults false and is not implied by `reviews:write`.

The configured grant and persisted grant are intersected at request time. A missing, malformed, duplicate, weak, invalid, disabled, revoked, wrong-portfolio, or insufficient bearer is 401 for missing/invalid credentials and 403 for an authenticated but unauthorized principal. Machine calls do not send or require Origin or CSRF. The API never reveals which grant or binding failed.

All persisted actor IDs use the bounded grammar `^[A-Za-z0-9][A-Za-z0-9._~-]*$` and the existing 1-128 byte bound. This applies to machine principal insert/update/upsert, idempotency ownership, archive actor fields, holding audit ownership, and any future Activity actor-ID column. Empty, first-character-invalid, whitespace, slash, control, Unicode, and overlength IDs are rejected before any write. Existing malformed legacy machine principals are retained as historical rows but cannot satisfy the active principal or mutation-idempotency guards and therefore cannot acquire holding-mutation authority or produce an API receipt.

### Permission witness

A successful receipt and append-only audit witness record only actor category, actor ID, and effective permission names. A browser witness is `browser_user/local-owner/portfolio:read`; a machine witness is `machine/<principal-id>/portfolio:read,holdings:write`. The witness is captured at commit time and is not recomputed when configuration later changes.

## 3. Create request and exact values

`HoldingCreateRequest` is an object with no unknown fields. Required fields are `instrument`, `quantity`, `unit_cost`, `cost_basis`, and `currency`. Optional fields are `account_id` and `acquisition_date`.

`instrument` is exactly one of:

- `{ "instrument_id": "..." }`, where the instrument is present in `portfolio_instruments`; or
- an inline definition with bounded `symbol`, optional `description`, `currency`, optional `asset_type`, and optional `exchange`. The server creates the global instrument and the portfolio association in the same transaction.

Sending both forms, neither form, unknown instrument fields, or an inline definition that collides with an existing global identity is invalid. An instrument is not portfolio-valid merely because its ID exists in another portfolio. Existing global instruments become valid for this portfolio only through the association. Existing 001-008 lots are backfilled into that association.

`quantity` is a positive canonical decimal string with at most 18 fractional digits. `unit_cost` is a non-negative canonical decimal string with at most 18 fractional digits, while `cost_basis` is the exact product and permits at most 36 fractional digits and 80 total characters (the maximum 80-digit integer product of two accepted 40-character inputs). Canonical strings have no plus sign, exponent, leading zero in the integer part, or trailing decimal point; fractional trailing zeroes are permitted and preserved as the declared scale (so `10.00` is not rewritten to `10`). JSON numbers, `float64`, JavaScript `Number`, and SQLite REAL are forbidden. The server parses decimal text into an exact integer-plus-scale representation and rejects overlong products rather than silently rounding. `currency` is uppercase ISO-like three-letter text and must equal the instrument currency.

`acquisition_date` is either null/omitted or exactly `YYYY-MM-DD`; timestamps, timezone suffixes, invalid calendar dates, and non-canonical dates are rejected. This contract selects the response-simplification provenance design: the public holding contract does not accept or expose client-supplied provenance. Source documents, import references, credentials, bearer values, CSRF values, account secrets, and raw request bodies are never holding fields. New mutations are attributed by their immutable actor/permission witness and append-only audit/Activity receipt.

An account, when supplied, must exist and have the requested portfolio as owner. A null account is valid. No account is created by this operation.

## 4. Removal and response receipts

Removal is an archive state transition, never `DELETE FROM lots`. The lot remains addressable by its ID with `lifecycle=removed` and its original immutable quantity, unit cost, cost basis, currency, account, acquisition date, and instrument. `removed_at`, actor category, and actor ID are appended as removal metadata. A removed lot is excluded from default current holdings reads. Its old snapshots, review targets, reviews, theses and thesis revisions, Activity events, and audit records remain unchanged and addressable.

Legacy `lots.created_at` is historical display/audit data and is preserved byte-for-byte. The contract artifact adds nullable `lots.created_at_order_key`, a separately persisted canonical UTC nanosecond comparison key. The trusted migration preflight parses RFC3339 values with no fraction, short fractions, nine-digit fractions, and offsets, normalizes them semantically to the 30-byte UTC form, and leaves the original value untouched. New rows require canonical `created_at` plus a non-null `created_at_order_key` equal to that canonical value; missing or mismatched keys are rejected. Nullable/unparseable keys are retained only for pre-contract malformed legacy rows, which remain readable but cannot be archived. Archive ordering compares canonical `removed_at` against the persisted key, so equal, earlier, and later instants are deterministic and never lexical comparisons against unconstrained historical text. The future numbered migration rollback must restore both the original bytes and the key state.

A successful create returns 201; a successful removal returns 200. A replay returns the exact stored receipt and original status/headers for the same actor category, actor ID, portfolio, operation, idempotency key, and request fingerprint. The physical namespace is exactly `(actor_category, actor_id, portfolio_id, operation, idempotency_key)`; there is no separate route dimension. `remove` is the archive operation, so the same key is independent in create and archive namespaces. Same-operation replay has no side effects; same-operation reuse with a different canonical fingerprint is `409 idempotency_conflict` with zero side effects. A receipt contains the operation, `applied` or `replayed`, safe holding record, new integer `portfolio_revision`, `snapshot_state`, current snapshot ID if any, actor witness, audit event ID, Activity event ID, and idempotency key. It contains no credential or raw request material.

`GET overview` supplies the witness as the exact strong ETag `"<portfolioId>-<canonical nonnegative revision>"`, for example `"portfolio-demo-7"`; weak tags, leading-zero revisions, wrong-portfolio tags, and duplicate/missing header values are invalid. A mutation must send that exact value in `If-Match`. The operation compares and increments the portfolio revision in the same SQLite transaction. Any mismatch is 409 `stale_revision` and has no side effects.

## 5. Snapshot semantics

A holding mutation never edits or replaces a snapshot. If a current immutable snapshot exists, the receipt reports `snapshot_state=stale` and its ID; if none exists, it reports `snapshot_state=no_snapshot` and a null ID. The overview/UI must show an explicit stale or no-snapshot state, the newly added lot as `unvalued`, and a removed lot as archived/hidden from the current active list. It must not show the prior snapshot as current and must not fabricate a portfolio total.

Snapshot input identity is derived from canonical database portfolio, holding, instrument, account, and holdings-revision state. It never depends on an external document identity or an imported-source hash.

Old snapshots and reviews retain their original holdings and values. A review whose snapshot predates a mutation remains historical; the existing lifecycle comparison marks it outdated once a later snapshot is produced. Theses and thesis history remain addressable for their holding ID, including after archival. A future valuation process produces a new snapshot; it is the only operation that can make the post-mutation overview current.

## 6. Atomic transaction and retry contract

The future store operation owns one `BEGIN IMMEDIATE` transaction. The HTTP layer performs authentication, body limits, canonical parsing, and authorization before entering it. Inside the transaction, the store:

1. verifies portfolio scope, active actor, and the exact revision witness;
2. checks the `(actor_category, actor_id, portfolio_id, operation, idempotency_key)` namespace;
3. replays the stored response for the same fingerprint, or returns 409 `idempotency_conflict` without exposing the stored request for a different fingerprint;
4. resolves an existing portfolio instrument or inserts one bounded inline instrument and its association, never both;
5. inserts one active lot, or updates exactly one active lot to `removed`;
6. increments `portfolios.holdings_revision` exactly once;
7. inserts one redacted append-only `audit_events` row and portfolio ownership row;
8. inserts one portfolio-scoped `activity_events` row linked to that audit event;
9. inserts one idempotency record containing the safe receipt and actor/permission witness; and
10. commits. Any error rolls back every preceding step, including an inline instrument, lot/archive state, revision, audit, Activity, and idempotency record.

The idempotency primary key is `(actor_category, actor_id, portfolio_id, operation, idempotency_key)`. The fingerprint covers the canonical method, canonical path, operation, portfolio/holding IDs, canonical body (or empty removal body), and revision witness; the route is fingerprinted but is not a physical namespace column. A same-operation key/body replay returns the same result. Same-operation reuse with any changed body, path, holding, or witness conflicts with zero writes. The same key in create and remove namespaces is independent. Idempotency rows and holding audit rows are append-only and cannot be edited or deleted. Two concurrent creates with distinct keys each commit one lot and one event after SQLite serializes their revision updates; the second caller must retry after receiving a stale witness if its witness was old. Two concurrent removes can archive a lot only once; the winner commits and the loser receives a stale revision or `holding_not_active`, never a second audit/Activity event.

## 7. Behavioral matrix

| Case | Expected result and side effects |
|---|---|
| Browser, valid session + Origin + CSRF + witness | Create 201 or remove 200; one receipt, lot change, revision, audit, Activity, and idempotency row. |
| Machine, exact bearer + active exact scope + both grants + witness | Same store result; no CSRF/Origin requirement; actor witness is machine. |
| Missing or malformed browser session | 401 `unauthorized`; no existence or scope disclosure; no writes. |
| Missing, malformed, duplicate, weak, or wrong bearer | 401 `unauthorized` with bearer challenge; no writes. |
| Absent/false/revoked persisted grant | 403 `forbidden` after identity is recognized; no writes. |
| Wrong configured or requested portfolio | 403 for authenticated machine scope failure, or 404 for an object not valid in the requested portfolio; no writes. |
| Missing, invalid, mismatched, or replayed browser CSRF | 403; no store access or writes. |
| Missing, multiple, null, or wrong Origin | 403; no store access or writes. |
| JSON unknown/duplicate field, malformed JSON, missing required field | 400 `invalid_request`; no writes. |
| Request body over 256 KiB on an endpoint that defines a body | 413; body is not persisted or logged. |
| Archive DELETE with no body | Valid request; DELETE has no application body and is exempt from the 256 KiB JSON mutation-body limit. |
| Archive DELETE with a non-empty body | Deterministic 400 `invalid_request`; body is not interpreted as a valid payload and DELETE does not advertise 413. Transport/server defensive limits remain separate. |
| Content-Type other than application/json | 415; no writes. |
| Non-canonical decimal/date, JSON number, exponent, negative quantity/cost, quantity zero, or inconsistent product | 400; no writes. |
| Existing valid instrument | Use only its portfolio association; cross-portfolio association is 404/invalid for this portfolio. |
| Inline instrument | Create global instrument plus portfolio association atomically; a conflict rolls back both. |
| Both existing reference and inline fields | 400; no writes. |
| Cross-portfolio account | 404 `not_found` and no existence detail; no writes. |
| Cross-portfolio instrument | 404 unless explicitly associated with requested portfolio; no writes. |
| Cross-portfolio holding | 404; no state or audit disclosure. |
| Missing portfolio/holding | 404; no writes. |
| Removed holding on a new removal key | 409 `holding_not_active`; no second events. |
| Same idempotency key and identical canonical request | Original status and byte-equivalent safe receipt; no new rows or revision. |
| Same idempotency key and changed request | 409 `idempotency_conflict`; no stored request disclosure or writes. |
| Stale `If-Match` witness | 409 `stale_revision`; no writes. |
| Two simultaneous creates | SQLite serialization plus revision witness prevents duplicate side effects; each committed request has one lot/event set. |
| Two simultaneous removes | One archive transition; the other gets stale/not-active conflict; one event set. |
| Failure after instrument, lot, revision, audit, Activity, or idempotency step | Transaction rollback removes all mutation artifacts and restores the prior revision. |
| Old snapshot/review/review target/thesis/history after create/remove | All remain immutable and addressable; only current active reads change. |
| Activity/audit read | Safe summary and actor witness only; no bearer, CSRF, session, secret, raw body, or sensitive account data. |
| Unsupported method or encoded/non-canonical path | 405 for known canonical path (with exact Allow) or route-level 404; never route normalization into a mutation. |

## 8. Contract migration design

`docs/holding-mutation-contract.sql` is an explicitly non-runtime contract artifact. It must be run in a scratch database after baseline SQL migrations 001-008, never copied under `internal/store/migrations` and never loaded by the application in this slice. Its assertions demonstrate target-state schema feasibility only; they are not numbered runtime migration/version evidence.

It is designed to be executable in one transaction and contains:

- `portfolios.holdings_revision INTEGER NOT NULL DEFAULT 0 CHECK (>=0)`, with a trigger that permits only a physical single-step increment. Raw SQLite file access is trusted and outside HTTP authorization; the shared transactional store operation is the sole supported application path that increments it, and failed or unauthorized HTTP requests never reach that update.
- `lots.lifecycle`, `removed_at`, `removed_by_category`, and `removed_by_id`, with insert/update guards allowing only active-to-removed archival;
- `machine_principals.permission_holdings_write INTEGER NOT NULL DEFAULT 0 CHECK (0/1)`;
- `portfolio_instruments(portfolio_id,instrument_id)` with one deterministic `MIN(created_at)` backfill row per portfolio/instrument, foreign keys, lookup indexes, and insert/update scope guards for lots;
- active/removal/instrument indexes on lots;
- `holding_mutation_idempotency` with actor category/id, portfolio scope, operation, key, SHA-256 fingerprint, lot, audit and Activity ownership, permission witness, safe response JSON, retention timestamps, foreign keys, and uniqueness constraints; and
- guards ensuring browser `local-owner` or an active machine principal owns a mutation, holding audit payload scope matches its portfolio, Activity points to the owned audit event, and archived lot fields/audit/idempotency rows remain immutable.

A future numbered migration/startup change must execute 001-008 compatibility checks first, preserve current production writes and fixtures, backfill existing rows, verify `PRAGMA foreign_key_check`, inspect `PRAGMA table_info`, `PRAGMA index_list`, `PRAGMA foreign_key_list`, and trigger definitions, then commit the schema. Migration failure rolls back its schema and version record. Rollback is by restoring the pre-migration backup/transaction, not by deleting immutable data or running a destructive down migration. Fresh initialization inserts `portfolio-default` exactly once with name `Portfolio`, reporting currency `MYR`, holdings revision `0`, and no holdings, instruments, accounts, or snapshot rows; an existing default portfolio is never renamed, emptied, or rejected, regardless of its current name or historical rows. New runtime writers must insert `portfolio_instruments` and use the revision/idempotency/event sequence described above; downstream valuation and read writers must derive identity from those canonical rows.

The executable scratch harness directly applies baseline SQL files 001-008 and this artifact against both a fresh database and an ordinary populated database. It seeds two portfolios, accounts, instruments, two same-portfolio lots sharing one instrument, a snapshot, review/review target, thesis/revision, audit, and Activity; it proves scratch empty-default feasibility, populated identity/name preservation, one association for duplicate lots, cross-portfolio account/instrument/holding rejection, unique idempotency, actor/grant guards, canonical removal metadata, foreign-key check, append-only historical rows, new-row comparison-key enforcement, machine-grant matrix behavior, and rollback after each simulated side-effect boundary. It also compares historical rows and payloads byte-for-byte before and after the artifact. The harness does not run `Store.Migrate`, change `schema_migrations`, invoke handlers, or claim authenticated empty-overview/runtime mutation evidence.

## 9. Phased implementation and acceptance ledger

### Shipped runtime acceptance

Migration 010 is loaded through `Store.Migrate`; runtime tests cover fresh initialization, authenticated empty overview, canonical browser/machine handlers, mutation authorization, idempotency, audit/Activity ownership, and transactional rollback. The scratch harness remains complementary evidence for schema feasibility and historical preservation.

### Backend persistence and API

1. Migration 010 and its physical schema guards are shipped and verified against existing fixtures.
2. Exact decimal/date canonical parsers and request-size/unknown-field validation are shipped.
3. One shared Store mutation operation handles browser and machine actors, revision witnesses, idempotency, inline instrument association, logical removal, audit ownership, Activity, redaction, and rollback.
4. Machine configuration and persisted-grant intersection for `holdings:write` are shipped; `reviews:write` remains independent and default false.
5. Canonical handlers, security middleware, no-store headers, ETag, status/error mapping, and route/method/path tests are shipped.
6. Runtime acceptance is recorded by the Go suite, focused concurrency tests, OpenAPI validation, scratch migration/foreign-key/rollback verification, and `git diff --check`.

### Frontend UI

1. Add authenticated form state for existing or inline instrument, exact decimal strings, account, and date; mutation attribution comes from the session/machine actor witness, not client provenance.
2. Obtain overview ETag and CSRF from the session bootstrap; send exact Origin, `If-Match`, and idempotency key; never use JavaScript Number for money/quantity.
3. Render validation, 401/403/404/409/413/415 states without leaking scope or credentials; allow retry with a refreshed overview witness.
4. Show unvalued additions, archived removals, stale/no-snapshot state, and a refresh-snapshot affordance; never present an old snapshot as current.
5. Acceptance: browser tests cover duplicate-submit/reload, stale tabs, CSRF/origin failures, inline/existing instrument branches, archive visibility, and historical links; UI remains unable to edit, sell, use an import surface, or access brokerages.

## 10. Implementation/removal ledger (named work items and proof obligations)

The following historical baseline/removal items document remaining cleanup around adjacent consumers. They are not an alternative to the shipped mutation handlers, and every item must retain the no-document-import invariant. Baseline paths named below are known conflicting debt, not acceptable architecture or an alternative implementation.

1. `internal/store/readmodel.go`: select only `lots.lifecycle='active'`, remove `source_row` from the read-model query/scan/domain mapping, and add a populated/archived overview regression proving removed lots are absent without changing historical rows.
2. `internal/httpserver/export.go`: export active lots only, remove the `source_row` column and renderer, and add CSV header/row regressions for duplicate instruments and archived lots.
3. `frontend/src/App.tsx` plus frontend types/UI: remove the `source_row` contract and render only active holdings; the authenticated holding create/archive controls are shipped in `frontend/src/HoldingMutations.tsx` with exact-value, receipt, retry, archive-confirmation, and refresh regressions. Add any remaining archived-lot and empty-overview fixture coverage when the baseline read-model cleanup lands.
4. Valuation, snapshot, and domain identity: remove source/import hashes and source-row identity; derive valuation input identity only from canonical portfolio, holding, instrument, account, and holdings-revision state; add snapshot identity and stale-after-mutation tests.
5. Config/auth/store persistence: add independent `holdings:write`, default false, persisted effective-grant intersection, redacted receipt/log handling, and machine-principal insert/update/upsert grammar checks; prove `reviews:write` never implies holding authority with config/auth/store tests.
6. `compose.yaml`, root `.env.example`, `deploy/private.env.example`, and private Compose/deployment docs: wire the new grant explicitly, keep its default false, redact values, and assert template byte-identity wherever templates are intended to match; add deployment/config tests and a no-secret grep check.
7. Importer CLI, `internal/importer`, import-only tests, import tables/columns/indexes, README/decision/deployment/operator instructions, and active source-note assumptions: remove the importer surface, `import_runs`/`import_items` dependencies, `source_row`/`source_ref`/`source_sha256` identity, and fixed imported-count assertions; add repository search and build/test regressions proving no active document-import path remains. Historical rows are preserved unless a separately approved destructive migration says otherwise.
8. Fresh initialization and authenticated empty overview: initialize `portfolio-default` as `Portfolio`/`MYR`/revision 0 with no holdings, instruments, accounts, or snapshot, preserve an ordinary populated default without rename/data loss, and add real migration plus authenticated read tests for both states.

Acceptance evidence for this ledger is named rather than implied: `go test ./...` for runtime/store/httpserver/domain/config tests, frontend unit tests for read/export/UI consumers, deployment-template tests for Compose/env/docs, importer package/CLI removal tests, strict OpenAPI/Redocly validation, the expanded scratch harness, and repository searches for prohibited document/source identity. The active contract above has no document import route, CLI, table, source identity, or fixed imported-count assumption.

## 11. Explicit non-goals and preserved behavior

This slice implements holding create/archive handlers, persistence runtime changes, migration loading, authentication, idempotency, revision ETags, and the authenticated browser controls in `frontend/src/HoldingMutations.tsx`. It does not add edit/update, sell transactions, brokerage connectivity, trade execution, price refresh, account creation, or the baseline-surface removals listed in the ledger; the API and UI do not imply brokerage/trading/quote-refresh authority.

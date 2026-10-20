# Portfolio holding mutation contract

Status: reviewed contract slice; no runtime handler, migration, or UI behavior is introduced by this change.

This contract supersedes the bootstrap-only interpretation of Portfolio.md. Import remains a bootstrap path. After import, an authorized browser session or an explicitly authorized Hermes machine may create a lot or logically archive a lot. Neither actor may access a brokerage, execute a trade, sell a position, edit an existing lot, refresh prices, create an account, or bulk import.

## 1. Canonical API surface

The canonical paths are:

- `POST /api/v1/portfolios/{portfolioId}/holdings` creates one active lot.
- `DELETE /api/v1/portfolios/{portfolioId}/holdings/{holdingId}` archives one active lot.
- `GET /api/v1/portfolios/{portfolioId}/overview` returns the current read model and the exact `ETag` holdings revision witness used by both mutations.

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

### Permission witness

A successful receipt and append-only audit witness record only actor category, actor ID, and effective permission names. A browser witness is `browser_user/local-owner/portfolio:read`; a machine witness is `machine/<principal-id>/portfolio:read,holdings:write`. The witness is captured at commit time and is not recomputed when configuration later changes.

## 3. Create request and exact values

`HoldingCreateRequest` is an object with no unknown fields. Required fields are `instrument`, `quantity`, `unit_cost`, `cost_basis`, `currency`, and `provenance`. Optional fields are `account_id` and `acquisition_date`.

`instrument` is exactly one of:

- `{ "instrument_id": "..." }`, where the instrument is present in `portfolio_instruments`; or
- an inline definition with bounded `symbol`, optional `description`, `currency`, optional `asset_type`, and optional `exchange`. The server creates the global instrument and the portfolio association in the same transaction.

Sending both forms, neither form, unknown instrument fields, or an inline definition that collides with an existing global identity is invalid. An instrument is not portfolio-valid merely because its ID exists in another portfolio. Existing global instruments become valid for this portfolio only through the association. Existing 001-008 lots are backfilled into that association.

`quantity` is a positive canonical decimal string with at most 18 fractional digits. `unit_cost` and `cost_basis` are non-negative canonical decimal strings with at most 18 fractional digits. Canonical strings have no plus sign, exponent, leading zero in the integer part, or trailing decimal point; fractional trailing zeroes are permitted and preserved as the declared source scale (so `10.00` is not rewritten to `10`). JSON numbers, `float64`, JavaScript `Number`, and SQLite REAL are forbidden. The server parses decimal text into an exact integer-plus-scale representation, verifies `quantity * unit_cost == cost_basis` by integer arithmetic after scale alignment, and stores the original canonical decimal strings. A product requiring more than the supported scale is rejected rather than silently rounded. `currency` is uppercase ISO-like three-letter text and must equal the instrument currency.

`acquisition_date` is either null/omitted or exactly `YYYY-MM-DD`; timestamps, timezone suffixes, invalid calendar dates, and non-canonical dates are rejected. `provenance.kind` is `manual`, `statement`, or `other`; its optional reference is a bounded safe label. Credentials, bearer values, CSRF values, account secrets, and raw request bodies are never accepted as provenance.

An account, when supplied, must exist and have the requested portfolio as owner. A null account is valid. No account is created by this operation.

## 4. Removal and response receipts

Removal is an archive state transition, never `DELETE FROM lots`. The lot remains addressable by its ID with `lifecycle=removed` and its original immutable quantity, unit cost, cost basis, currency, account, acquisition date, instrument, and provenance. `removed_at`, actor category, and actor ID are appended as removal metadata. A removed lot is excluded from default current holdings reads. Its old snapshots, review targets, reviews, theses and thesis revisions, Activity events, and audit records remain unchanged and addressable.

A successful create returns 201; a successful removal returns 200. A replay returns the exact stored receipt and original status/headers for the same actor, portfolio, route, operation, idempotency key, and request fingerprint. A receipt contains the operation, `applied` or `replayed`, safe holding record, new integer `portfolio_revision`, `snapshot_state`, current snapshot ID if any, actor witness, audit event ID, Activity event ID, and idempotency key. It contains no credential or raw request material.

`GET overview` supplies the witness as an opaque strong ETag, for example `"portfolio-demo-7"`. A mutation must send that exact value in `If-Match`. The operation compares and increments the portfolio revision in the same SQLite transaction. Any mismatch is 409 `stale_revision` and has no side effects.

## 5. Snapshot semantics

A holding mutation never edits or replaces a snapshot. If a current immutable snapshot exists, the receipt reports `snapshot_state=stale` and its ID; if none exists, it reports `snapshot_state=no_snapshot` and a null ID. The overview/UI must show an explicit stale or no-snapshot state, the newly added lot as `unvalued`, and a removed lot as archived/hidden from the current active list. It must not show the prior snapshot as current and must not fabricate a portfolio total.

Old snapshots and reviews retain their original holdings and values. A review whose snapshot predates a mutation remains historical; the existing lifecycle comparison marks it outdated once a later snapshot is produced. Theses and thesis history remain addressable for their holding ID, including after archival. A future valuation process produces a new snapshot; it is the only operation that can make the post-mutation overview current.

## 6. Atomic transaction and retry contract

The future store operation owns one `BEGIN IMMEDIATE` transaction. The HTTP layer performs authentication, body limits, canonical parsing, and authorization before entering it. Inside the transaction, the store:

1. verifies portfolio scope, active actor, and the exact revision witness;
2. checks the actor/portfolio/route/operation/idempotency key;
3. replays the stored response for the same fingerprint, or returns 409 `idempotency_conflict` without exposing the stored request for a different fingerprint;
4. resolves an existing portfolio instrument or inserts one bounded inline instrument and its association, never both;
5. inserts one active lot, or updates exactly one active lot to `removed`;
6. increments `portfolios.holdings_revision` exactly once;
7. inserts one redacted append-only `audit_events` row and portfolio ownership row;
8. inserts one portfolio-scoped `activity_events` row linked to that audit event;
9. inserts one idempotency record containing the safe receipt and actor/permission witness; and
10. commits. Any error rolls back every preceding step, including an inline instrument, lot/archive state, revision, audit, Activity, and idempotency record.

The idempotency primary key is `(actor_category, actor_id, portfolio_id, idempotency_key)`. The fingerprint covers the canonical method, canonical path, operation, portfolio/holding IDs, canonical body (or empty removal body), and revision witness. A key/body replay returns the same result. Reuse with any changed body, path, operation, holding, or witness conflicts. Idempotency rows and holding audit rows are append-only and cannot be edited or deleted. Two concurrent creates with distinct keys each commit one lot and one event after SQLite serializes their revision updates; the second caller must retry after receiving a stale witness if its witness was old. Two concurrent removes can archive a lot only once; the winner commits and the loser receives a stale revision or `holding_not_active`, never a second audit/Activity event.

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
| Body over 256 KiB | 413; body is not persisted or logged. |
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

`docs/holding-mutation-contract.sql` is an explicitly non-runtime contract artifact. It must be run in a scratch database after real migrations 001-008, never copied under `internal/store/migrations` and never loaded by the application in this slice.

It is designed to be executable in one transaction and contains:

- `portfolios.holdings_revision INTEGER NOT NULL DEFAULT 0 CHECK (>=0)`, with a trigger that permits only the mutation operation's single-step increment;
- `lots.lifecycle`, `removed_at`, `removed_by_category`, and `removed_by_id`, with insert/update guards allowing only active-to-removed archival;
- `machine_principals.permission_holdings_write INTEGER NOT NULL DEFAULT 0 CHECK (0/1)`;
- `portfolio_instruments(portfolio_id,instrument_id)` with backfill from existing lots, foreign keys, and lookup indexes;
- active/removal/instrument indexes on lots;
- `holding_mutation_idempotency` with actor category/id, portfolio scope, operation, key, SHA-256 fingerprint, lot, audit and Activity ownership, permission witness, safe response JSON, retention timestamps, foreign keys, and uniqueness constraints; and
- guards ensuring browser `local-owner` or an active machine principal owns a mutation, holding audit payload scope matches its portfolio, Activity points to the owned audit event, and archived lot fields/audit/idempotency rows remain immutable.

A future numbered migration must execute 001-008 compatibility checks first, preserve current production writes and fixtures, backfill existing rows, verify `PRAGMA foreign_key_check`, inspect `PRAGMA table_info`, `PRAGMA index_list`, `PRAGMA foreign_key_list`, and trigger definitions, then commit the schema. Migration failure rolls back its schema and version record. Rollback is by restoring the pre-migration backup/transaction, not by deleting immutable data or running a destructive down migration. New runtime writers must insert `portfolio_instruments` and use the revision/idempotency/event sequence described above; existing import and valuation writers continue to work with active-default lots and zero-default revisions.

The scratch verification must seed two portfolios, accounts, instruments, lots, a snapshot, review/review target, thesis/revision, audit, and Activity. It must prove: active default and removed-state checks; cross-portfolio account/instrument/holding rejection; unique idempotency; actor/grant guards; foreign-key check; append-only historical rows; and rollback after each side-effect boundary. It must also prove that removal leaves old snapshot/review/thesis rows and payloads byte-identical.

## 9. Phased implementation and acceptance ledger

### Backend persistence and API

1. Add the real numbered migration based on the contract artifact; verify the physical schema and fixture compatibility.
2. Add exact decimal/date canonical parsers and request-size/unknown-field validation.
3. Add one shared store mutation operation for browser and machine actors, with `BEGIN IMMEDIATE`, revision witness, idempotency, inline instrument association, logical removal, audit ownership, Activity, redaction, and rollback tests.
4. Add machine configuration and persisted-grant intersection for `holdings:write`; keep `reviews:write` independent and default false.
5. Add canonical handlers, security middleware, no-store headers, ETag, status/error mapping, and route/method/path tests.
6. Acceptance: all OpenAPI examples validate; every matrix row has a test; `go test ./...`, strict OpenAPI validation, scratch migration/foreign-key/rollback verification, and `git diff --check` pass; no runtime migration/handler/UI is part of this contract commit.

### Frontend UI

1. Add authenticated form state for existing or inline instrument, exact decimal strings, account, date, and provenance.
2. Obtain overview ETag and CSRF from the session bootstrap; send exact Origin, `If-Match`, and idempotency key; never use JavaScript Number for money/quantity.
3. Render validation, 401/403/404/409/413/415 states without leaking scope or credentials; allow retry with a refreshed overview witness.
4. Show unvalued additions, archived removals, stale/no-snapshot state, and a refresh-snapshot affordance; never present an old snapshot as current.
5. Acceptance: browser tests cover duplicate-submit/reload, stale tabs, CSRF/origin failures, inline/existing instrument branches, archive visibility, and historical links; UI remains unable to edit, sell, import, or access brokerages.

## 10. Explicit non-goals and preserved behavior

This slice does not implement handlers, persistence runtime changes, migration loading, frontend controls, edit/update, sell transactions, brokerage connectivity, trade execution, price refresh, account creation, or bulk import. Existing API routes and still-valid review, feedback, thesis, snapshot, export, and read scenarios remain unchanged except that the overview contract now advertises its machine-capable read alternative and revision ETag needed by the future mutation implementation.

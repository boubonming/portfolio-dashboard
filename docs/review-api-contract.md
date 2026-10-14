# Phase D scoped review API contract

Status: implementation contract for Phase D; no runtime routes, migrations, machine credentials, Hermes configuration, UI, or database writes are introduced by this change.

This record and `api/openapi.yaml` are one contract. The canonical resource namespace is `/api/v1/portfolios/{portfolioId}/...`; the older unscoped paths in the planning note are superseded by this portfolio-scoped form. All identifiers in this record are synthetic examples.

## 1. Boundary and non-goals

The portfolio application owns the ledger, immutable valuation snapshots, review storage, authentication boundary, and read model. Hermes remains the existing external environment and uses the configured local AI Server model by default. The portfolio container does not embed an agent runtime.

Hermes may:

- read one explicitly authorized portfolio's immutable snapshot and safe holding/instrument review context;
- submit an advisory review tied to one exact snapshot and calculation version; and
- read the resulting review.

Hermes may not edit portfolios, lots, holdings, instruments, quotes, FX, snapshots, theses, accounts, browser sessions, credentials, or trades. There are no trading or brokerage endpoints. Human feedback is a browser-only append-only event and never modifies a review or records a trade.

Every Phase D response is JSON except where an existing endpoint already defines another media type. Private responses set `Cache-Control: no-store`, `Vary: Cookie`, and `X-Content-Type-Options: nosniff`. JSON uses `application/json`; request bodies are UTF-8 JSON and reject unknown fields, duplicate object keys, trailing values, raw HTML, and bodies over 256 KiB for review/feedback writes. Text is returned as escaped JSON strings and is rendered as text, never as HTML.

## 2. Identities, permissions, and authentication

There are two independent identity classes:

1. Browser user: the existing single-user `portfolio_session` HttpOnly, Secure-in-production, SameSite=Strict cookie. It is short-lived, in-memory, revocable on logout, and paired with an in-memory CSRF token. It is used for UI reads and human feedback.
2. Machine principal: a fixed, explicitly configured Hermes principal using an opaque bearer credential. The credential is injected only into the Hermes environment/secret manager. It is never returned, logged, persisted to SQLite, included in a URL, placed in frontend assets, or stored in browser storage.

The machine principal has these least-privilege permissions:

- `portfolio:read`: latest/exact snapshots, safe holding review context, and review reads for the bound portfolio;
- `reviews:write`: advisory review submission only. This permission never implies any ledger, thesis, snapshot, account, browser, or trade write.

The machine credential is configured as one active token plus an optional next token during rotation. The application accepts the active token and, only during a bounded operator-controlled overlap, the next token. Rotation replaces the active secret, and revocation removes it and invalidates it immediately. Secrets are compared in constant time after a fixed-shape decode/normalization path; malformed and unknown tokens take the same unauthorized outcome and do not disclose principal existence. Authentication failures do not reveal whether a portfolio, token, or permission exists.

Startup fails closed if the machine principal is enabled without a secret, if a secret is shorter than 32 random bytes, if the principal has permissions outside the declared allow-list, if a portfolio binding is absent/invalid, or if the existing browser auth configuration is invalid. No insecure default, generated fallback, plaintext SQLite secret, or unauthenticated machine mode is allowed.

Auth precedence is deterministic:

1. Validate canonical method/path and request syntax enough to reject malformed requests.
2. Authenticate exactly one credential class. A valid browser cookie is used only for session operations; a bearer request is used only for machine operations. Supplying both is rejected with `401 unauthorized` rather than allowing credential confusion.
3. Check the principal's portfolio binding and permission. A valid identity without permission is `403 forbidden`.
4. Check resource ownership and cross-resource invariants. Missing or foreign resources return the documented not-found envelope without cross-portfolio existence disclosure.
5. For browser unsafe writes, validate same-origin Origin/Referer and `X-CSRF-Token` in constant time. Bearer requests do not bypass this rule for cookie-authenticated routes; feedback has no bearer alternative.

Missing, malformed, expired, revoked, or invalid credentials return `401` with `unauthorized`. A valid identity lacking permission, a wrong portfolio binding, invalid CSRF token, or rejected browser origin returns `403` with `forbidden`. A known path with a wrong method returns `405`, an `Allow` header, and JSON. An unknown or malformed API path returns `404 not_found` as JSON, without SPA redirects. A malformed body, query, path identifier, duplicate ID, or violated association returns `400 invalid_request`. The server does not use `404` to replace a missing credential.

The existing browser session login remains same-origin protected. Machine bearer authentication is not accepted by browser routes and never changes cookie-session CSRF semantics.

## 3. Canonical endpoints

All endpoints below are under `/api/v1/portfolios/{portfolioId}`. `portfolioId`, `snapshotId`, `holdingId`, `instrumentId`, and `reviewId` are 1–128 characters matching `^[A-Za-z0-9][A-Za-z0-9._~-]*$`.

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /snapshots/latest` | browser session or machine `portfolio:read` | Read latest committed snapshot, including explicit `no_snapshot`. |
| `GET /snapshots/{snapshotId}` | browser session or machine `portfolio:read` | Read one exact historical snapshot. Never reattaches to a newer snapshot. |
| `GET /holdings/{holdingId}/review-context?snapshot_id=...` | browser session or machine `portfolio:read` | Read only the selected lot, instrument identity, and safe snapshot valuation fields. |
| `GET /reviews` | browser session or machine `portfolio:read` | List reviews with optional holding/status filters. |
| `POST /reviews` | machine `portfolio:read` + `reviews:write` | Commit one immutable advisory review. |
| `GET /reviews/{reviewId}` | browser session or machine `portfolio:read` | Read one review and its current-snapshot lifecycle comparison. |
| `POST /reviews/{reviewId}/feedback` | browser session + CSRF | Append human agree/disagree/defer/request-another-review event. |

The OpenAPI file is authoritative for every field, bound, enum, example, security alternative, and response reference. Query keys are canonical and singular: `snapshot_id`, `holding_id`, `status`, `limit`, and `cursor`. Unknown or duplicate query keys are `400`. `limit` defaults to 20, is 1–50 for reviews, and the opaque signed cursor is 16–512 URL-safe characters. Review pages are ordered by `created_at DESC, review_id DESC`; cursors encode and authenticate that key plus portfolio ID and are unusable for another portfolio. A final page omits `next_cursor`; a non-final page includes it and `has_more: true`.

`POST /reviews` requires `Content-Type: application/json`, a 16–128 character `Idempotency-Key`, and a body no larger than 256 KiB. `POST /feedback` has the same idempotency-key and body limit plus the CSRF header. `Location` on a successful review submission is the canonical review URL. The exact errors are the shared `ErrorResponse` envelope:

```json
{"error":{"code":"invalid_request","message":"Request does not satisfy the API contract.","request_id":"req-demo-001"}}
```

`message` is safe, bounded, and never contains credentials, bearer values, account secrets, raw submitted text, SQL, or stack traces. `request_id` is safe to return and is the only correlation identifier exposed to the caller.

## 4. Snapshot and context semantics

A snapshot is immutable and identified by `(portfolio_id, snapshot_id)`. It includes `calculation_version`, `created_at`, `as_of`, explicit dependency quality arrays, and holding rows. Decimal quantities and monetary values are canonical base-10 strings, never JSON numbers, SQLite `REAL`, or binary floating point. `fresh`, `stale`, `missing`, `future`, and `invalid` are distinct freshness states.

`GET /snapshots/latest` returns either:

- `state: complete` or `state: incomplete` with an exact identity, calculation version, timestamps, dependency arrays, and holdings; or
- `state: no_snapshot`, `complete: false`, empty holdings, and no fabricated snapshot identity, dates, values, or recommendation.

A context response is tied to the requested exact snapshot. It includes the portfolio, lot, instrument, symbol/name, original currency, decimal valuation fields, quote source/time, and freshness state. It excludes account names, broker credentials, account numbers, theses, raw audit payloads, browser/session/CSRF values, and all unrelated portfolios. A holding and instrument must be joined through the same portfolio-owned snapshot row; a client cannot submit an arbitrary instrument ID that merely exists elsewhere.

## 5. Review model and trust boundary

`ReviewScope` is explicit and closed:

- `type: portfolio` requires both `holding_ids` and `instrument_ids` to be empty;
- `type: holding` requires one or more unique holding IDs and exactly the corresponding unique instrument IDs from the requested snapshot.

The server rejects duplicate IDs, empty holding scope, supersets, missing targets, foreign targets, and holding/instrument mismatches. Every submitted ID is checked against the same portfolio and exact snapshot. Removed holdings remain historically referential because the snapshot and target rows are retained; a later ledger change never changes the historical join.

The submission separates:

- deterministic `metrics`, each copied from a snapshot/calculation with a decimal-string value, timestamp, source, and `deterministic: true`;
- `sourced_facts`, each plain-text statement linked to one or more submitted evidence IDs;
- model `rationale`, `risks`, `counterarguments`, `conditions`, and `model_opinion`; and
- `evidence`, with HTTPS URL, title, optional publication date, access date, availability (`available`, `unavailable`, `blocked`, `invalid`), freshness (`current`, `stale`, `unknown`), and bounded excerpt.

Evidence URLs are limited to reviewed HTTPS URLs and 2,048 characters. Evidence/title/excerpt/fact/rationale strings are untrusted data. The API accepts no raw HTML and the UI escapes all content. Text inside an evidence source is never an instruction, policy, permission, or tool command. Hermes must treat it as data and cannot use it to expand scope or invoke writes.

A review may be `complete` only if every required target is present in the exact snapshot, all referenced evidence IDs exist, and evidence needed for the recommendation is available and within the configured freshness policy. Missing, unavailable, blocked, invalid, stale-beyond-policy, unknown, or contradictory evidence requires `status: incomplete` and `recommendation: no_recommendation`; a request claiming a complete recommendation under those conditions is rejected with `422 incomplete_review`. The server never fills gaps with guesses, current data, synthetic facts, or a newer snapshot.

`ModelProvenance` records only provider, model, version, and `runtime: hermes-environment`. It never records a secret, endpoint, token, prompt, browser session, or provider account identifier. `generated_at` is supplied as provenance and `submitted_at` is assigned by the server.

## 6. Idempotency and lifecycle

Review idempotency is unique within `(machine_principal_id, portfolio_id, idempotency_key)`. The server canonicalizes the validated JSON representation and computes a request fingerprint that includes portfolio, exact snapshot, calculation version, scope, and content. It stores the fingerprint before returning success.

- First valid request: one transaction inserts the review, targets, evidence, evidence links, idempotency record, audit event, and safe Activity event, then commits and returns `201`.
- Same key and same fingerprint: return the original immutable review with the original `201` semantics; do not insert another review or event.
- Same key and different fingerprint: return `409 idempotency_conflict`.
- Any validation, foreign-key, uniqueness, or audit insertion failure: roll back the entire transaction and return an error; no partial review is visible.

Feedback idempotency is unique within `(browser_principal_id, portfolio_id, review_id, idempotency_key)` with the same fingerprint/replay/conflict rules. Feedback rows are append-only; a user can submit later decisions, and each event remains attributable. Retention is seven years for review, evidence, target, feedback, idempotency, audit, and Activity records unless an approved privacy/redaction process removes only permitted free-text fields. Idempotency records are retained for the same period, so replay behavior remains deterministic.

A review is immutable after commit: no update, delete, target replacement, evidence replacement, status rewrite, or snapshot reassociation endpoint exists. On read, `lifecycle.outdated` is true exactly when a non-null `current_snapshot_id` differs from `review.snapshot_id`; `current_snapshot_id` is the latest current snapshot when one exists and is `null` when no current snapshot exists. The stored `historical_status`, original snapshot ID, calculation version, content, evidence, and targets never change. A missing current snapshot leaves the historical review intact and reports `outdated: false` with `current_snapshot_id: null` rather than silently changing the review.

## 7. Authorization and cross-resource invariants

The implementation must enforce these invariants inside the domain/store transaction, not only in HTTP handlers:

- every portfolio ID in URL, snapshot, target, review, evidence link, feedback, idempotency, audit, and Activity rows is the same portfolio;
- `snapshot_id` belongs to that portfolio and its calculation version equals the submitted value;
- each holding target belongs to the exact snapshot and its instrument target is the snapshot's instrument;
- portfolio-scope reviews have no target rows; holding-scope reviews have a non-empty complete target set;
- no machine principal can call thesis PUT, import, refresh, transaction, quote/FX write, snapshot write, account, browser, or trading code paths;
- historical references remain readable after a holding is removed from a later snapshot; and
- duplicate or superset target IDs are deterministic `400` errors, never silently deduplicated.

The portfolio container exposes no Hermes management endpoint. The Hermes adapter owns its bearer secret and submits only the declared review payload. No public tunnel, permissive CORS setting, browser token, or frontend environment variable is used.

## 8. Persistence proposal: canonical migration 004

The app is new and the existing deployed schema has no Phase D production data. The next migration is exactly `004_scoped_reviews.sql`; no compatibility bridge or hypothetical legacy migration is added. It runs transactionally after foreign keys are enabled. All timestamps are UTC RFC3339 text; all decimal values are `TEXT`.

Existing `portfolios`, `instruments`, `lots`, `snapshots`, `activity_events`, and `audit_events` are reused. The existing `snapshots` identity is `(id, portfolio_id)` logically; migration 004 adds a unique composite index if needed so child foreign keys cannot join a snapshot to another portfolio. The proposed new normalized entities are:

`machine_principals`

- `id TEXT PRIMARY KEY` (opaque internal principal ID)
- `name TEXT NOT NULL UNIQUE`, `portfolio_id TEXT NOT NULL REFERENCES portfolios(id)`
- `permission_portfolio_read INTEGER NOT NULL CHECK (permission_portfolio_read IN (0,1))`
- `permission_reviews_write INTEGER NOT NULL CHECK (permission_reviews_write IN (0,1))`
- `active INTEGER NOT NULL CHECK (active IN (0,1))`
- `created_at TEXT NOT NULL`, `rotated_at TEXT`, `revoked_at TEXT`

No token column exists. Secret material is environment/secret-manager state only. A deployment may keep a non-secret `credential_generation` for audit, but not a hash that creates a second secret store.

`review_snapshots` (a historical immutable association, not a copy of snapshot payload)

- `portfolio_id TEXT NOT NULL REFERENCES portfolios(id)`
- `snapshot_id TEXT NOT NULL`, `calculation_version TEXT NOT NULL`
- primary key `(portfolio_id, snapshot_id)`
- foreign key `(portfolio_id, snapshot_id) REFERENCES snapshots(portfolio_id, id)`

`snapshot_holdings` (the concrete immutable target index used to make review foreign keys possible)

- `portfolio_id TEXT NOT NULL`, `snapshot_id TEXT NOT NULL`, `holding_id TEXT NOT NULL`, `instrument_id TEXT NOT NULL`
- primary key `(portfolio_id, snapshot_id, holding_id)`
- unique `(portfolio_id, snapshot_id, holding_id, instrument_id)`
- foreign key `(portfolio_id, snapshot_id) REFERENCES review_snapshots(portfolio_id, snapshot_id)`
- foreign key `instrument_id REFERENCES instruments(id)`
- populated by the snapshot writer in the same transaction as the immutable snapshot association; no later update/delete is allowed

`advisory_reviews`

- `id TEXT PRIMARY KEY`, `portfolio_id TEXT NOT NULL`, `snapshot_id TEXT NOT NULL`, `calculation_version TEXT NOT NULL`
- `scope_type TEXT NOT NULL CHECK (scope_type IN ('portfolio','holding'))`
- `historical_status TEXT NOT NULL CHECK (historical_status IN ('complete','incomplete'))`
- `recommendation TEXT NOT NULL CHECK (recommendation IN ('hold','review','trim_candidate','exit_candidate','no_recommendation'))`
- `rationale TEXT NOT NULL`, `model_opinion TEXT NOT NULL`
- `model_provider TEXT NOT NULL`, `model_name TEXT NOT NULL`, `model_version TEXT NOT NULL`, `model_runtime TEXT NOT NULL CHECK (model_runtime='hermes-environment')`
- `generated_at TEXT NOT NULL`, `submitted_at TEXT NOT NULL`
- foreign key `(portfolio_id, snapshot_id) REFERENCES review_snapshots(portfolio_id, snapshot_id)`
- unique `(portfolio_id, id)` and a trigger rejecting UPDATE/DELETE

`review_risks`, `review_counterarguments`, and `review_conditions`

- `(review_id, ordinal)` primary key, `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`, `text TEXT NOT NULL`, `ordinal INTEGER NOT NULL CHECK (ordinal >= 0)`
- unique `(review_id, text, ordinal)` is unnecessary; input order is preserved by ordinal and duplicates are valid text only where explicitly allowed by the domain.

`review_targets`

- `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`, `portfolio_id TEXT NOT NULL`, `snapshot_id TEXT NOT NULL`
- `holding_id TEXT NOT NULL`, `instrument_id TEXT NOT NULL`, primary key `(review_id, holding_id)`
- foreign key `(portfolio_id, snapshot_id, holding_id, instrument_id) REFERENCES snapshot_holdings(portfolio_id, snapshot_id, holding_id, instrument_id)`
- unique `(review_id, instrument_id, holding_id)`

`review_metrics`

- `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`, `ordinal INTEGER NOT NULL`, `name TEXT NOT NULL`, `value TEXT NOT NULL`, `unit TEXT NOT NULL`, `as_of TEXT NOT NULL`, `source TEXT NOT NULL CHECK (source IN ('snapshot','calculation','submitted'))`, `deterministic INTEGER NOT NULL CHECK (deterministic=1)`
- primary key `(review_id, ordinal)`

`review_evidence`

- `id TEXT PRIMARY KEY`, `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`, `ordinal INTEGER NOT NULL`
- `url TEXT NOT NULL`, `title TEXT NOT NULL`, `published_at TEXT`, `accessed_at TEXT NOT NULL`
- `availability TEXT NOT NULL CHECK (availability IN ('available','unavailable','blocked','invalid'))`
- `freshness TEXT NOT NULL CHECK (freshness IN ('current','stale','unknown'))`, `excerpt TEXT NOT NULL`
- unique `(review_id, id)`, primary key/unique `(review_id, ordinal)` for deterministic output

`review_sourced_facts`, `review_fact_evidence`

- fact table: `(review_id, ordinal)` primary key, `statement TEXT NOT NULL`
- link table: `(review_id, fact_ordinal, evidence_id)` primary key, both foreign keys scoped to the same review; evidence IDs must resolve within that review

`review_idempotency`

- `principal_id TEXT NOT NULL REFERENCES machine_principals(id)`, `portfolio_id TEXT NOT NULL REFERENCES portfolios(id)`, `idempotency_key TEXT NOT NULL`, `request_fingerprint TEXT NOT NULL`, `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`, `created_at TEXT NOT NULL`, `expires_at TEXT NOT NULL`
- primary key `(principal_id, portfolio_id, idempotency_key)` and unique `(portfolio_id, review_id)`

`review_feedback`

- `id TEXT PRIMARY KEY`, `portfolio_id TEXT NOT NULL REFERENCES portfolios(id)`, `review_id TEXT NOT NULL REFERENCES advisory_reviews(id)`
- `decision TEXT NOT NULL CHECK (decision IN ('agree','disagree','defer','request_another_review'))`, `note TEXT NOT NULL`, `principal_id TEXT NOT NULL`, `occurred_at TEXT NOT NULL`
- foreign key `(portfolio_id, review_id) REFERENCES advisory_reviews(portfolio_id, id)`
- append-only trigger rejecting UPDATE/DELETE

`feedback_idempotency`

- `principal_id TEXT NOT NULL`, `portfolio_id TEXT NOT NULL`, `review_id TEXT NOT NULL`, `idempotency_key TEXT NOT NULL`, `request_fingerprint TEXT NOT NULL`, `feedback_id TEXT NOT NULL REFERENCES review_feedback(id)`, `created_at TEXT NOT NULL`, `expires_at TEXT NOT NULL`
- primary key `(principal_id, portfolio_id, review_id, idempotency_key)`

Indexes:

- reviews by `(portfolio_id, submitted_at DESC, id DESC)`;
- reviews by `(portfolio_id, snapshot_id, submitted_at DESC)`;
- targets by `(portfolio_id, holding_id, submitted_at DESC)` through the review join;
- evidence and fact links by `(review_id, ordinal)`;
- feedback by `(portfolio_id, review_id, occurred_at DESC, id DESC)`;
- idempotency primary keys and review foreign keys as above.

The migration must enable and verify SQLite foreign keys, create all tables/indexes/triggers in one migration transaction, and fail without partial schema. Review submit commits all review-owned rows, idempotency, audit, and Activity attribution in one transaction. The audit payload contains safe action/entity/principal category and IDs only; it redacts bearer values, cookies, CSRF tokens, request bodies, evidence excerpts, account data, and provider secrets. Activity exposes only the safe summary (`hermes review submitted` or `human review feedback recorded`) and never raw model/evidence text.

## 9. Ordered implementation pipeline

1. Persistence/domain vertical slice: implement migration 004, composite ownership checks, immutable review/evidence/target/feedback tables, decimal-string validation, and transaction tests. Depends on current snapshot/store/activity schemas. Produces the storage/domain contract and rollback/idempotency behavior.
2. Machine auth vertical slice: add environment-only principal configuration, constant-time bearer validation, permission/portfolio binding, startup fail-closed checks, rotation/revocation, and negative tests proving no thesis/holding/trade path is reachable. Depends on existing browser auth. Produces the machine identity contract.
3. HTTP review API vertical slice: implement exact snapshot/context/review/feedback routes, strict JSON parsing, body/query/cursor bounds, error envelopes, no-store/Vary/nosniff headers, CSRF boundaries, and `httptest` isolation/idempotency tests. Depends on slices 1–2. Produces the OpenAPI operation contract.
4. Frontend Reviews UI/feedback slice: add typed client models, escaped review/evidence rendering, current/outdated/incomplete states, paginated review queue, and CSRF-protected human feedback. Depends on slice 3. Produces browser behavior without machine credentials.
5. Hermes adapter slice: integrate the existing Hermes environment only after its documented capabilities are inspected; fetch one portfolio-scoped exact snapshot/context, preserve untrusted evidence boundaries, and submit with a machine bearer secret held outside the portfolio container. Depends on slices 1–3 and does not add an agent runtime to the application.
6. Real dated-snapshot end-to-end verification: use a real dated immutable snapshot in an authorized isolated environment, submit once, replay the same key, read the review through the API/UI, create a newer snapshot, verify historical values remain unchanged and `outdated: true`, and prove a machine identity cannot edit holdings/theses or trade. Depends on all prior slices and explicit approval for the test environment; no real provider credentials or trades.

Each slice must use focused failing tests before implementation, then the relevant Go/frontend regression suite. This contract task intentionally leaves all pipeline tasks unchecked and introduces no runtime behavior.

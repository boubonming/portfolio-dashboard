# Phase D scoped review API contract

Status: implementation contract only. This change modifies no handlers, migrations, UI, credentials, deployment, or database. `api/openapi.yaml` and this record are one contract; the portfolio-scoped namespace is `/api/v1/portfolios/{portfolioId}`.

## 1. Boundary and non-goals

Phase D exposes immutable snapshot/context reads, machine-submitted advisory reviews, review reads, and browser-only append-only feedback. The machine principal cannot mutate holdings, lots, instruments, quotes, FX, theses, snapshots, accounts, browser sessions, credentials, or trades. Feedback never edits a review and never records a trade. There is no Hermes management endpoint and the application does not embed an agent runtime.

All private JSON success and error responses carry exactly these security headers: `Cache-Control: no-store`, `Vary: Cookie`, and `X-Content-Type-Options: nosniff`. Review and feedback writes accept only UTF-8 JSON, reject unknown fields, duplicate keys, trailing values, raw HTML, and bodies larger than 256 KiB. All strings are bounded by the OpenAPI schemas and rendered as escaped text.

The canonical next migration is exactly `008_scoped_reviews.sql`. Migrations `001_initial.sql` through `007_thesis.sql` already exist, including `004_fx_observation_constraints.sql`; this contract does not add that migration.

## 2. Identity, authentication, and authorization

There are two credential classes:

- Browser: the existing HttpOnly, Secure-in-production, SameSite=Strict `portfolio_session` cookie and its in-memory CSRF token. The cookie contains a random short-lived session identifier; it is revocable on logout.
- Machine: one fixed, explicitly configured Hermes principal with an opaque bearer secret held only in the Hermes environment/secret manager. The secret is never logged, returned, put in a URL/frontend asset/browser store, or persisted in SQLite. The configured principal is bound to one portfolio and may have only `portfolio:read` and, for POST review, `reviews:write`.

A stable logical browser principal is persisted separately from sessions:

- `browser_principals.id` is the non-secret single-user ID `local-owner`, seeded once.
- Every valid browser session resolves to this ID; it is not the random session ID and is never a credential.
- It is used only for feedback attribution and feedback idempotency. No password, cookie, CSRF token, session secret, or credential hash is stored in this relation.

For every Phase D route the server first performs only generic request-target and canonical-encoding safety checks (including rejection of malformed percent encoding, path traversal, duplicate/unknown query keys, and invalid identifiers). It then authenticates before revealing whether a protected resource path or method exists:

1. Reject missing, malformed, expired, revoked, or invalid credentials with uniform `401` / `unauthorized`.
2. Reject a request carrying both cookie and bearer credentials with the same `401` / `unauthorized`; never choose one credential class.
3. For a valid identity, check the portfolio binding and permission; failure is `403` / `forbidden`.
4. Only after authentication and authorization resolve ownership and the resource. A missing or foreign resource is `404` / `not_found`; a known route with a wrong method is `405` / `method_not_allowed` with its exact `Allow` value.
5. Browser unsafe writes require same-origin `Origin`/`Referer` validation and a constant-time CSRF-token match. Feedback is browser-only: bearer-only feedback is rejected, and machine authentication never weakens any cookie write or CSRF rule.

The bearer-capable unauthorized response deterministically offers `WWW-Authenticate: Bearer realm="portfolio-machine"`; a cookie-session unauthorized response omits that challenge so browser users are not instructed to use bearer credentials. Both-credential rejection remains a runtime negative-test obligation because OpenAPI security alternatives cannot express mutual exclusion.

Required negative tests cover both credentials, missing/invalid bearer and cookie values, malformed and encoded paths, unknown and duplicate query/header/body fields, wrong methods and exact `Allow`, wrong portfolio, missing permission, bearer-only feedback, bad Origin/Referer, bad CSRF token, and machine attempts to reach every prohibited mutation path.

## 3. Canonical endpoints and response matrix

| Method and path | Authentication | Permission/purpose |
|---|---|---|
| `GET /snapshots/latest` | browser or machine | Exact portfolio latest snapshot, including explicit `no_snapshot`. |
| `GET /snapshots/{snapshotId}` | browser or machine | Exact historical immutable snapshot. |
| `GET /holdings/{holdingId}/review-context?snapshot_id=...` | browser or machine | One snapshot-owned holding/instrument context. |
| `GET /reviews` | browser or machine | List reviews with `holding_id`, `status`, optional `outdated`, `limit`, and signed `cursor`. |
| `POST /reviews` | machine with both permissions | Append one immutable review; atomic audit/Activity/idempotency. |
| `GET /reviews/{reviewId}` | browser or machine | Historical review plus lifecycle comparison. |
| `POST /reviews/{reviewId}/feedback` | browser cookie + CSRF only | Append attributable feedback; atomic audit/Activity/idempotency. |

All Phase D operations declare applicable `400 invalid_request`, `401 unauthorized`, `403 forbidden`, `404 not_found`, `405 method_not_allowed`, `409 idempotency_conflict` where a write key is present, `413 payload_too_large` for writes, `415 unsupported_media_type` for writes, `422 incomplete_review` for review submission completeness, and `500 internal_error` responses in OpenAPI. Every declared error has the three private headers. `405` always includes exact canonical `Allow`, and it is emitted only after authentication/authorization. POST review `201` creation and same-key replay include the canonical `Location: /api/v1/portfolios/{portfolioId}/reviews/{reviewId}`. Error codes are closed per response; there is no arbitrary `ErrorResponse.error.code` string.

`GET /reviews` uses singular canonical query keys. Unknown/duplicate keys and malformed cursors are `400`. `status` filters only stored `complete|incomplete`; `outdated` is a separate optional boolean lifecycle filter and does not enter the stored status enum. The cursor is signed over portfolio ID, the complete filter set (`holding_id`, `status`, `outdated`), `submitted_at`, and the database `id`. A cursor for another portfolio or filter set is rejected. Wire order is `submitted_at DESC, review_id DESC`; wire `review_id` is the database `advisory_reviews.id`, and the supporting index is `(portfolio_id, submitted_at DESC, id DESC)`.

A final page is `{items, has_more:false}` and omits `next_cursor`. A non-final page is `{items, has_more:true, next_cursor}` with a 16–512 character signed cursor. These are explicit OpenAPI `oneOf` branches.

## 4. Snapshot, scope, and review semantics

Snapshots are immutable and joined by `(portfolio_id, snapshot_id)`. `calculation_version`, decimal values, and timestamps are copied exactly; decimal values are canonical base-10 strings, never JSON numbers or SQLite `REAL`.

Snapshot response branches are explicit: `state: complete` requires `complete:true`; `state: incomplete` requires `complete:false`; `state: no_snapshot` requires `complete:false`, empty holdings, and no fabricated snapshot ID, calculation version, timestamps, or values.

`ReviewScope` is a closed `oneOf`:

- `type: portfolio` requires both `holding_ids: []` and `instrument_ids: []`.
- `type: holding` requires non-empty, unique arrays for both fields. The server additionally requires exact 1:1 positional/corresponding membership in the requested snapshot and rejects duplicates, empty scope, supersets, foreign targets, and holding/instrument mismatches.

A complete review branch requires `status: complete`, a recommendation other than `no_recommendation`, at least one `available`/`current` HTTPS evidence item, and at least one sourced fact referencing submitted evidence. An incomplete review requires `status: incomplete` and `recommendation: no_recommendation`; it may describe stale, unknown, unavailable, blocked, invalid, or contradictory evidence. The server still enforces freshness policy, target membership, contradiction checks, calculation version, and every sourced-fact evidence reference. An invalid completeness claim is `422 incomplete_review`.

Evidence URLs are HTTPS-only and bounded to 2,048 characters. Evidence, facts, rationale, risks, counterarguments, and conditions are untrusted text and never instructions, policy, permission, or tool commands. Model provenance contains provider/model/version and the fixed `hermes-environment` runtime only; it contains no prompt, endpoint, token, session, or account secret.

## 5. Exact idempotency, immutability, and feedback note

Review and feedback writes are append-only and atomic with their owned rows, idempotency row, audit event, and safe Activity row. The transaction rolls back completely on any validation, foreign-key, uniqueness, audit, or Activity failure.

The server canonicalizes the validated JSON before fingerprinting. Omitted optional values and explicit JSON `null` have the same canonical representation where the field is nullable. For feedback, omitted `note` and `"note": null` both canonicalize to `note:null`; the response always contains `note:null` in that case; a present string is 1–2,000 characters. The proposed SQLite `review_feedback.note` column is nullable `TEXT`.

Same key plus the same canonical request returns the original immutable result with original `201` semantics and no duplicate rows. Same key plus a different request returns `409 idempotency_conflict`. Both idempotency tables have a non-null `retain_until` at least seven years after their operation and remain reserved indefinitely: this current contract has no purge/delete endpoint or background purge, and append-only triggers reject deletion, so keys are never reused. `retain_until` is only the earliest eligibility timestamp for a future separately reviewed and versioned retention migration; it does not authorize deletion now. Any future purge must atomically remove the complete review/feedback graph (including audit association and Activity) and replace or disable the append-only triggers in that new migration, outside Phase D. Replay is therefore always available for the lifetime of this contract. Review content, targets, evidence, status, and snapshot association have no update/delete route and are protected by append-only triggers.

## 6. SQLite persistence proposal for `008_scoped_reviews.sql`

The migration runs transactionally after `PRAGMA foreign_keys=ON` and is the only proposed database change. `008_scoped_reviews.sql` first adds nullable `retain_until` to the existing `audit_events` table, creates the normalized parent keys, and then creates the review graph. It rebuilds `activity_events` only after the legacy association backfill described below. All timestamps are canonical UTC RFC3339 text (`YYYY-MM-DDTHH:MM:SS.ffffff000Z`), validated by the exact length/GLOB/date/time checks used by the existing Activity migration; all IDs and bounded text use `TEXT` with the stated length/check constraints. The exact timestamp check is `length(ts)=30 AND ts GLOB '????-??-??T??:??:??.?????????Z' AND substr(ts,20,1)='.' AND substr(ts,12,2) BETWEEN '00' AND '23' AND substr(ts,15,2) BETWEEN '00' AND '59' AND substr(ts,18,2) BETWEEN '00' AND '59'`; migration 008 expands it for every generated/review timestamp column, while legacy generic audit timestamps are not retroactively rewritten. Decimal values are canonical decimal strings and are never stored as `REAL`, `INTEGER` approximations, or JSON numbers.

The normalized tables below are the sole immutable source of truth for a committed `Review` and its same-key replay. No response field may be recovered from `snapshots.payload`, `audit_events.payload`, an unstated JSON blob, or an external store. The parent row stores scalar wire fields; each child table stores one ordered array and its ordinal. Every review-owned row carries the same `retain_until` boundary as its parent review or feedback operation. Existing generic audit/Activity rows are not retroactively asserted to have a boundary: their new nullable column remains `NULL` until a future reviewed migration assigns one.

Before the normalized tables, migration 008 creates the SQLite parent keys required by composite foreign keys: `CREATE UNIQUE INDEX snapshots_portfolio_id_id ON snapshots(portfolio_id, id)` and the explicitly ordered unique keys shown below. These are valid against the existing `snapshots(id PRIMARY KEY, portfolio_id, ...)` table and make the parent-column order explicit.

The following definitions are the structural contract (the length bounds and checks are normative; implementations may add stricter checks without weakening them):

```sql
ALTER TABLE audit_events ADD COLUMN retain_until TEXT NULL;
CREATE UNIQUE INDEX snapshots_portfolio_id_id ON snapshots(portfolio_id, id);

CREATE TABLE browser_principals (
  id TEXT PRIMARY KEY CHECK (id = 'local-owner'),
  kind TEXT NOT NULL CHECK (kind = 'single_user'),
  created_at TEXT NOT NULL
);
INSERT INTO browser_principals(id, kind, created_at) VALUES ('local-owner','single_user',strftime('%Y-%m-%dT%H:%M:%fZ','now'));

CREATE TABLE machine_principals (
  id TEXT PRIMARY KEY, portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
  permission_portfolio_read INTEGER NOT NULL CHECK(permission_portfolio_read IN (0,1)),
  permission_reviews_write INTEGER NOT NULL CHECK(permission_reviews_write IN (0,1)),
  active INTEGER NOT NULL CHECK(active IN (0,1)), created_at TEXT NOT NULL,
  UNIQUE(id, portfolio_id)
);

CREATE TABLE review_snapshots (
  portfolio_id TEXT NOT NULL REFERENCES portfolios(id), snapshot_id TEXT NOT NULL,
  calculation_version TEXT NOT NULL, PRIMARY KEY(portfolio_id, snapshot_id),
  UNIQUE(portfolio_id, snapshot_id, calculation_version),
  FOREIGN KEY(portfolio_id, snapshot_id) REFERENCES snapshots(portfolio_id, id)
);

CREATE TABLE snapshot_holdings (
  portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL,
  PRIMARY KEY(portfolio_id, snapshot_id, holding_id),
  UNIQUE(portfolio_id, snapshot_id, holding_id, instrument_id),
  FOREIGN KEY(portfolio_id, snapshot_id) REFERENCES review_snapshots(portfolio_id, snapshot_id),
  FOREIGN KEY(instrument_id) REFERENCES instruments(id)
);

CREATE TABLE advisory_reviews (
  id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128),
  portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
  snapshot_id TEXT NOT NULL CHECK(length(snapshot_id) BETWEEN 1 AND 128),
  calculation_version TEXT NOT NULL CHECK(length(calculation_version) BETWEEN 1 AND 128),
  scope_type TEXT NOT NULL CHECK(scope_type IN ('portfolio','holding')),
  historical_status TEXT NOT NULL CHECK(historical_status IN ('complete','incomplete')),
  recommendation TEXT NOT NULL CHECK(recommendation IN ('hold','review','trim_candidate','exit_candidate','no_recommendation')),
  rationale TEXT NOT NULL CHECK(length(rationale) BETWEEN 1 AND 8000),
  model_opinion TEXT NOT NULL CHECK(length(model_opinion) BETWEEN 1 AND 8000),
  model_provider TEXT NOT NULL CHECK(length(model_provider) BETWEEN 1 AND 128),
  model_name TEXT NOT NULL CHECK(length(model_name) BETWEEN 1 AND 256),
  model_version TEXT NOT NULL CHECK(length(model_version) BETWEEN 1 AND 128),
  model_runtime TEXT NOT NULL CHECK(model_runtime = 'hermes-environment'),
  generated_at TEXT NOT NULL,
  submitted_at TEXT NOT NULL,
  submitted_by_category TEXT NOT NULL CHECK(submitted_by_category = 'machine'),
  submitted_by_id TEXT NOT NULL CHECK(length(submitted_by_id) BETWEEN 1 AND 128),
  retain_until TEXT NOT NULL,
  UNIQUE(id, portfolio_id), UNIQUE(portfolio_id, id),
  UNIQUE(id, portfolio_id, snapshot_id),
  UNIQUE(id, portfolio_id, snapshot_id, calculation_version),
  FOREIGN KEY(portfolio_id, snapshot_id, calculation_version)
    REFERENCES review_snapshots(portfolio_id, snapshot_id, calculation_version),
  CHECK(generated_at GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(submitted_at GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(retain_until GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(julianday(retain_until) >= julianday(submitted_at, '+7 years'))
);

CREATE TABLE review_targets (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, calculation_version TEXT NOT NULL,
  holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 32),
  retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, holding_id), UNIQUE(review_id, ordinal),
  FOREIGN KEY(review_id, portfolio_id, snapshot_id) REFERENCES advisory_reviews(id, portfolio_id, snapshot_id),
  FOREIGN KEY(portfolio_id, snapshot_id, holding_id, instrument_id)
    REFERENCES snapshot_holdings(portfolio_id, snapshot_id, holding_id, instrument_id),
  FOREIGN KEY(review_id, portfolio_id, snapshot_id, calculation_version)
    REFERENCES advisory_reviews(id, portfolio_id, snapshot_id, calculation_version)
);

CREATE TABLE review_risks (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 32),
  text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, ordinal), FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_counterarguments (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 32),
  text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, ordinal), FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_conditions (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 32),
  text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, ordinal), FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_metrics (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 100),
  name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128),
  value TEXT NOT NULL CHECK(length(value) BETWEEN 1 AND 128 AND value NOT GLOB '*[^0-9.+-]*'),
  unit TEXT NOT NULL CHECK(length(unit) BETWEEN 1 AND 64), as_of TEXT NOT NULL,
  source TEXT NOT NULL CHECK(source IN ('snapshot','calculation','submitted')),
  deterministic INTEGER NOT NULL CHECK(deterministic = 1), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, ordinal), FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_evidence (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, evidence_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 64),
  url TEXT NOT NULL CHECK(length(url) BETWEEN 1 AND 2048 AND url GLOB 'https://*'),
  title TEXT NOT NULL CHECK(length(title) BETWEEN 1 AND 512),
  published_at TEXT NULL, accessed_at TEXT NOT NULL,
  availability TEXT NOT NULL CHECK(availability IN ('available','unavailable','blocked','invalid')),
  freshness TEXT NOT NULL CHECK(freshness IN ('current','stale','unknown')),
  excerpt TEXT NOT NULL CHECK(length(excerpt) BETWEEN 1 AND 4000), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, evidence_id), UNIQUE(review_id, ordinal), UNIQUE(review_id, portfolio_id, evidence_id),
  FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_sourced_facts (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL CHECK(fact_ordinal >= 0 AND fact_ordinal < 32),
  statement TEXT NOT NULL CHECK(length(statement) BETWEEN 1 AND 2000), retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, fact_ordinal), UNIQUE(review_id, portfolio_id, fact_ordinal),
  FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);
CREATE TABLE review_fact_evidence (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL, evidence_id TEXT NOT NULL,
  evidence_ordinal INTEGER NOT NULL CHECK(evidence_ordinal >= 0 AND evidence_ordinal < 16),
  retain_until TEXT NOT NULL,
  PRIMARY KEY(review_id, fact_ordinal, evidence_id), UNIQUE(review_id, fact_ordinal, evidence_ordinal),
  FOREIGN KEY(review_id, portfolio_id, fact_ordinal) REFERENCES review_sourced_facts(review_id, portfolio_id, fact_ordinal),
  FOREIGN KEY(review_id, portfolio_id, evidence_id) REFERENCES review_evidence(review_id, portfolio_id, evidence_id)
);
CREATE TABLE review_idempotency (
  principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL, review_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  PRIMARY KEY(principal_id, portfolio_id, idempotency_key), UNIQUE(portfolio_id, review_id),
  FOREIGN KEY(principal_id, portfolio_id) REFERENCES machine_principals(id, portfolio_id),
  FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id),
  CHECK(created_at GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(retain_until GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(julianday(retain_until) >= julianday(created_at, '+7 years'))
);

CREATE TABLE review_feedback (
  id TEXT PRIMARY KEY, browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL,
  decision TEXT NOT NULL CHECK(decision IN ('agree','disagree','defer','request_another_review')),
  note TEXT NULL, occurred_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  UNIQUE(id, browser_principal_id, portfolio_id, review_id),
  FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id),
  FOREIGN KEY(portfolio_id, review_id) REFERENCES advisory_reviews(portfolio_id, id),
  CHECK(occurred_at GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(retain_until GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(julianday(retain_until) >= julianday(occurred_at, '+7 years'))
);
CREATE TABLE feedback_idempotency (
  browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL, feedback_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  PRIMARY KEY(browser_principal_id, portfolio_id, review_id, idempotency_key),
  FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id),
  FOREIGN KEY(portfolio_id, review_id) REFERENCES advisory_reviews(portfolio_id, id),
  FOREIGN KEY(feedback_id, browser_principal_id, portfolio_id, review_id) REFERENCES review_feedback(id, browser_principal_id, portfolio_id, review_id),
  CHECK(created_at GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(retain_until GLOB '????-??-??T??:??:??.?????????Z'),
  CHECK(julianday(retain_until) >= julianday(created_at, '+7 years'))
);

CREATE TABLE audit_event_portfolios (
  audit_event_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, retain_until TEXT NULL,
  PRIMARY KEY(audit_event_id, portfolio_id),
  FOREIGN KEY(audit_event_id) REFERENCES audit_events(id),
  FOREIGN KEY(portfolio_id) REFERENCES portfolios(id)
);
```

The migration rebuilds `activity_events` while preserving its current columns and checks. The replacement shape is:

```sql
CREATE TABLE activity_events_new (
  id TEXT PRIMARY KEY,
  portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
  actor_category TEXT NOT NULL, action TEXT NOT NULL, category TEXT NOT NULL,
  entity_type TEXT, entity_id TEXT, summary TEXT NOT NULL, details TEXT NOT NULL DEFAULT '',
  occurred_at TEXT NOT NULL,
  sort_time TEXT NOT NULL CHECK(sort_time = occurred_at),
  source_audit_event_id TEXT NULL,
  retain_until TEXT NULL,
  FOREIGN KEY(source_audit_event_id, portfolio_id)
    REFERENCES audit_event_portfolios(audit_event_id, portfolio_id)
);
```

The composite FK is intentionally nullable. SQLite accepts a legacy row when `source_audit_event_id IS NULL`; when it is non-null, the `(audit_event_id, portfolio_id)` association is mandatory and enforces scoped ownership. The migration order is exactly: `BEGIN IMMEDIATE`; add nullable `audit_events.retain_until`; create `audit_event_portfolios`; insert associations only for existing Activity rows with non-null `source_audit_event_id` (`SELECT DISTINCT source_audit_event_id, portfolio_id ... WHERE source_audit_event_id IS NOT NULL`), copying the audit row's nullable boundary; create `activity_events_new`; copy every legacy row unchanged, including NULL source and NULL retention; drop old indexes/triggers/table; rename the new table; recreate `idx_activity_source_portfolio` and `idx_activity_portfolio_order`; recreate the existing append-only update/delete triggers; add the Phase-D-only triggers below; run `PRAGMA foreign_key_check`; and `COMMIT`. Any failure rolls back the entire transaction, including the `audit_event_portfolios` backfill. No synthetic audit event or synthetic source ID is created.

New Phase D audit events are inserted only after their review/feedback owner exists. Phase-D action/category values are `review_submitted`/`review` and `feedback_submitted`/`feedback`. An INSERT trigger rejects a missing `retain_until`, requires it to equal the owning `advisory_reviews.retain_until` or `review_feedback.retain_until`, and permits only the safe top-level JSON payload keys `id`, `action`, `principal_category`, `principal_id`, `portfolio_id`, `review_id`, `snapshot_id`, `status`, `occurred_at`, `submitted_at`, and `created_at`. The trigger rejects invalid/non-object JSON, unknown keys, nested objects/arrays, bearer/cookie/CSRF values, request bodies, rationale, model opinion, evidence text/URLs/excerpts, account data, prompts, provider endpoints, and secrets. Generic legacy and non-Phase-D audit rows retain their existing behavior and may have `retain_until IS NULL`.

Phase-D Activity INSERT triggers apply only when `category = 'review'` or `category = 'feedback'` (or the corresponding Phase-D action). They reject NULL `source_audit_event_id`, NULL `retain_until`, absent or cross-portfolio `audit_event_portfolios`, and any retention mismatch with the source audit event and its review/feedback owner. Legacy/generic Activity inserts continue to permit their existing nullable source and retention. The conceptual SQLite triggers are executable as follows:

```sql
CREATE TRIGGER phase_d_activity_retention_guard
BEFORE INSERT ON activity_events
WHEN NEW.category IN ('review','feedback') OR NEW.action IN ('review_submitted','feedback_submitted')
BEGIN
  SELECT RAISE(ABORT, 'Phase D Activity requires source audit event')
    WHERE NEW.source_audit_event_id IS NULL OR NEW.retain_until IS NULL;
  SELECT RAISE(ABORT, 'Phase D Activity audit scope mismatch')
    WHERE NOT EXISTS (SELECT 1 FROM audit_event_portfolios p
      WHERE p.audit_event_id = NEW.source_audit_event_id AND p.portfolio_id = NEW.portfolio_id);
  SELECT RAISE(ABORT, 'Phase D Activity retention mismatch')
    WHERE NEW.retain_until <> (SELECT retain_until FROM audit_events
      WHERE id = NEW.source_audit_event_id);
END;
CREATE TRIGGER phase_d_audit_retention_guard
BEFORE INSERT ON audit_events
WHEN NEW.action IN ('review_submitted','feedback_submitted')
BEGIN
  SELECT RAISE(ABORT, 'Phase D audit requires retention')
    WHERE NEW.retain_until IS NULL;
  SELECT RAISE(ABORT, 'Phase D audit owner/retention mismatch')
    WHERE (NEW.entity_type = 'advisory_review' AND NEW.retain_until <> (SELECT retain_until FROM advisory_reviews WHERE id = NEW.entity_id))
       OR (NEW.entity_type = 'review_feedback' AND NEW.retain_until <> (SELECT retain_until FROM review_feedback WHERE id = NEW.entity_id))
       OR NEW.entity_type NOT IN ('advisory_review','review_feedback');
  SELECT RAISE(ABORT, 'Phase D audit payload is not an object')
    WHERE json_valid(NEW.payload) = 0 OR json_type(NEW.payload) <> 'object';
  SELECT RAISE(ABORT, 'Phase D audit payload contains a forbidden field')
    WHERE EXISTS (SELECT 1 FROM json_each(NEW.payload)
      WHERE key NOT IN ('id','action','principal_category','principal_id','portfolio_id','review_id','snapshot_id','status','occurred_at','submitted_at','created_at'));
  SELECT RAISE(ABORT, 'Phase D audit payload contains nested data')
    WHERE EXISTS (SELECT 1 FROM json_each(NEW.payload) WHERE type IN ('object','array'));
END;
CREATE TRIGGER phase_d_audit_scope_guard
BEFORE INSERT ON audit_event_portfolios
WHEN EXISTS (SELECT 1 FROM audit_events a WHERE a.id = NEW.audit_event_id AND a.action IN ('review_submitted','feedback_submitted'))
BEGIN
  SELECT RAISE(ABORT, 'Phase D audit association requires retention')
    WHERE NEW.retain_until IS NULL;
  SELECT RAISE(ABORT, 'Phase D audit association retention mismatch')
    WHERE NEW.retain_until <> (SELECT retain_until FROM audit_events WHERE id = NEW.audit_event_id);
END;
```

Every review, ordered child (targets, risks, counterarguments, conditions, metrics, evidence, sourced facts, fact/evidence links), feedback, both idempotency tables, and every Phase-D audit association and Activity row has a non-null boundary at least seven years after the associated operation. Existing generic audit, audit associations, and Activity rows may remain NULL. All review-owned tables have append-only update/delete triggers; their boundary is not a delete authorization. Migration 008 emits the same two exact triggers for each of `advisory_reviews`, `review_targets`, `review_risks`, `review_counterarguments`, `review_conditions`, `review_metrics`, `review_evidence`, `review_sourced_facts`, `review_fact_evidence`, `review_idempotency`, `review_feedback`, and `feedback_idempotency`: `BEFORE UPDATE` and `BEFORE DELETE`, each executing `SELECT RAISE(ABORT, 'Phase D rows are append-only');`. The audit association and Phase-D Activity use the existing/recreated append-only triggers as well.

## 7. Canonical reconstruction and retention rules

A committed review is reconstructed only from the normalized graph, in this exact order: read `advisory_reviews` by `(portfolio_id,id)`; map `id -> review_id`, `snapshot_id`, `calculation_version`, `scope_type -> scope.type`, `historical_status -> status`, scalar text/model/timestamp/provenance columns to the identically named wire fields, and `submitted_by_category/submitted_by_id` plus `machine_principals` permissions to `PrincipalAttribution`; read `review_targets ORDER BY ordinal` for the holding scope's positional `holding_ids` and `instrument_ids` (portfolio scope emits the required empty arrays); then read risks, counterarguments, conditions, and metrics each by ascending `ordinal`, evidence by ascending `ordinal`, and sourced facts by ascending `fact_ordinal`; resolve each fact's `review_fact_evidence` rows by ascending `evidence_ordinal` and emit the referenced evidence ID array in that deterministic order. `published_at` remains omitted when NULL. The database `id` is the wire `review_id`; `generated_at` and `submitted_at` map directly to their wire timestamps. `lifecycle` is the only derived portion: it is computed from the current snapshot while all historical columns remain unchanged.

For a same-key replay, read `review_idempotency` by `(principal_id, portfolio_id, idempotency_key)`, compare the canonical request fingerprint, then read this same committed normalized graph by its stored `review_id`. A matching fingerprint returns exactly the reconstructed immutable representation and original `201`/`Location` semantics; a different fingerprint returns `409`. No replay path reads an opaque payload or relies on a provider. The write transaction commits the complete graph, audit association, and Activity row atomically before a replay can observe it.

`retain_until` is canonical UTC text and must be at least seven years after the owning `submitted_at`, `occurred_at`, or `created_at` (as applicable), checked by the Phase-D owner/child triggers with `julianday` comparisons. Child and association INSERT triggers require equality with the owner boundary, preventing a shorter or mismatched boundary. The application has no purge/delete endpoint or background purge; append-only triggers reject deletion, idempotency keys remain reserved indefinitely, and no key reuse is in contract. A future purge requires a new reviewed/versioned migration that atomically processes this entire graph and replaces/disables append-only triggers.

## 8. Lifecycle and implementation obligations

`ReviewLifecycle` is an OpenAPI branch: `current_snapshot_id:null` requires `outdated:false`; a non-null current snapshot has an explicit current (`outdated:false`) or outdated (`outdated:true`) branch. Normatively `outdated=false` iff IDs match and `outdated=true` iff they differ; JSON Schema cannot compare two IDs, so the handler/store must enforce this equality.

The exact historical snapshot, calculation version, targets, evidence, facts, recommendation, and status never change when a newer snapshot arrives. A missing current snapshot reports `current_snapshot_id:null` and `outdated:false` without rewriting history.

The contract intentionally leaves all runtime pipeline work unchecked: migration implementation, domain/store transaction tests, machine auth, HTTP handlers, frontend, Hermes adapter, and dated-snapshot end-to-end tests. This record defines their required behavior only.

## 9. Required verification cases

A contract verifier must prove that OpenAPI rejects: portfolio scope with targets; holding scope with empty arrays or duplicate arrays; complete review with empty evidence/facts; complete review with `no_recommendation`; incomplete review with a recommendation; unavailable/stale evidence in a complete review; contradictory snapshot `state`/`complete`; contradictory lifecycle ID/null/`outdated`; final page with `next_cursor`; and non-final page without a cursor.

The scratch SQLite migration probe must execute actual migrations `001_initial.sql` through `007_thesis.sql` with `PRAGMA foreign_keys=ON`, seed one valid legacy Activity row with `source_audit_event_id IS NULL` and one with a non-null source plus its existing audit row, execute the documented `008_scoped_reviews.sql` transaction, and verify both rows are byte-for-byte preserved, `PRAGMA foreign_key_check` is empty, the source/order indexes and append-only triggers are recreated, and rollback restores the pre-migration schema on any failure. It must then insert a full Phase-D review/feedback graph with every scalar and child field, accept a correctly scoped Activity row with matching retention, and reject Phase-D Activity rows with NULL source, missing/mismatched retention, and cross-portfolio source association. It must also reject Phase-D audit payloads containing forbidden keys or a mismatched owner boundary while accepting the exact allowlisted payload.

The normalized reconstruction probe must populate every `Review` field, all three text arrays, metrics, evidence including optional publication date, sourced facts and ordered fact/evidence links, targets, model provenance, principal attribution, idempotency row, audit association, and Activity. It must reconstruct field-for-field in the declared order from normalized tables alone (not `snapshots.payload` or an audit payload), replay the same key with the same fingerprint as `201` with the original `Location`, and reject a different fingerprint as `409`. A fresh in-memory SQLite probe with foreign keys enabled must reject: a review target from another portfolio/snapshot; a target whose instrument differs from the snapshot holding; a fact linked to evidence from another review; review idempotency with a principal/portfolio mismatch; feedback with a browser-principal/portfolio/review mismatch; feedback idempotency with a different feedback owner; and an Activity row whose audit association has another portfolio. It must accept the corresponding same-owner rows.

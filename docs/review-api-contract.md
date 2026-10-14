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

Same key plus the same canonical request returns the original immutable result with original `201` semantics and no duplicate rows. Same key plus a different request returns `409 idempotency_conflict`. Idempotency records use `retain_until`, not `expires_at`: the key remains reserved while the row exists for at least seven years. Purge may remove a key only with its related retained review/feedback, audit, Activity, and dependent records under the approved retention process. Replay guarantees end only after that purge; key reuse is then allowed. Review content, targets, evidence, status, and snapshot association have no update/delete route and are protected by append-only triggers.

## 6. SQLite persistence proposal for `008_scoped_reviews.sql`

The migration runs transactionally after `PRAGMA foreign_keys=ON`. It first creates/validates required parent unique keys, then creates child tables, then creates the rebuilt Activity table and triggers. Existing tables are `portfolios`, `instruments`, `lots`, `snapshots`, `audit_events`, and `activity_events`; no hypothetical production legacy bridge is needed. Fresh-database ordering is explicit: existing parents, normalized snapshot/target parents, audit association, then child rows and rebuilt Activity.

Before the normalized tables, migration 008 creates the SQLite parent key required by the composite snapshot foreign key: `CREATE UNIQUE INDEX snapshots_portfolio_id_id ON snapshots(portfolio_id, id)`. This is valid against the existing `snapshots(id PRIMARY KEY, portfolio_id, ...)` table and makes the parent-column order explicit.

The following definitions are the structural contract (types/checks may be expanded only without weakening these keys):

```sql
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
  id TEXT PRIMARY KEY, portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, calculation_version TEXT NOT NULL,
  scope_type TEXT NOT NULL CHECK(scope_type IN ('portfolio','holding')),
  historical_status TEXT NOT NULL CHECK(historical_status IN ('complete','incomplete')),
  recommendation TEXT NOT NULL CHECK(recommendation IN ('hold','review','trim_candidate','exit_candidate','no_recommendation')),
  submitted_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  UNIQUE(id, portfolio_id), UNIQUE(portfolio_id, id), UNIQUE(id, portfolio_id, snapshot_id),
  FOREIGN KEY(portfolio_id, snapshot_id, calculation_version) REFERENCES review_snapshots(portfolio_id, snapshot_id, calculation_version)
);

CREATE TABLE review_targets (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL,
  PRIMARY KEY(review_id, holding_id), UNIQUE(review_id, portfolio_id, snapshot_id, holding_id, instrument_id),
  FOREIGN KEY(review_id, portfolio_id, snapshot_id) REFERENCES advisory_reviews(id, portfolio_id, snapshot_id),
  FOREIGN KEY(portfolio_id, snapshot_id, holding_id, instrument_id) REFERENCES snapshot_holdings(portfolio_id, snapshot_id, holding_id, instrument_id)
);

CREATE TABLE review_evidence (
  review_id TEXT NOT NULL, evidence_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
  PRIMARY KEY(review_id, evidence_id), UNIQUE(review_id, ordinal),
  FOREIGN KEY(review_id) REFERENCES advisory_reviews(id)
);
CREATE TABLE review_sourced_facts (
  review_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL, statement TEXT NOT NULL,
  PRIMARY KEY(review_id, fact_ordinal), UNIQUE(review_id, fact_ordinal),
  FOREIGN KEY(review_id) REFERENCES advisory_reviews(id)
);
CREATE TABLE review_fact_evidence (
  review_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL, evidence_id TEXT NOT NULL,
  PRIMARY KEY(review_id, fact_ordinal, evidence_id),
  FOREIGN KEY(review_id, fact_ordinal) REFERENCES review_sourced_facts(review_id, fact_ordinal),
  FOREIGN KEY(review_id, evidence_id) REFERENCES review_evidence(review_id, evidence_id)
);

CREATE TABLE review_idempotency (
  principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL, review_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  PRIMARY KEY(principal_id, portfolio_id, idempotency_key), UNIQUE(portfolio_id, review_id),
  FOREIGN KEY(principal_id, portfolio_id) REFERENCES machine_principals(id, portfolio_id),
  FOREIGN KEY(review_id, portfolio_id) REFERENCES advisory_reviews(id, portfolio_id)
);

CREATE TABLE review_feedback (
  id TEXT PRIMARY KEY, browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL,
  decision TEXT NOT NULL CHECK(decision IN ('agree','disagree','defer','request_another_review')),
  note TEXT NULL, occurred_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  UNIQUE(id, browser_principal_id, portfolio_id, review_id),
  FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id),
  FOREIGN KEY(portfolio_id, review_id) REFERENCES advisory_reviews(portfolio_id, id)
);
CREATE TABLE feedback_idempotency (
  browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL, feedback_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL,
  PRIMARY KEY(browser_principal_id, portfolio_id, review_id, idempotency_key),
  FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id),
  FOREIGN KEY(portfolio_id, review_id) REFERENCES advisory_reviews(portfolio_id, id),
  FOREIGN KEY(feedback_id, browser_principal_id, portfolio_id, review_id) REFERENCES review_feedback(id, browser_principal_id, portfolio_id, review_id)
);

CREATE TABLE audit_event_portfolios (
  audit_event_id TEXT NOT NULL, portfolio_id TEXT NOT NULL,
  PRIMARY KEY(audit_event_id, portfolio_id),
  FOREIGN KEY(audit_event_id) REFERENCES audit_events(id),
  FOREIGN KEY(portfolio_id) REFERENCES portfolios(id)
);
```

The migration rebuilds fresh `activity_events` with the existing safe columns and replaces its source relation with:

```sql
source_audit_event_id TEXT NOT NULL,
portfolio_id TEXT NOT NULL,
FOREIGN KEY(source_audit_event_id, portfolio_id)
  REFERENCES audit_event_portfolios(audit_event_id, portfolio_id),
UNIQUE(source_audit_event_id, portfolio_id)
```

The audit event and `audit_event_portfolios` row are inserted in the same transaction as the scoped Activity row, so an Activity event cannot claim another portfolio's audit event. Existing generic activity rows are copied into the rebuilt table with their own association; this is a fresh-database ordering contract, not a request for a hypothetical legacy bridge.

All review/feedback, idempotency, audit, and Activity rows have `retain_until` at least seven years after their owning event. Foreign-key parent keys are written in the exact order shown. The proposed migration also adds append-only triggers for advisory reviews, targets, evidence, facts, feedback, audit association, and Activity.

## 7. Lifecycle and implementation obligations

`ReviewLifecycle` is an OpenAPI branch: `current_snapshot_id:null` requires `outdated:false`; a non-null current snapshot has an explicit current (`outdated:false`) or outdated (`outdated:true`) branch. Normatively `outdated=false` iff IDs match and `outdated=true` iff they differ; JSON Schema cannot compare two IDs, so the handler/store must enforce this equality.

The exact historical snapshot, calculation version, targets, evidence, facts, recommendation, and status never change when a newer snapshot arrives. A missing current snapshot reports `current_snapshot_id:null` and `outdated:false` without rewriting history.

The contract intentionally leaves all runtime pipeline work unchecked: migration implementation, domain/store transaction tests, machine auth, HTTP handlers, frontend, Hermes adapter, and dated-snapshot end-to-end tests. This record defines their required behavior only.

## 8. Required verification cases

A contract verifier must prove that OpenAPI rejects: portfolio scope with targets; holding scope with empty arrays or duplicate arrays; complete review with empty evidence/facts; complete review with `no_recommendation`; incomplete review with a recommendation; unavailable/stale evidence in a complete review; contradictory snapshot `state`/`complete`; contradictory lifecycle ID/null/`outdated`; final page with `next_cursor`; and non-final page without a cursor.

A fresh in-memory SQLite probe with foreign keys enabled must reject: a review target from another portfolio/snapshot; a target whose instrument differs from the snapshot holding; a fact linked to evidence from another review; review idempotency with a principal/portfolio mismatch; feedback with a browser-principal/portfolio/review mismatch; feedback idempotency with a different feedback owner; and an Activity row whose audit association has another portfolio. It must accept the corresponding same-owner rows.

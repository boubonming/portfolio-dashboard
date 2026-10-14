# Phase D scoped review API contract

Status: implementation contract only. This change modifies no handlers, migrations, UI, credentials, deployment, or database. `api/openapi.yaml` and this record are one contract; the portfolio-scoped namespace is `/api/v1/portfolios/{portfolioId}`.

## 1. Boundary and non-goals

Phase D exposes immutable snapshot/context reads, machine-submitted advisory reviews, review reads, and browser-only append-only feedback. The machine principal cannot mutate holdings, lots, instruments, quotes, FX, theses, snapshots, accounts, browser sessions, credentials, or trades. Feedback never edits a review and never records a trade. There is no Hermes management endpoint and the application does not embed an agent runtime.

All private JSON success and error responses carry exactly these security headers: `Cache-Control: no-store`, `Vary: Cookie`, and `X-Content-Type-Options: nosniff`. Review and feedback writes accept only UTF-8 JSON, reject unknown fields, duplicate keys, trailing values, raw HTML, and bodies larger than 256 KiB. All strings are bounded by the OpenAPI schemas and rendered as escaped text.

The canonical next migration is exactly `008_scoped_reviews.sql`. Migrations `001_initial.sql` through `007_thesis.sql` already exist, including `004_fx_observation_constraints.sql`; this contract does not add that migration.

## 2. Identity, authentication, and authorization

There are two credential classes:

- Browser: the existing HttpOnly, Secure-in-production, SameSite=Strict `portfolio_session` cookie and its in-memory CSRF token. The cookie contains a random short-lived session identifier; it is revocable on logout.
- Machine: one fixed, explicitly configured Hermes principal with an opaque bearer secret held only in the Hermes environment/secret manager. The secret is never logged, returned, put in a URL/frontend asset/browser store, or persisted in SQLite. The configured principal is bound to one portfolio and must have `reviews:write` enabled for POST review; `portfolio:read` is the only optional additional permission.

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

Machine-only POST review and every dual-auth read deterministically use `UnauthorizedBearer` with `WWW-Authenticate: Bearer realm="portfolio-dashboard"`. Browser-only feedback uses `UnauthorizedCookie` and never emits a bearer challenge. Both-credential rejection remains a runtime negative-test obligation because OpenAPI security alternatives cannot express mutual exclusion.

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

A complete review branch requires `status: complete`, a recommendation other than `no_recommendation`, at least one `available`/`current` HTTPS evidence item, and at least one sourced fact referencing submitted evidence. An incomplete review requires `status: incomplete` and `recommendation: no_recommendation`; it may describe stale, unknown, unavailable, blocked, invalid, or contradictory evidence. The server still enforces freshness policy, target membership, contradiction checks, calculation version, and every sourced-fact evidence reference. An invalid completeness claim is `422 incomplete_review`. SQLite repeats these branch rules with deferred composite witness foreign keys: a complete parent carries non-null primary evidence, primary sourced-fact, and (for holding scope) primary-target witnesses; an incomplete parent carries none. The parent may be inserted before its children, but commit fails if the deferred witnesses are absent, foreign, stale/unavailable, or scope-inconsistent.

Evidence URLs are absolute HTTPS URLs with lowercase ASCII DNS labels, no userinfo, explicit port, fragment, whitespace, or controls. A URL is canonicalized at the Go `net/url` store boundary to `url_host` and `url_suffix`; `url_suffix` is empty or begins with `/` or `?`, and the response is exactly `https://` plus host plus suffix. The host is at most 253 bytes and each label is 1–63 bytes. The maximum reconstructed URL is 2,048 bytes. Positive examples are `https://example.test` and `https://example.test/source/demo-001?b=c`; `https://`, whitespace, userinfo, ports including 443/99999, uppercase or non-ASCII hosts, `http://`, fragments, controls, and overlength values are rejected by both boundary and storage checks. Evidence, facts, rationale, risks, counterarguments, and conditions are untrusted text and never instructions, policy, permission, or tool commands. Model provenance contains provider/model/version and the fixed `hermes-environment` runtime only; it contains no prompt, endpoint, token, session, or account secret.

## 5. Exact idempotency, immutability, and feedback note

Review and feedback writes are append-only and atomic with their owned rows, idempotency row, audit event, and safe Activity row. The transaction rolls back completely on any validation, foreign-key, uniqueness, audit, or Activity failure.

The server canonicalizes the validated JSON before fingerprinting. Omitted optional values and explicit JSON `null` have the same canonical representation where the field is nullable. For feedback, omitted `note` and `"note": null` both canonicalize to `note:null`; the response always contains `note:null` in that case; a present string is 1–2,000 characters and never empty. The SQLite column is nullable `TEXT` with the same bound.

Same key plus the same canonical request returns the original immutable result with original `201` semantics and no duplicate rows. Same key plus a different request returns `409 idempotency_conflict`. Both idempotency tables have a non-null `retain_until` at least seven years after their operation and remain reserved indefinitely: this current contract has no purge/delete endpoint or background purge, and append-only triggers reject deletion, so keys are never reused. `retain_until` is only the earliest eligibility timestamp for a future separately reviewed and versioned retention migration; it does not authorize deletion now. Any future purge must atomically remove the complete review/feedback graph and replace or disable the append-only triggers in that new migration, outside Phase D. Replay is therefore always available for the lifetime of this contract.

## 6. Executable `008_scoped_reviews.sql`

The following is the one authoritative migration body. It is extracted verbatim and run with SQLite foreign keys enabled inside the transaction owned by `internal/store/store.go`: `BeginTx`, rollback on body error, the post-body `schema_migrations` insert, and `Commit` all belong to that migrator. The body deliberately contains no `BEGIN`, `BEGIN IMMEDIATE`, `COMMIT`, `ROLLBACK`, or direct `schema_migrations` insert. The temporary assertion table makes every ambiguity an executable abort rather than documentation. A repository-equivalent harness executes migrations 001–007, begins one outer transaction, executes this body, inserts `008_scoped_reviews.sql` exactly as `store.go` does, and commits; it must produce one migration row without a nested-transaction or duplicate-record error.

```sql
PRAGMA foreign_keys=ON;
PRAGMA defer_foreign_keys=ON;

CREATE TEMP TABLE _phase_d_assertion (ok INTEGER NOT NULL CHECK(ok=1));
ALTER TABLE audit_events ADD COLUMN retain_until TEXT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS snapshots_portfolio_id_id ON snapshots(portfolio_id,id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lots_portfolio_id_id ON lots(portfolio_id,id);

CREATE TABLE browser_principals (
  id TEXT PRIMARY KEY CHECK(id='local-owner'),
  kind TEXT NOT NULL CHECK(kind='single_user'),
  created_at TEXT NOT NULL CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(created_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(created_at,1,10)))=substr(created_at,1,10) AND substr(created_at,20,1)='.' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);
INSERT INTO browser_principals VALUES('local-owner','single_user',strftime('%Y-%m-%dT%H:%M:%S','now')||'.000000000Z');

CREATE TABLE machine_principals (
  id TEXT PRIMARY KEY, portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
  permission_portfolio_read INTEGER NOT NULL CHECK(permission_portfolio_read IN(0,1)),
  permission_reviews_write INTEGER NOT NULL CHECK(permission_reviews_write IN(0,1)),
  active INTEGER NOT NULL CHECK(active IN(0,1)), created_at TEXT NOT NULL,
  UNIQUE(id,portfolio_id),
  CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(created_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(created_at,1,10)))=substr(created_at,1,10) AND substr(created_at,20,1)='.' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59')
);

CREATE TABLE review_snapshots (
  portfolio_id TEXT NOT NULL REFERENCES portfolios(id), snapshot_id TEXT NOT NULL,
  calculation_version TEXT NOT NULL, PRIMARY KEY(portfolio_id,snapshot_id),
  UNIQUE(portfolio_id,snapshot_id,calculation_version),
  FOREIGN KEY(portfolio_id,snapshot_id) REFERENCES snapshots(portfolio_id,id)
);
CREATE TABLE snapshot_holdings (
  portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL,
  PRIMARY KEY(portfolio_id,snapshot_id,holding_id), UNIQUE(portfolio_id,snapshot_id,holding_id,instrument_id),
  FOREIGN KEY(portfolio_id,snapshot_id) REFERENCES review_snapshots(portfolio_id,snapshot_id), FOREIGN KEY(instrument_id) REFERENCES instruments(id)
);

CREATE TABLE advisory_reviews (
  id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128), portfolio_id TEXT NOT NULL REFERENCES portfolios(id),
  snapshot_id TEXT NOT NULL CHECK(length(snapshot_id) BETWEEN 1 AND 128), calculation_version TEXT NOT NULL CHECK(length(calculation_version) BETWEEN 1 AND 128),
  scope_type TEXT NOT NULL CHECK(scope_type IN('portfolio','holding')), historical_status TEXT NOT NULL CHECK(historical_status IN('complete','incomplete')),
  recommendation TEXT NOT NULL CHECK(recommendation IN('hold','review','trim_candidate','exit_candidate','no_recommendation')),
  rationale TEXT NOT NULL CHECK(length(rationale) BETWEEN 1 AND 8000), model_opinion TEXT NOT NULL CHECK(length(model_opinion) BETWEEN 1 AND 8000),
  model_provider TEXT NOT NULL CHECK(length(model_provider) BETWEEN 1 AND 128), model_name TEXT NOT NULL CHECK(length(model_name) BETWEEN 1 AND 256), model_version TEXT NOT NULL CHECK(length(model_version) BETWEEN 1 AND 128), model_runtime TEXT NOT NULL CHECK(model_runtime='hermes-environment'),
  generated_at TEXT NOT NULL, submitted_at TEXT NOT NULL,
  submitted_by_id TEXT NOT NULL CHECK(length(submitted_by_id) BETWEEN 1 AND 128), submitted_by_kind TEXT NOT NULL CHECK(submitted_by_kind='machine'),
  submitted_by_primary_permission TEXT NOT NULL CHECK(submitted_by_primary_permission='reviews:write'),
  primary_evidence_id TEXT NULL, primary_evidence_availability TEXT NULL, primary_evidence_freshness TEXT NULL,
  primary_fact_ordinal INTEGER NULL, primary_target_holding_id TEXT NULL, retain_until TEXT NOT NULL,
  UNIQUE(id,portfolio_id), UNIQUE(portfolio_id,id), UNIQUE(id,portfolio_id,snapshot_id), UNIQUE(id,portfolio_id,snapshot_id,calculation_version),
  FOREIGN KEY(portfolio_id,snapshot_id,calculation_version) REFERENCES review_snapshots(portfolio_id,snapshot_id,calculation_version),
  FOREIGN KEY(id,portfolio_id,primary_evidence_id,primary_evidence_availability,primary_evidence_freshness) REFERENCES review_evidence(review_id,portfolio_id,evidence_id,availability,freshness) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(id,portfolio_id,primary_fact_ordinal) REFERENCES review_sourced_facts(review_id,portfolio_id,fact_ordinal) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(id,portfolio_id,primary_fact_ordinal,primary_evidence_id) REFERENCES review_fact_evidence(review_id,portfolio_id,fact_ordinal,evidence_id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(id,portfolio_id,snapshot_id,calculation_version,primary_target_holding_id) REFERENCES review_targets(review_id,portfolio_id,snapshot_id,calculation_version,holding_id) DEFERRABLE INITIALLY DEFERRED,
  CHECK((historical_status='complete')=(recommendation<>'no_recommendation')),
  CHECK((historical_status='complete' AND primary_evidence_id IS NOT NULL AND primary_evidence_availability='available' AND primary_evidence_freshness='current' AND primary_fact_ordinal IS NOT NULL) OR (historical_status='incomplete' AND primary_evidence_id IS NULL AND primary_evidence_availability IS NULL AND primary_evidence_freshness IS NULL AND primary_fact_ordinal IS NULL)),
  CHECK((scope_type='holding' AND primary_target_holding_id IS NOT NULL) OR (scope_type='portfolio' AND primary_target_holding_id IS NULL)),
  CHECK(length(generated_at)=30 AND generated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(generated_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(generated_at,1,10)))=substr(generated_at,1,10) AND substr(generated_at,12,2) BETWEEN '00' AND '23' AND substr(generated_at,15,2) BETWEEN '00' AND '59' AND substr(generated_at,18,2) BETWEEN '00' AND '59'),
  CHECK(length(submitted_at)=30 AND submitted_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(submitted_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(submitted_at,1,10)))=substr(submitted_at,1,10) AND substr(submitted_at,12,2) BETWEEN '00' AND '23' AND substr(submitted_at,15,2) BETWEEN '00' AND '59' AND substr(submitted_at,18,2) BETWEEN '00' AND '59'),
  CHECK(length(retain_until)=30 AND retain_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(retain_until,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(retain_until,1,10)))=substr(retain_until,1,10) AND substr(retain_until,12,2) BETWEEN '00' AND '23' AND substr(retain_until,15,2) BETWEEN '00' AND '59' AND substr(retain_until,18,2) BETWEEN '00' AND '59' AND julianday(retain_until)>=julianday(submitted_at,'+7 years'))
);

CREATE TABLE review_targets (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, snapshot_id TEXT NOT NULL, calculation_version TEXT NOT NULL, holding_id TEXT NOT NULL, instrument_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 99), PRIMARY KEY(review_id,holding_id), UNIQUE(review_id,ordinal), UNIQUE(review_id,portfolio_id,snapshot_id,calculation_version,holding_id),
  FOREIGN KEY(review_id,portfolio_id,snapshot_id) REFERENCES advisory_reviews(id,portfolio_id,snapshot_id), FOREIGN KEY(portfolio_id,snapshot_id,holding_id,instrument_id) REFERENCES snapshot_holdings(portfolio_id,snapshot_id,holding_id,instrument_id), FOREIGN KEY(review_id,portfolio_id,snapshot_id,calculation_version) REFERENCES advisory_reviews(id,portfolio_id,snapshot_id,calculation_version)
);
CREATE TABLE review_risks (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), PRIMARY KEY(review_id,ordinal), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id));
CREATE TABLE review_counterarguments (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), PRIMARY KEY(review_id,ordinal), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id));
CREATE TABLE review_conditions (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 31), text TEXT NOT NULL CHECK(length(text) BETWEEN 1 AND 2000), PRIMARY KEY(review_id,ordinal), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id));
CREATE TABLE review_metrics (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 99), name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 128), value TEXT NOT NULL CHECK(length(value) BETWEEN 1 AND 128 AND value GLOB '-*[0-9]*' AND value NOT GLOB '*[^0-9.-]*'), unit TEXT NOT NULL CHECK(length(unit) BETWEEN 1 AND 64), as_of TEXT NOT NULL, source TEXT NOT NULL CHECK(source IN('snapshot','calculation','submitted')), deterministic INTEGER NOT NULL CHECK(deterministic=1), PRIMARY KEY(review_id,ordinal), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id),
  CHECK(length(as_of)=30 AND as_of GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(as_of,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(as_of,1,10)))=substr(as_of,1,10) AND substr(as_of,12,2) BETWEEN '00' AND '23' AND substr(as_of,15,2) BETWEEN '00' AND '59' AND substr(as_of,18,2) BETWEEN '00' AND '59')
);
CREATE TABLE review_evidence (
  review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, evidence_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63), url_host TEXT NOT NULL, url_suffix TEXT NOT NULL DEFAULT '', title TEXT NOT NULL CHECK(length(title) BETWEEN 1 AND 512), published_at TEXT NULL, accessed_at TEXT NOT NULL, availability TEXT NOT NULL CHECK(availability IN('available','unavailable','blocked','invalid')), freshness TEXT NOT NULL CHECK(freshness IN('current','stale','unknown')), excerpt TEXT NOT NULL CHECK(length(excerpt) BETWEEN 1 AND 4000), PRIMARY KEY(review_id,evidence_id), UNIQUE(review_id,ordinal), UNIQUE(review_id,portfolio_id,evidence_id), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id),
  UNIQUE(review_id,portfolio_id,evidence_id,availability,freshness),
  CHECK(length(url_host) BETWEEN 1 AND 253 AND url_host=lower(url_host) AND url_host NOT GLOB '*[^a-z0-9.-]*' AND url_host NOT LIKE '.%' AND url_host NOT LIKE '%.' AND url_host NOT LIKE '%..%' AND length(url_suffix)<=2047 AND (url_suffix='' OR substr(url_suffix,1,1) IN('/','?')) AND url_suffix NOT GLOB '*[^A-Za-z0-9._~:/?$&''()*+,;=%!-]*' AND length('https://'||url_host||url_suffix) BETWEEN 9 AND 2048),
  CHECK(published_at IS NULL OR (length(published_at)=30 AND published_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(published_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(published_at,1,10)))=substr(published_at,1,10) AND substr(published_at,12,2) BETWEEN '00' AND '23' AND substr(published_at,15,2) BETWEEN '00' AND '59' AND substr(published_at,18,2) BETWEEN '00' AND '59')),
  CHECK(length(accessed_at)=30 AND accessed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(accessed_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(accessed_at,1,10)))=substr(accessed_at,1,10) AND substr(accessed_at,12,2) BETWEEN '00' AND '23' AND substr(accessed_at,15,2) BETWEEN '00' AND '59' AND substr(accessed_at,18,2) BETWEEN '00' AND '59')
);
CREATE TABLE review_sourced_facts (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL CHECK(fact_ordinal BETWEEN 0 AND 31), statement TEXT NOT NULL CHECK(length(statement) BETWEEN 1 AND 2000), PRIMARY KEY(review_id,fact_ordinal), UNIQUE(review_id,portfolio_id,fact_ordinal), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id));
CREATE TABLE review_fact_evidence (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, fact_ordinal INTEGER NOT NULL, evidence_id TEXT NOT NULL, evidence_ordinal INTEGER NOT NULL CHECK(evidence_ordinal BETWEEN 0 AND 15), PRIMARY KEY(review_id,fact_ordinal,evidence_id), UNIQUE(review_id,portfolio_id,fact_ordinal,evidence_id), UNIQUE(review_id,fact_ordinal,evidence_ordinal), FOREIGN KEY(review_id,portfolio_id,fact_ordinal) REFERENCES review_sourced_facts(review_id,portfolio_id,fact_ordinal), FOREIGN KEY(review_id,portfolio_id,evidence_id) REFERENCES review_evidence(review_id,portfolio_id,evidence_id));

CREATE TABLE review_idempotency (principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_fingerprint TEXT NOT NULL, review_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL, PRIMARY KEY(principal_id,portfolio_id,idempotency_key), UNIQUE(portfolio_id,review_id), FOREIGN KEY(principal_id,portfolio_id) REFERENCES machine_principals(id,portfolio_id), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id), CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%d',julianday(substr(created_at,1,10)))=substr(created_at,1,10) AND substr(created_at,20,1)='.' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59'), CHECK(length(retain_until)=30 AND retain_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%d',julianday(substr(retain_until,1,10)))=substr(retain_until,1,10) AND julianday(retain_until)>=julianday(created_at,'+7 years')));
CREATE TABLE review_feedback (
  id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128), browser_principal_id TEXT NOT NULL CHECK(browser_principal_id='local-owner'), portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL, decision TEXT NOT NULL CHECK(decision IN('agree','disagree','defer','request_another_review')), note TEXT NULL CHECK(note IS NULL OR length(note) BETWEEN 1 AND 2000), occurred_at TEXT NOT NULL, retain_until TEXT NOT NULL, submitted_by_id TEXT NOT NULL CHECK(submitted_by_id='local-owner'), submitted_by_kind TEXT NOT NULL CHECK(submitted_by_kind='browser_user'), submitted_by_primary_permission TEXT NOT NULL CHECK(submitted_by_primary_permission='portfolio:read'), UNIQUE(id,browser_principal_id,portfolio_id,review_id), FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id), FOREIGN KEY(portfolio_id,review_id) REFERENCES advisory_reviews(portfolio_id,id), CHECK(length(occurred_at)=30 AND occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(occurred_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(occurred_at,1,10)))=substr(occurred_at,1,10) AND substr(occurred_at,12,2) BETWEEN '00' AND '23' AND substr(occurred_at,15,2) BETWEEN '00' AND '59' AND substr(occurred_at,18,2) BETWEEN '00' AND '59'), CHECK(length(retain_until)=30 AND retain_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(retain_until,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(retain_until,1,10)))=substr(retain_until,1,10) AND substr(retain_until,12,2) BETWEEN '00' AND '23' AND substr(retain_until,15,2) BETWEEN '00' AND '59' AND substr(retain_until,18,2) BETWEEN '00' AND '59' AND julianday(retain_until)>=julianday(occurred_at,'+7 years'))
);
CREATE TABLE review_submission_permission_snapshots (review_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal=1), permission TEXT NOT NULL CHECK(permission IN('portfolio:read','reviews:write')), PRIMARY KEY(review_id,ordinal), UNIQUE(review_id,permission), FOREIGN KEY(review_id,portfolio_id) REFERENCES advisory_reviews(id,portfolio_id));
CREATE TABLE feedback_permission_snapshots (feedback_id TEXT NOT NULL, browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL, ordinal INTEGER NOT NULL CHECK(ordinal=1), permission TEXT NOT NULL CHECK(permission='portfolio:read'), PRIMARY KEY(feedback_id,ordinal), UNIQUE(feedback_id,permission), FOREIGN KEY(feedback_id,browser_principal_id,portfolio_id,review_id) REFERENCES review_feedback(id,browser_principal_id,portfolio_id,review_id));
CREATE TABLE feedback_idempotency (browser_principal_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, review_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_fingerprint TEXT NOT NULL, feedback_id TEXT NOT NULL, created_at TEXT NOT NULL, retain_until TEXT NOT NULL, PRIMARY KEY(browser_principal_id,portfolio_id,review_id,idempotency_key), FOREIGN KEY(browser_principal_id) REFERENCES browser_principals(id), FOREIGN KEY(portfolio_id,review_id) REFERENCES advisory_reviews(portfolio_id,id), FOREIGN KEY(feedback_id,browser_principal_id,portfolio_id,review_id) REFERENCES review_feedback(id,browser_principal_id,portfolio_id,review_id), CHECK(length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%d',julianday(substr(created_at,1,10)))=substr(created_at,1,10) AND substr(created_at,20,1)='.' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59'), CHECK(length(retain_until)=30 AND retain_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND strftime('%Y-%m-%d',julianday(substr(retain_until,1,10)))=substr(retain_until,1,10) AND julianday(retain_until)>=julianday(created_at,'+7 years')));
CREATE TABLE audit_event_portfolios (audit_event_id TEXT NOT NULL, portfolio_id TEXT NOT NULL, retain_until TEXT NULL, PRIMARY KEY(audit_event_id,portfolio_id), FOREIGN KEY(audit_event_id) REFERENCES audit_events(id), FOREIGN KEY(portfolio_id) REFERENCES portfolios(id));

CREATE TRIGGER phase_d_url_label_guard BEFORE INSERT ON review_evidence BEGIN
  SELECT RAISE(ABORT,'invalid evidence host') WHERE EXISTS (WITH RECURSIVE labels(rest,label) AS (SELECT NEW.url_host,CASE WHEN instr(NEW.url_host,'.')=0 THEN NEW.url_host ELSE substr(NEW.url_host,1,instr(NEW.url_host,'.')-1) END UNION ALL SELECT substr(rest,instr(rest,'.')+1),CASE WHEN instr(substr(rest,instr(rest,'.')+1),'.')=0 THEN substr(rest,instr(rest,'.')+1) ELSE substr(substr(rest,instr(rest,'.')+1),1,instr(substr(rest,instr(rest,'.')+1),'.')-1) END FROM labels WHERE instr(rest,'.')>0) SELECT 1 FROM labels WHERE length(label)<1 OR length(label)>63 OR label LIKE '-%' OR label LIKE '%-' );
END;
CREATE TRIGGER phase_d_url_label_update_guard BEFORE UPDATE OF url_host,url_suffix ON review_evidence BEGIN
  SELECT RAISE(ABORT,'invalid evidence host') WHERE EXISTS (WITH RECURSIVE labels(rest,label) AS (SELECT NEW.url_host,CASE WHEN instr(NEW.url_host,'.')=0 THEN NEW.url_host ELSE substr(NEW.url_host,1,instr(NEW.url_host,'.')-1) END UNION ALL SELECT substr(rest,instr(rest,'.')+1),CASE WHEN instr(substr(rest,instr(rest,'.')+1),'.')=0 THEN substr(rest,instr(rest,'.')+1) ELSE substr(substr(rest,instr(rest,'.')+1),1,instr(substr(rest,instr(rest,'.')+1),'.')-1) END FROM labels WHERE instr(rest,'.')>0) SELECT 1 FROM labels WHERE length(label)<1 OR length(label)>63 OR label LIKE '-%' OR label LIKE '%-');
END;

CREATE TRIGGER phase_d_review_targets_scope_insert BEFORE INSERT ON review_targets BEGIN
  SELECT RAISE(ABORT,'review target scope mismatch') WHERE NOT EXISTS(SELECT 1 FROM advisory_reviews WHERE id=NEW.review_id AND portfolio_id=NEW.portfolio_id AND scope_type='holding');
END;

INSERT INTO _phase_d_assertion SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM activity_events WHERE source_audit_event_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=activity_events.source_audit_event_id)) THEN 1 ELSE 0 END;
INSERT INTO _phase_d_assertion SELECT CASE WHEN NOT EXISTS(SELECT source_audit_event_id FROM activity_events WHERE source_audit_event_id IS NOT NULL GROUP BY source_audit_event_id HAVING COUNT(DISTINCT portfolio_id)>1) THEN 1 ELSE 0 END;
UPDATE audit_events SET retain_until=strftime('%Y-%m-%dT%H:%M:%S',julianday(substr(created_at,1,19),'+7 years'))||substr(created_at,20) WHERE retain_until IS NULL AND length(created_at)=30 AND created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T*Z' AND strftime('%Y-%m-%d',julianday(substr(created_at,1,10)))=substr(created_at,1,10) AND substr(created_at,20,1)='.' AND substr(created_at,12,2) BETWEEN '00' AND '23' AND substr(created_at,15,2) BETWEEN '00' AND '59' AND substr(created_at,18,2) BETWEEN '00' AND '59';
INSERT INTO audit_event_portfolios(audit_event_id,portfolio_id,retain_until) SELECT e.source_audit_event_id,e.portfolio_id,a.retain_until FROM activity_events e JOIN audit_events a ON a.id=e.source_audit_event_id WHERE e.source_audit_event_id IS NOT NULL GROUP BY e.source_audit_event_id,e.portfolio_id,a.retain_until;

CREATE TEMP TABLE _phase_d_activity_count (n INTEGER NOT NULL);
INSERT INTO _phase_d_activity_count SELECT COUNT(*) FROM activity_events;
CREATE TABLE activity_events_new (
  id TEXT PRIMARY KEY, portfolio_id TEXT NOT NULL REFERENCES portfolios(id), actor_category TEXT NOT NULL, action TEXT NOT NULL, category TEXT NOT NULL, entity_type TEXT, entity_id TEXT, summary TEXT NOT NULL, details TEXT NOT NULL DEFAULT '', occurred_at TEXT NOT NULL, sort_time TEXT NOT NULL CHECK(sort_time=occurred_at), source_audit_event_id TEXT NULL, retain_until TEXT NOT NULL,
  FOREIGN KEY(source_audit_event_id,portfolio_id) REFERENCES audit_event_portfolios(audit_event_id,portfolio_id),
  CHECK(length(occurred_at)=30 AND occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(occurred_at,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(occurred_at,1,10)))=substr(occurred_at,1,10) AND substr(occurred_at,12,2) BETWEEN '00' AND '23' AND substr(occurred_at,15,2) BETWEEN '00' AND '59' AND substr(occurred_at,18,2) BETWEEN '00' AND '59'),
  CHECK(length(retain_until)=30 AND retain_until GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z' AND substr(retain_until,20,1)='.' AND strftime('%Y-%m-%d',julianday(substr(retain_until,1,10)))=substr(retain_until,1,10) AND substr(retain_until,12,2) BETWEEN '00' AND '23' AND substr(retain_until,15,2) BETWEEN '00' AND '59' AND substr(retain_until,18,2) BETWEEN '00' AND '59')
);
INSERT INTO _phase_d_assertion SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM activity_events WHERE occurred_at IS NULL OR length(occurred_at)<>30 OR strftime('%Y-%m-%d',julianday(substr(occurred_at,1,10)))<>substr(occurred_at,1,10)) THEN 1 ELSE 0 END;
INSERT INTO activity_events_new(id,portfolio_id,actor_category,action,category,entity_type,entity_id,summary,details,occurred_at,sort_time,source_audit_event_id,retain_until) SELECT e.id,e.portfolio_id,e.actor_category,e.action,e.category,e.entity_type,e.entity_id,e.summary,e.details,e.occurred_at,e.sort_time,e.source_audit_event_id,COALESCE(a.retain_until,strftime('%Y-%m-%dT%H:%M:%S',julianday(substr(e.occurred_at,1,19),'+7 years'))||substr(e.occurred_at,20)) FROM activity_events e LEFT JOIN audit_events a ON a.id=e.source_audit_event_id;
DROP INDEX IF EXISTS idx_activity_source_portfolio;
DROP INDEX IF EXISTS idx_activity_portfolio_order;
DROP TRIGGER IF EXISTS activity_events_append_only_update;
DROP TRIGGER IF EXISTS activity_events_append_only_delete;
DROP TABLE activity_events;
ALTER TABLE activity_events_new RENAME TO activity_events;
CREATE UNIQUE INDEX idx_activity_source_portfolio ON activity_events(source_audit_event_id,portfolio_id);
CREATE INDEX idx_activity_portfolio_order ON activity_events(portfolio_id,sort_time DESC,id DESC);
CREATE TRIGGER activity_events_append_only_update BEFORE UPDATE ON activity_events BEGIN SELECT RAISE(ABORT,'activity events are append-only'); END;
CREATE TRIGGER activity_events_append_only_delete BEFORE DELETE ON activity_events BEGIN SELECT RAISE(ABORT,'activity events are append-only'); END;

CREATE TRIGGER phase_d_review_guard BEFORE INSERT ON advisory_reviews BEGIN SELECT RAISE(ABORT,'review principal mismatch') WHERE NOT EXISTS(SELECT 1 FROM machine_principals WHERE id=NEW.submitted_by_id AND portfolio_id=NEW.portfolio_id AND active=1 AND permission_reviews_write=1) OR NEW.submitted_by_primary_permission<>'reviews:write'; END;
CREATE TRIGGER phase_d_feedback_guard BEFORE INSERT ON review_feedback BEGIN SELECT RAISE(ABORT,'feedback principal mismatch') WHERE NEW.submitted_by_id<>NEW.browser_principal_id OR NEW.submitted_by_kind<>'browser_user'; END;
CREATE TRIGGER phase_d_review_permission_guard BEFORE INSERT ON review_submission_permission_snapshots BEGIN SELECT RAISE(ABORT,'review permission mismatch') WHERE NOT EXISTS(SELECT 1 FROM advisory_reviews r JOIN machine_principals m ON m.id=r.submitted_by_id AND m.portfolio_id=r.portfolio_id WHERE r.id=NEW.review_id AND r.portfolio_id=NEW.portfolio_id AND r.submitted_by_primary_permission='reviews:write' AND m.active=1 AND m.permission_reviews_write=1 AND NEW.ordinal=1 AND NEW.permission='portfolio:read' AND m.permission_portfolio_read=1); END;
CREATE TRIGGER phase_d_feedback_permission_guard BEFORE INSERT ON feedback_permission_snapshots BEGIN SELECT RAISE(ABORT,'feedback permission mismatch') WHERE NOT EXISTS(SELECT 1 FROM review_feedback f WHERE f.id=NEW.feedback_id AND f.browser_principal_id='local-owner' AND NEW.browser_principal_id='local-owner' AND f.portfolio_id=NEW.portfolio_id AND f.review_id=NEW.review_id AND f.submitted_by_id='local-owner' AND f.submitted_by_kind='browser_user' AND f.submitted_by_primary_permission='portfolio:read' AND NEW.ordinal=1 AND NEW.permission='portfolio:read'); END;
CREATE TRIGGER phase_d_audit_guard BEFORE INSERT ON audit_events WHEN NEW.action IN('review_submitted','feedback_submitted') BEGIN SELECT RAISE(ABORT,'invalid Phase D audit') WHERE NEW.retain_until IS NULL OR length(NEW.created_at)<>30 OR substr(NEW.created_at,21,9) GLOB '*[^0-9]*' OR substr(NEW.created_at,5,1)<>'-' OR substr(NEW.created_at,8,1)<>'-' OR substr(NEW.created_at,11,1)<>'T' OR substr(NEW.created_at,14,1)<>':' OR substr(NEW.created_at,17,1)<>':' OR substr(NEW.created_at,20,1)<>'.' OR substr(NEW.created_at,30,1)<>'Z' OR strftime('%Y-%m-%d',julianday(substr(NEW.created_at,1,10)))<>substr(NEW.created_at,1,10) OR substr(NEW.created_at,12,2) NOT BETWEEN '00' AND '23' OR substr(NEW.created_at,15,2) NOT BETWEEN '00' AND '59' OR substr(NEW.created_at,18,2) NOT BETWEEN '00' AND '59' OR json_valid(NEW.payload)=0 OR json_type(NEW.payload)<>'object' OR EXISTS(SELECT 1 FROM json_each(NEW.payload) WHERE key NOT IN('id','action','principal_category','principal_id','portfolio_id','review_id','snapshot_id','status','occurred_at','submitted_at','created_at') OR type IN('object','array')) OR (NEW.action='review_submitted' AND (NEW.entity_type<>'advisory_review' OR NOT EXISTS(SELECT 1 FROM advisory_reviews WHERE id=NEW.entity_id) OR NEW.retain_until<>(SELECT retain_until FROM advisory_reviews WHERE id=NEW.entity_id))) OR (NEW.action='feedback_submitted' AND (NEW.entity_type<>'review_feedback' OR NOT EXISTS(SELECT 1 FROM review_feedback WHERE id=NEW.entity_id) OR NEW.retain_until<>(SELECT retain_until FROM review_feedback WHERE id=NEW.entity_id))); END;
CREATE TRIGGER phase_d_audit_scope_guard BEFORE INSERT ON audit_event_portfolios WHEN EXISTS(SELECT 1 FROM audit_events WHERE id=NEW.audit_event_id AND action IN('review_submitted','feedback_submitted')) BEGIN SELECT RAISE(ABORT,'invalid Phase D audit owner') WHERE NEW.retain_until<>(SELECT retain_until FROM audit_events WHERE id=NEW.audit_event_id) OR NOT EXISTS(SELECT 1 FROM audit_events a LEFT JOIN advisory_reviews r ON a.action='review_submitted' AND a.entity_id=r.id LEFT JOIN review_feedback f ON a.action='feedback_submitted' AND a.entity_id=f.id WHERE a.id=NEW.audit_event_id AND ((r.id IS NOT NULL AND r.portfolio_id=NEW.portfolio_id) OR (f.id IS NOT NULL AND f.portfolio_id=NEW.portfolio_id))); END;
CREATE TRIGGER phase_d_activity_guard BEFORE INSERT ON activity_events WHEN NEW.category IN('review','feedback') OR NEW.action IN('review_submitted','feedback_submitted') BEGIN SELECT RAISE(ABORT,'invalid Phase D Activity') WHERE NEW.source_audit_event_id IS NULL OR (NEW.category='review' AND NEW.action<>'review_submitted') OR (NEW.category='feedback' AND NEW.action<>'feedback_submitted') OR NEW.retain_until<>(SELECT retain_until FROM audit_events WHERE id=NEW.source_audit_event_id) OR NOT EXISTS(SELECT 1 FROM audit_event_portfolios p JOIN audit_events a ON a.id=p.audit_event_id WHERE p.audit_event_id=NEW.source_audit_event_id AND p.portfolio_id=NEW.portfolio_id AND p.retain_until=NEW.retain_until AND a.action=NEW.action AND a.entity_id=NEW.entity_id); END;

CREATE TRIGGER phase_d_audit_update BEFORE UPDATE ON audit_events WHEN OLD.action IN('review_submitted','feedback_submitted') BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_audit_delete BEFORE DELETE ON audit_events WHEN OLD.action IN('review_submitted','feedback_submitted') BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_audit_scope_update BEFORE UPDATE ON audit_event_portfolios WHEN EXISTS(SELECT 1 FROM audit_events WHERE id=OLD.audit_event_id AND action IN('review_submitted','feedback_submitted')) BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_audit_scope_delete BEFORE DELETE ON audit_event_portfolios WHEN EXISTS(SELECT 1 FROM audit_events WHERE id=OLD.audit_event_id AND action IN('review_submitted','feedback_submitted')) BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;

CREATE TRIGGER phase_d_browser_principals_update BEFORE UPDATE ON browser_principals BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_browser_principals_delete BEFORE DELETE ON browser_principals BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_snapshots_update BEFORE UPDATE ON review_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_snapshots_delete BEFORE DELETE ON review_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_snapshot_holdings_update BEFORE UPDATE ON snapshot_holdings BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_snapshot_holdings_delete BEFORE DELETE ON snapshot_holdings BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;

CREATE TRIGGER phase_d_review_submission_permission_snapshots_update BEFORE UPDATE ON review_submission_permission_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_submission_permission_snapshots_delete BEFORE DELETE ON review_submission_permission_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_permission_snapshots_update BEFORE UPDATE ON feedback_permission_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_permission_snapshots_delete BEFORE DELETE ON feedback_permission_snapshots BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_append_only_update BEFORE UPDATE ON advisory_reviews BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_append_only_delete BEFORE DELETE ON advisory_reviews BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_append_only_update BEFORE UPDATE ON review_feedback BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_append_only_delete BEFORE DELETE ON review_feedback BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_targets_update BEFORE UPDATE ON review_targets BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_targets_delete BEFORE DELETE ON review_targets BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_risks_update BEFORE UPDATE ON review_risks BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_risks_delete BEFORE DELETE ON review_risks BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_counterarguments_update BEFORE UPDATE ON review_counterarguments BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_counterarguments_delete BEFORE DELETE ON review_counterarguments BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_conditions_update BEFORE UPDATE ON review_conditions BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_conditions_delete BEFORE DELETE ON review_conditions BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_metrics_update BEFORE UPDATE ON review_metrics BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_metrics_delete BEFORE DELETE ON review_metrics BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_evidence_update BEFORE UPDATE ON review_evidence BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_evidence_delete BEFORE DELETE ON review_evidence BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_sourced_facts_update BEFORE UPDATE ON review_sourced_facts BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_sourced_facts_delete BEFORE DELETE ON review_sourced_facts BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_fact_evidence_update BEFORE UPDATE ON review_fact_evidence BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_fact_evidence_delete BEFORE DELETE ON review_fact_evidence BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_idempotency_update BEFORE UPDATE ON review_idempotency BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_review_idempotency_delete BEFORE DELETE ON review_idempotency BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_idempotency_update BEFORE UPDATE ON feedback_idempotency BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;
CREATE TRIGGER phase_d_feedback_idempotency_delete BEFORE DELETE ON feedback_idempotency BEGIN SELECT RAISE(ABORT,'Phase D rows are append-only'); END;

INSERT INTO _phase_d_assertion SELECT CASE WHEN (SELECT COUNT(*) FROM activity_events)=(SELECT n FROM _phase_d_activity_count) THEN 1 ELSE 0 END;
INSERT INTO _phase_d_assertion SELECT CASE WHEN (SELECT COUNT(*) FROM audit_event_portfolios p LEFT JOIN audit_events a ON a.id=p.audit_event_id WHERE a.id IS NULL)=0 THEN 1 ELSE 0 END;
INSERT INTO _phase_d_assertion SELECT CASE WHEN (SELECT COUNT(*) FROM pragma_foreign_key_check)=0 THEN 1 ELSE 0 END;
DROP TABLE _phase_d_activity_count;
DROP TABLE _phase_d_assertion;
```

The activity replacement deliberately copies every old payload column, preserves valid NULL `source_audit_event_id` rows without creating an ownership row, and derives a deterministic seven-year boundary from the canonical `occurred_at` for rows whose audit owner has no boundary. A non-null source is associated with the activity portfolio before the copy, and its audit boundary is copied exactly. Missing or multi-portfolio source associations abort through `_phase_d_assertion`; the old indexes/triggers are explicitly removed because SQLite removes them with the dropped table, then recreated on the installed table. The row-count assertion references the saved pre-drop count in the authoritative script. The body leaves the outer transaction open; `store.go` performs the post-body migration-record insert and commit, while any `RAISE(ABORT, ...)` causes the migrator to roll back the complete outer transaction, leaving the old schema/data and no 008 row.

## 7. Immutable attribution and normalized reconstruction

`advisory_reviews` stores `submitted_by_id`, fixed `submitted_by_kind=machine`, and required `submitted_by_primary_permission=reviews:write`; its insert guard requires the same-portfolio principal to be active and have `permission_reviews_write=1`. `review_feedback` stores `submitted_by_id=local-owner`, fixed `submitted_by_kind=browser_user`, and required `submitted_by_primary_permission=portfolio:read`. These primary columns make zero additional rows valid while guaranteeing a non-empty wire permission array. Review additional permission `portfolio:read` is the only allowed child permission and its insert guard requires the same active machine principal's `permission_portfolio_read=1`; `reviews:write` is never claimed as an additional permission. Feedback additional permission is exactly `portfolio:read` for the stable `local-owner` browser principal. Unknown, disabled, wrong-principal, inactive, expired, foreign-portfolio, duplicate, or gapped claims are rejected. The normalized additional tables contain only their immutable owner key, ordinal `1`, and permission. There are no duplicated child principal ID or kind columns. Their owner foreign keys, vocabulary checks, uniqueness, ordinal check, primary-permission exclusion triggers, and append-only triggers reject wrong owner, wrong kind/ID, invalid/duplicate/gapped permission, update, and delete probes. The application transaction copies only the already-authorized effective permission set into the parent/child snapshot rows before the parent is visible; replay reads only those immutable rows and never reads current principal configuration.

Reconstruction reads the parent, emits the primary permission first, then additional rows ordered by ordinal, and maps all other child arrays by their ordinals. Review evidence reconstructs each URL exactly as `https://` + `url_host` + `url_suffix`; no full URL, port, or mutable principal table is consulted. Feedback reconstruction preserves `note:null` versus each present 1–2,000-character string exactly. The complete and incomplete output branches, same-key fingerprint comparison, `201` replay, `Location`, and `409` obligations remain those declared in the OpenAPI contract.

The canonical timestamp predicate is the full length/GLOB/dot/Gregorian-date/hour/minute/second expression shown in every Phase-D timestamp-bearing table and installed Activity table. It rejects invalid Gregorian dates, leap days, `9999-99-99T99:99:99.000000000Z`, offsets, missing or non-nine-digit fractions, and out-of-range time fields while accepting valid leap-boundary values. Go `net/url` must apply the exact lowercase-host/no-port/no-fragment grammar before the storage transaction.

## 8. Retention, append-only classes, and verification matrix

Every immutable parent and child, idempotency row, Phase-D audit association, and Phase-D Activity row has a non-null boundary at least seven years after its operation; children inherit the parent's boundary through owner equality guards. Existing mutable operational `machine_principals` configuration is intentionally not an immutable attribution source: only its explicitly administrative lifecycle/permission fields may change through the authenticated administration path, and no other Phase-D row is mutable. The authoritative append-only checklist is exactly 20/20 classes, each with explicit BEFORE UPDATE and BEFORE DELETE coverage: (1) browser_principals, (2) review_snapshots, (3) snapshot_holdings, (4) advisory_reviews, (5) review_targets, (6) review_risks, (7) review_counterarguments, (8) review_conditions, (9) review_metrics, (10) review_evidence, (11) review_sourced_facts, (12) review_fact_evidence, (13) review_submission_permission_snapshots, (14) feedback_permission_snapshots, (15) review_idempotency, (16) feedback_idempotency, (17) review_feedback, (18) Phase-D audit_events, (19) audit_event_portfolios, and (20) Phase-D activity_events. The verifier attempts UPDATE and DELETE for every class; only the documented machine-principal lifecycle path is intentionally mutable.

The verifier extracts this single SQL body verbatim, runs migrations 001–007, seeds one canonical legacy Activity row with NULL source and one with a non-null source plus its audit row, begins the same outer transaction as `store.go`, executes the body, performs the same post-body migration-record insert, commits, and requires exactly one 008 row with no nested-transaction or duplicate-record error. It separately runs every declared abort path inside that outer transaction and requires rollback to the old schema/data with no migration record. It requires the installed `activity_events` columns/FK/indexes/triggers, no `activity_events_new`, preserved old columns/payloads and row count, exact non-null association boundary, preserved null source, migration record, and empty `foreign_key_check`. It then runs valid review and feedback transactions through audit association and Activity, and reruns owner, retention, attribution permission matrix (every allowed permission enabled/disabled plus unknown, wrong principal, inactive/expired, foreign portfolio, duplicate/gapped ordering and exact reconstruction), zero-additional, wrong-kind/ID, URL wire/storage boundary (label 1/63/64, host 253/254, 2048/2049 and all forbidden URL forms), feedback timestamp Gregorian/time matrix, deferred witness branch/scope matrix (including 1 and 100 targets), decimal/date, HTTP/error/header/Allow/Location/challenge, lifecycle, pagination, replay, and all 20/20 mutation probes. OpenAPI is parsed, all references/examples/branches and seven-operation response rows are validated, and cached Redocly 2.62.1 is run when available; blocked threat-intelligence access is reported honestly rather than claimed as a pass.

A complete reconstruction is field-for-field from normalized rows only: parent scalars and timestamps, scope targets, three text arrays, metrics, component URL evidence, sourced facts and ordered links, model provenance, immutable attribution, lifecycle, idempotency, audit association, and Activity. No `snapshots.payload`, audit payload, opaque snapshot, current principal, full URL, or external store is a response source. The exact verification record must report the local commit SHA, commands, counts, blocked tools if any, exactly the two allowed changed files, `git diff --check 179f60ebc79d8f18f0ef97b803159d560aba072f..HEAD`, and a clean worktree.

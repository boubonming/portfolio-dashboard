# Independent portfolio review runner

This directory is a versioned, review-only external runner. It does not run an agent in the dashboard application or image, mutate holdings, or change existing briefing/alert jobs.

## Coverage contract

- `prepare` snapshots and enumerates every active lot. Each lot is a distinct opaque `target_key`; same symbols and same instruments are not merged.
- `draft` consumes an object keyed by `target_key`, with one public-source evidence bundle per target. Research each stock/ETF/crypto independently from relevant primary filings, results, and material developments. There is no six-holding/source cap; bounded batches must resume against the persisted manifest.
- Each target receives its own holding scope, deterministic metrics, model opinion, evidence, idempotency key, pending body, progress record, and exact-ID read-back. A failed target leaves other target payloads and receipts intact.
- A missing/stale/invalid target quote makes only that target's price/valuation conclusions unavailable. Its qualitative business, thesis, disclosure, and risk discussion remains explicit. The runner emits `incomplete`/`no_recommendation` for that target rather than inventing a price or recommendation.
- A complete portfolio review still requires the immutable snapshot to be globally complete and never computes partial-denominator weights or returns. A holding review may be complete when that target's own source quote and source value are fresh, even if unrelated holdings or reporting-currency conversion are unavailable.
- `coverage_complete` is true only after the manifest exactly matches the immutable snapshot target set and every target is terminally reconciled as `verified` or explicitly `unsupported`. `successful_review_complete` is true only when every target is `verified`; an unsupported target is accounted for but never misrepresented as a successful full sweep. `coverage_reconciled` also reports exact membership when deferred or failed outcomes are present, but those outcomes still require resume/reconciliation before coverage is complete.
- Existing receipts are untrusted durable hints: the runner validates target, run, pending idempotency key/body hash, snapshot/scope identity, review status, and receipt schema, then performs exact-ID read-back before marking a target verified. A malformed, forged, stale, or read-back-mismatched receipt never establishes coverage.
- An explicitly unsupported instrument type (anything outside stock/equity/ETF/crypto when an asset type is supplied) is persisted as `unsupported` without model or research work. Missing or stale prices do not make an otherwise supported target unsupported; that target receives qualitative analysis with `incomplete`/`no_recommendation`.

## Safe installation and activation (parent-owned)

Do not activate this runner from the worktree. The parent should first review the local commit and install this directory as a dedicated root-owned or otherwise least-privilege automation directory, preserving mode `0700` for the private run root and `0600` for pending/attempt files. Copy the versioned `review-schema.json` beside the runner. Keep `config.json`, credentials, private holdings notes, and run artifacts outside this repository and do not check them in.

Recommended parent sequence:

```text
1. Install the reviewed runner and schema into a dedicated automation path.
2. Create a private config.json with the existing verified private origin, resolve IP,
   local AI endpoint/model, and submission_enabled=false.
3. Run: portfolio_dashboard_review.py preflight
4. Run: portfolio_dashboard_review.py prepare --run-id <opaque-run-id>
5. Collect public evidence independently for every target_key in runs/<run-id>/manifest.json.
6. Run: portfolio_dashboard_review.py draft --run-id <opaque-run-id> --evidence <private evidence-map.json>
7. Inspect every target outcome and every pending body locally; do not approve partial coverage.
8. For an explicitly approved trial only, enable the existing submission gate and run submit.
9. Verify every receipt, exact target scope, review status, and server read-back before enabling
   any recurring schedule through the parent-owned operational process.
```

The evidence map is an object whose keys are the manifest's opaque target keys and whose values are arrays of public evidence objects. It must not contain quantities, costs, accounts, private notes, credentials, or derived portfolio weights. The runner's only dashboard writes are review POSTs guarded by durable target-specific idempotency keys; transport remains nonredirecting and restricted to the configured private dashboard origin. The local model path remains the existing idle/admission-guarded endpoint with no remote fallback.

Retries reuse an existing exact `pending.json` and `attempted.json` unchanged. Never edit those files after a POST may have committed; inspect the target's read-back or replay the same key. A changed snapshot, body, target membership, metric, or key stops submission and requires a fresh run.

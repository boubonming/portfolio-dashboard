# Portfolio Dashboard — bounded human-usability remediation plan

**Planning/audit only. Not product completion or implementation authorization.**
**Executive verdict: usable with material friction.**
**Recommendation:** repair the existing holdings-first application in bounded vertical slices, starting with **confirmed Add/Archive outcomes and safe recovery**, then routine interpretation and responsive navigation. Keep its exact-decimal accounting, immutable lots, honest partial data, same-origin/session/CSRF controls and existing APIs. A new shell/framework, catalog service, partial mapping endpoint or trading workflow is not justified.

The application already does important things correctly: Holdings is the default, P/L is server-owned and exact, incomplete reporting totals/allocation are withheld, same-symbol lots retain identity, archive is explicitly confirmed/logical, and reviews separate advisory evidence/opinion from human feedback. Those strengths do not compensate for losing the confirmation of a completed holding change, blank destinations after read failure, or unusable narrow-screen orientation.

## Canonical contract and project context

- Shared interface contract and **only acceptance ledger**: [`../../plan/references/user-journey.md`](../../plan/references/user-journey.md). Its J0–J7 storyboards, F01–F10 findings and U-01–U-23 checks are normative proposed behavior. Copy relevant acceptance items **unchanged** into coder/reviewer briefs; do not invent a competing ledger in this plan.
- Worktree `/opt/data/projects/portfolio-dashboard/.worktrees/human-usability-audit`, branch `plan/human-usability-audit`, base **`f4e84d1993cadd7bd89a7e57bffabf13c838e56f`**. Initial status clean. Primary checkout is owner-dirty/stale and was not touched; no other worktree modified.
- Existing canonical whole-project note inspected, **not edited**: `/opt/data/obsidian-vault/projects/01-Active/Portfolio-Dashboard/Implementation Plan.md`. Exact task-requested repository artifact paths take precedence for this package. Orchestrator may later link the accepted audit from the vault; no third competing roadmap is created here.
- Existing separate `/opt/data/obsidian-vault/projects/01-Active/Portfolio-Dashboard/Purchase User Journey.md` is a reviewed **proposal awaiting owner acceptance**, not a shipped capability or authorization. It already covers another-purchase selection/exact product/recovery. Do not duplicate it or make its unaccepted features prerequisite to fixing current defects.
- Canonical operations constraints read at `/opt/data/skills/software-development/portfolio-dashboard-operations/SKILL.md`. Use current code/OpenAPI for superseded historical details. In this exact base quantity/unit cost each support 18 fractional digits; exact cost basis supports 36. No floats, symbol-based identity joins, Markdown imports, trades, deployment changes, secrets or private portfolio examples.

## Evidence, facts and limitations

### Verified facts

- Read actual registration and contracts: `frontend/src/main.tsx`, `App.tsx` and all major workflow components/styles/theme; `internal/httpserver/router.go`, market workflow routes, review query parser; holding and mapping Store constraints; domain readmodels; strict config parser; `api/openapi.yaml`; component/API test assertions and `frontend/package.json`.
- Six state-based views, **not six URL routes**: Holdings (initial), Overview, Activity, Reviews (visible current tab says Review), Market Data, Settings. Add/archive/thesis/review detail are interaction states inside these views. Server APIs exist as enumerated in the contract; no route names are fabricated.
- Existing committed frontend assets are present at base: `internal/web/dist/index.html`, `/assets/index-RjgkmqNl.js`, `/assets/index-BGK5dROE.css`. Served those bytes read-only through a loopback-only static server; isolated browser fetch fixture supplied synthetic responses. No build/install/migration/test run or real backend was used.
- Fixture reproduced: exact-lot archive success/focus disappears on overview reload; valid-format complex cron shown as Daily/blank time; Settings header-only after initial overview 500; selected Settings tab still displays open thesis; tiny positive exact P/L rendered as zero; source quote time omitted; 320px document/table orientation overflow. Details/measurements and source references are in the contract.
- Source also confirms: mapping late discovery lacks owner-edit guard; mapping success resets itself via load; stale-setup recovery has no normal Reload setup action; incorrect validation-key DOM focus mapping; review Back label disagreement; no UI review status/outdated filters despite existing backend support; evidence IDs not navigable to entries and publication date not displayed.
- Store holding replay occurs after actor verification but **before** stale-revision comparison. Mapping batch currently **must cover every active instrument**; preserve that rule. Review `outdated=false` is not equivalent to verified Current review. Scheduled refresh is separate from Market Data and saved disabled settings override environment bootstrap defaults.

### Unverified / assumptions

- Orchestrator could not resolve private deployment host; no protected live audit. This audit does not independently certify a production image/revision, real holdings, provider coverage, successful credential configuration, scheduling execution, database state or human user-study results.
- Earlier runtime is the tracked bundle plus explicitly synthetic fetch responses, **not** compiled-current-source or real auth/store integration. This continuation recovered/read those recorded observations and independently rechecked the static login surface (HTTP 200 page, auth API 404, Username focus). No credentials entered. Test sources are evidence of intentions/assertions, not a claim they passed. No duplicate holding, real 5xx-after-commit or provider call was reproduced.
- Temporary planning evidence: `/opt/data/profiles/planner/cache/browser-use/workspace/46bb27c3-75a7-4424-a7a7-0da02642ae00/observations.json`. Selected output excerpts and reproduction boundary are now preserved in the contract; downstream review/release must not depend on cache retention. Earlier screenshots were not recovered and are not cited as evidence. The browser-emulation measurements differ; treat overflow as observed but do not treat one numeric width as a stable device baseline.
- Assumed audience is one private human owner, not an administrator who knows internal instrument/account IDs. Preserve existing routes/data ownership; no new user/account system is needed. Conditional error frequency is unknown; priorities reflect consequence and reproducibility, not invented analytics.

## Scope, non-goals and minimum useful outcome

**Required:** remedy confirmed task/recovery/interpretation defects, cover all audited workflows in the storyboard, preserve functioning behavior and verify human use at desktop/320px/keyboard. Each delivered slice must visibly improve a real task, not just reorganize components or add test infrastructure.

**Minimum first outcome:** after Add holding or Archive holding, the owner can tell that the exact change was recorded even when reading Holdings fails, can retry the read without repeating the write, and can safely resolve an unconfirmed write with the same request identity.

**Non-goals:** full visual redesign, new theme/light mode, scenario planning, new database/service, broker/trading integration, import/sync, mutation editing/restore, total-paid/fees/tax/dividend accounting, catalog/account pickers, durable cross-session drafts, deep-link router, review generation endpoint, schedule next-run service, historical refresh-run API, live credentials/provider validation or deployment. Refactoring is only in service of the bounded visible outcome. Do not revive the retired redundant machine-refresh permission gate.

## Meaningful alternatives and trade-offs

1. **Keep current application / documentation-only guidance.** Lowest change risk; acceptable for cosmetic preferences. Rejected as the main response: instructions cannot recover a lost receipt/focus, repair blank views or make a valid custom cron represent itself correctly.
2. **Incremental repair using current components/APIs — recommended.** Small reviewable patches, no migration presumed, retained tested safety semantics. Costs: App.tsx is a coordination hotspot; real provider/private-deployment verification remains separate. Draft/attempt/receipt lifetime must be repaired, not merely a banner added to the unmounting component.
3. **Broad mobile redesign, partial mapping API, new catalogs or durable request-status system.** Might improve future workflows but expands backend/security/identity contracts without current authorization. Deferred. In particular whole-batch mapping difficulty must first get useful progress/recovery guidance, **not** an unsupported client-side subset save.

Tiny-number presentation choice: compact signed less-than threshold + exact-amount toggle is less disruptive than replacing every row with long strings or misleading rounded zero. Preserve exact arithmetic/sort/input/receipt strings; this is display only. Use explicit UTC now; timezone preference/conversion is not necessary to fix missing timestamps.

## Ordered bounded slices (proposals for orchestrator, not dispatched tasks)

**No parallel cards editing App.tsx.** Sequence shared-shell work; component-contained Settings/Market/Reviews work can follow independently once orchestrator reserves any App integration. Every cycle includes implementation self-test and independent reviewer challenge. No PR/publication/deployment is implied. IDs refer to the single contract ledger.

### S1 — Trustworthy holding change completion and unresolved retry barrier (mandatory, P0; FIRST)

- **Visible outcome:** exact-lot **Holding added / Holding archived** receipt survives table reload; failed GET offers **Retry loading holdings**, not another mutation. An uncertain write remains an explicit same-attempt recovery state.
- **Scope:** shared create/remove submit/lifetime flow only; freeze unresolved body/key/If-Match/lot, bind session/scope, independent confirmed receipt/read states, accept newer valid overview revisions, correct error classes, session privacy teardown. Keep existing Add fields and archive menu/confirmation. Fix field-target focus with unique dialog IDs/scoped refs (including the outer Reporting currency ID collision) and units in existing archive copy while touching these paths; no new picker/form product.
- **Likely paths:** `frontend/src/App.tsx` load/render/lifetime integration; `HoldingMutations.tsx`; `App.test.tsx` **full-App** mutation scenarios; `HoldingMutations.test.tsx`. Existing `api.ts` only if a small shared error representation is needed, not blanket remote-body display. No server/schema change assumed.
- **Evidence/gap:** F01/F09; H-only tests provide synthetic onRefresh that does not exercise App's conditional unmount. `internal/store/holding_mutation.go` already provides authenticated replay-before-stale ordering and immutable receipts. Do not weaken that to satisfy UI.
- **Acceptance:** U-05/06/07/08/21 + relevant U-23 holding journey. Regression: CSV/session/scope and same-symbol archive tests; run real isolated backend mutation/idempotency tests in implementation, including lost-response-after-commit simulation.
- **Dependency:** none; reuses existing APIs. If verified receipt reconstruction fails, block that behavior rather than inventing absence proof/status endpoint. **Risk:** stale authority resurrection or duplicate writes; mitigated by immutable session-bound owner and real request assertions. **Rollback:** UI revert only after resolving any uncertain in-memory attempts; never undo recorded lots/audit/snapshots. Existing create/delete contracts unchanged.

### S2 — Honest daily Holdings interpretation (mandatory, P1)

- **Visible outcome:** owner sees valuation status/time, original/reporting currency, source market time and useful next action; tiny gains/prices are not false zero. First-run and no-match empty states differ.
- **Scope:** F03/F04 display/context, exact toggle, explanatory price-vs-cost cue, empty/filter clear, quality labels with raw detail. Keep current server withholding and exact sort. No new financial calculation or sector/review-column roadmap.
- **Likely paths:** `App.tsx` row/toolbar/status render; `App.test.tsx` formatter/financial-state assertions; `styles.css`; `theme.ts` only if needed for preserved readable focus/non-color cues. `internal/domain/readmodel.go` already carries market_at via freshness; frontend types already declare it. No data-contract enrichment presumed.
- **Acceptance:** U-04/09; relevant U-03/10/23 and existing exact P/L/incomplete fixtures. Synthetic tiny and large decimals, missing/stale/future/invalid, own-lot valid/unrelated gap must retain intended differences.
- **Dependency:** schedule after S1 to avoid App collision; no backend prerequisite. **Risk:** accidentally changing accounting/semantic sign or making snapshot-complete mean currently fresh. **Rollback:** presentation only, retain API decimals/history; do not restore false zero as an acceptable final state.

### S3 — Responsive shell and navigable tables (mandatory, P1)

- **Visible outcome:** at 320px all view choices/actions can be discovered; no document-wide sideways scrolling; horizontal table scan preserves instrument/lot identity and keyboard access.
- **Scope:** F02 actual MUI responsive layout diagnosis, control/header reflow, explicit named focusable bounded table regions, sticky instrument context, safe action/menu focus. No card-view rewrite or optional decoration.
- **Likely paths:** `App.tsx`, `styles.css`, `theme.ts` (only if root cause), `MarketDataPanel.tsx` mapping scroll semantics, `App.test.tsx`, `MarketDataPanel.test.tsx`, `HoldingMutations.test.tsx`. Inspect generated styles at browser width rather than assuming source `sx` guarantees wrapping.
- **Acceptance:** U-02/03/21/23; use Boot's ProductionProviders + real stylesheet, long synthetic labels, all views and real browser measurement. JSDOM style-selector assertions do not establish absence of overflow. Do not use overflow:hidden to conceal inaccessible content.
- **Dependency:** after S2 for App serialization; can precede S2 if orchestrator reprioritizes narrow-screen need, but S1 remains first recommended. **Risk:** sticky cells obscure columns/focus, portal menu clipping/regressions. **Rollback:** limited layout revert, no data migration; retain explicit accessibility defects in report if not closed.

### S4 — Recoverable view navigation and session entry (mandatory, P1)

- **Visible outcome:** Settings/Reviews never silently blank after portfolio read failure; a selected destination corresponds to content; thesis changes are not silently lost; review Back says where it goes.
- **Scope:** F06. Gate dependent views with explicit pending/error/read retry; correct Back to holdings and content jump target; preserve returned focus. Clean thesis can leave; dirty thesis gets inline Keep editing / Discard changes and leave (not a new page); pending save makes no undo promise. Preserve J1b's existing thesis form/history; correct its false not-saved assertion after an unconfirmed PUT and retain unchanged document/expected_revision replay while resolving it (existing Store request-hash contract, not a new key API). Small session-bootstrap Retry session check action and accurate error copy from J0; no auth changes.
- **Likely paths:** `App.tsx`, `ThesisEditor.tsx` (dirty/pending signal/navigation contract if needed), `Reviews.tsx` prop/copy; `App.test.tsx`, `ThesisEditor.test.tsx`, `Reviews.test.tsx`. Exact dirty/pending callback signature is implementer detail; parent/child must agree and preserve existing async lanes.
- **Acceptance:** U-01/11/12/15-navigation/23 plus existing thesis conflict/history/session teardown and `internal/store/thesis.go` replay tests. Test failures before data ever loads, not just reload failures with cached data. Generic server failures get truthful outcome copy, not invented field-level or not-saved claims.
- **Dependency:** S1/S3 shared shell integration; no new routes. **Risk:** bypassing dirty protection or losing focus through remount. **Rollback:** frontend-only; preserve saved theses/reviews. In-memory unsaved changes cannot be migrated retroactively.

### S5 — Schedules represent themselves truthfully and recover clearly (mandatory, P1)

- **Visible outcome:** valid custom cron remains editable as Custom, Off is recognizable, error/degraded state has a working Reload schedule and stops saying Loading.
- **Scope:** F05, strict simple-preset recognition, returned canonical form synchronization, explicit disabled template vs saved values, summary with timezone/currency/default semantics, read/error state, GET reconciliation after uncertain PUT, explicit stale-setting adoption. Preserve strong ETag and saved-disabled precedence. Move raw revision/ETag secondary, not removed.
- **Likely paths:** `SettingsPanel.tsx`, `SettingsPanel.test.tsx`; `api.ts` only for verified response contract adjustments (none assumed). Backend reference/tests: `internal/config/config.go`; `internal/httpserver/market_workflow.go`; `schedule_acceptance_matrix_test.go`; `internal/store/market_refresh_schedule_test.go`; scheduler runtime tests. No migration or cron-library replacement.
- **Acceptance:** U-17/18/19 + U-02/23. Include step/list/range minute/hour, disabled empty expression and 200-degraded as well as 503; current tests cover basic presets and statuses but not these human recovery gaps.
- **Dependency:** no functional dependency on other slices; schedule App touch separately. **Risk:** changing timing merely by opening form or mistaking unsaved draft for runtime effective configuration. **Rollback:** revert UI only; saved schedule remains authoritative. Do not revert database schedules or restore environment timing behind the owner's back.

### S6 — Non-destructive mapping discovery and safe mapping-save recovery (mandatory, P1)

- **Visible outcome:** owner can finish explicit mappings without late discovery erasing work, knows why Save all mappings is disabled, sees durable-in-session save confirmation and can recover a stale setup.
- **Scope:** F07: touched-row/application-generation guard; identity edits clear confirmation; display returned distinguishing metadata; progress/next-unfinished control; pending edit freeze; separate success/read/discovery notices; Reload setup and discard-aware navigation; uncertain-save read reconciliation (no false exactly-once claim). Keep original whole-batch Store/API validation and confirmed committed rows.
- **Likely paths:** `MarketDataPanel.tsx`, `marketMappingSuggestions.ts` only if seed metadata needs it, `MarketDataPanel.test.tsx`, `api.test.ts`. Source server references `internal/store/market_workflow.go`, `internal/httpserver/market_workflow_http_test.go` and Store workflow tests for invariants, not anticipated production edits.
- **Acceptance:** U-13/14 + U-02/03/08/23. Defer discovery while user edits; valid/ambiguous/no-result/failure; exact-set save/412/malformed result/read failure/zero instruments. Never pass by dropping unresolved rows, auto-confirming or loosening response validators.
- **Dependency:** S3 scroll behavior and S4 navigation integration; component work can be prepared separately with orchestrator-controlled App integration. **Risk:** race between late discovery/save/current revision or losing unsaved draft on leave. **Rollback:** UI only; append-only mapping history retained, no deletion of newly saved revisions.

### S7 — Refresh outcome details and actionable Overview (mandatory, P1)

- **Visible outcome:** owner understands why usable lot values coexist with withheld total, where a provider/quote/FX gap is, and whether to retry a read, same refresh or fix mappings.
- **Scope:** F03 plus source-confirmed discarded validated refresh diagnostics (A:625–639). Retain last result/uncertain attempt in authorized session; View refresh details, safe category/name mapping, existing Market Data navigation; distinguish confirmed snapshot + failed overview read. Add Overview's existing-capability navigation/help and future-dependency wording. Do not create historical run endpoint or automatic refresh-on-tab.
- **Likely paths:** `App.tsx`, `api.ts` only to reuse existing validated result types, `App.test.tsx`, `api.test.ts`, `styles.css`; no server change expected. Reference existing canonical durable refresh service/idempotency tests in `internal/httpserver/market_workflow_http_test.go`, `internal/store/market_refresh_durable_test.go`, `internal/store/market_refresh_idempotency.go` and `internal/marketrefresh/service.go`.
- **Acceptance:** U-10/20 + relevant U-08/09/23. Complete/incomplete, configuration/provider failure, missing/stale/future/invalid quote/FX, post-commit lost response, GET-only recovery and stable same-key replay. Do not call every provider “ready” just because configured or claim live freshness from snapshot coverage.
- **Dependency:** S1's proven session/attempt patterns, S4 navigation, S6 setup labels. **Risk:** raw remote diagnostics leak secrets or symbol-only mapping misattributes data. Use locally mapped safe labels and authoritative setup instrument IDs/FX pairs; unknowns remain unknown. **Rollback:** read/disclosure UI revert does not undo snapshot/quote/audit history; settle uncertain attempts first.

### S8 — Find and understand the right advisory review (mandatory, P2)

- **Visible outcome:** owner finds incomplete/outdated analysis without paging blindly and connects a sourced fact to its actual evidence/publication date.
- **Scope:** F08 existing backend status/outdated filters with All/All defaults; exact-lot context, scope/filter validation; readable target with full ID secondary; evidence anchors/publication fallback; clear first-page reload for bad cursor. Correct feedback conflict copy so a new event is not described as editing/replacing earlier feedback; preserve existing unchanged-attempt retry. Preserve lifecycle/immutable detail validation, opinion/facts/metrics separation. Do not change recommendation policy or schedule analysis. Optional S9 controls are not part of this slice's required acceptance.
- **Likely paths:** `Reviews.tsx`, `Reviews.test.tsx`, `styles.css`; `App.tsx` only if not already handled plural Reviews/Back copy in S4. Existing `internal/httpserver/review_read.go`/`review_read_acceptance_matrix_test.go` and `api/openapi.yaml` are contractual references; no new filter endpoint/schema assumed.
- **Acceptance:** U-15/16-evidence + U-02/08/23. Include no-current snapshot with outdated=false; prevent labeling it Current, archived targets, same-symbol distinct lots and server-filter pagination.
- **Dependency:** S4 navigation; can be component-contained after S3. **Risk:** filtering only loaded page, incorrect Current classification or dangling evidence links. **Rollback:** frontend-only; stored immutable review/evidence untouched.

### S9 — Explicit feedback event/retry intent (OPTIONAL, P2 protection)

- **Visible outcome:** one confirmed feedback event is not accidentally recorded again by the same unchanged submit; changing an uncertain draft is explicitly a separate event, never an edit.
- **Scope:** R:247–248 clears attempt on draft edit; R:271/302 allows a fresh key after success without explicit new-event intent. Add same-body confirmed barrier/Record another feedback event and changed-uncertain warning; preserve unchanged-attempt replay across detail reopen, existing Agree default and append-only receipt semantics. No feedback history endpoint or external job launch.
- **Likely paths:** `Reviews.tsx`, `Reviews.test.tsx`. Existing `internal/store/feedback.go`, feedback HTTP/Store retry/acceptance tests establish append-only/idempotency behavior; no migration presumed.
- **Acceptance if adopted:** U-16 optional S9 subcheck + U-08 and feedback portion U-23. Confirmed unchanged second click cannot issue another POST; explicit separate-event action can. Uncertain unchanged retry retains exact body/key; changed draft requires warning acknowledgment. Conflicts/rejections do not silently rotate key or fabricate recorded status. Source establishes current repeat-event affordance, not evidence that an owner accidentally used it; do not make preference-level extra intent controls a blocker to S8 or the routine-use verdict.
- **Dependency:** S8 review surfaces; avoids parallel edits to Reviews.tsx. **Risk:** implying global no-duplicate feedback across sessions/reload; make memory boundary explicit, no durable claim. **Rollback:** retain already-recorded events; do not rewrite historical user decisions.

## Mandatory vs optional

**Mandatory program:** S1–S8 and shared invariant/human-task checks, excluding the explicitly optional U-16 S9 subcheck. S1 is useful on its own; no requirement to bundle every slice into one large change. Per-slice verification must disclose adjacent known defects not yet addressed rather than assert the whole application is fixed. The final routine-use verdict requires the combined U-23 task gate; it does not require adopting preference-only controls.

**Optional follow-ups, not blockers:**
- S9's explicit new-feedback-event guard and changed-uncertain-draft acknowledgment. Existing replay/authority/append-only semantics and truthful conflict copy remain mandatory.
- F10 friendly Activity actor/action labels and explicit UTC polish (U-22 remains a regression gate regardless). Server readable summaries already do the main job.
- Adopt the existing Add purchase design after owner acceptance; no parallel competing picker/account service. Current Add holding's internal-ID/manual-product friction is disclosed, not falsely solved by this audit. Reuse S1 recovery rather than implementing it twice.
- Default feedback to no decision rather than Agree; richer summary/filters; local-time presentation preference; light theme; URL deep links; table column customization. Preference alone is not severity P1.
- A future partial mapping contract, durable retry/draft store or historical refresh diagnostics would require separate API/identity/security design and acceptance, not silently added scope.

## Verification route for future implementation — NOT EXECUTED

The repository manifest has `typecheck`, `test`, `build`; **no `test:e2e` script**. Do not quote the old vault roadmap as proof of a browser runner. Toolchain/dependencies must be verified by coder in its authorized isolated workspace; this planner did not install them. Proposed commands, results expected but not claimed:

1. Focused frontend behavior: `npm --prefix frontend run test -- --run src/App.test.tsx src/HoldingMutations.test.tsx` for S1; substitute actual component test files for later slices. `npm --prefix frontend run typecheck`; `npm --prefix frontend run test -- --run` for full regression.
2. `npm --prefix frontend run build` **only during authorized implementation**, before Go embed-dependent gates. Do not accidentally include generated bundle changes in this planning package.
3. For unchanged backend contracts, run focused real Store/HTTP regression cases selected from inspected test files; then relevant full packages: `go test ./internal/domain ./internal/store ./internal/httpserver ./internal/scheduler -count=1`. Broader `go test ./...`, race, vet/build follow repository release ownership/toolchain policy. These can be long and need proper timeout; do not substitute passing mocks for server replay/isolation tests.
4. Real browser: isolated backend fixture with fake providers, or candidate-built UI with fully synthetic controlled responses. Use actual Boot providers/styles; 1280px and 320px, keyboard and focus, long content, delayed/reordered failures, confirm writes/readback. No production credentials/private records/provider traffic. Record fixture boundaries and whether actual persistence was exercised. Build/browser plumbing is a verification aid within each visible slice, not the first deliverable.
5. Reviewer reads exact candidate diff/contracts and independently exercises the designated U-checks, recording actual outputs and any remaining known defects. Human usefulness (discover/understand/complete/recover/recognize success) is a separate gate from passing component assertions.

## Risks, compatibility and rollback

- **Coordination hotspot:** `frontend/src/App.tsx` owns view selection, authenticated generation, overview lifetime, mutation mounting and routine refresh. Do not run several shell/lifetime cards in parallel. `Reviews.tsx` also combines validation/list/detail/feedback; serialize S8/S9. Extract only a small proven state owner/helper if necessary, not a speculative generalized workflow framework.
- **Precision/identity:** never solve compactness by floats, rounded payloads, grouped same-symbol lot mutations or inferred listing joins. Exact unit/account/date/unknown semantics stay server-validated. Quality/source timestamps remain explicit; unknown is not zero/complete.
- **Existing contracts:** browser-only schedule authority, machine review submission separation, exact-lot DELETE, strong holding If-Match/idempotency and setup all-instrument atomicity stay unchanged. No migration is expected; any discovered backend prerequisite must be raised narrowly with evidence to orchestrator before expanding scope.
- **Data privacy:** use synthetic examples only; provider query minimization and local-model/privacy boundaries remain. A remote model reviewing this plan does not make the entire review offline. No private portfolio was supplied here.
- **Rollback:** generally revert frontend slice; never delete snapshots/mappings/audit/feedback or reverse logical archival behind owner intent. UI rollback while a mutation is unresolved can destroy in-memory recovery; reconcile under unchanged authority first or document the limitation to owner. Deployment/image/database rollback decisions remain owner-controlled.

## Decisions and handoff

No material missing fact blocks the proposed bounded repairs. Owner feedback is still needed on the **proposed design**, especially exact-amount presentation and future Add purchase adoption; it is not a request to re-authorize existing safety fixes or a reason to invent extra infrastructure. Orchestrator owns acceptance, scope selection and dispatch.

**Planner role constraint:** this card requests a local planning commit, but governing planner policy prohibits Git commits. Only these two planning files were written. Do not fabricate a SHA or treat base HEAD as a planning commit. Independent review can verify file hashes and base; orchestrator must arrange the requested local commit of exactly these files before the **independently approved local planning commit** stop condition is satisfied. No push/PR/publication or production change is allowed by this package.

**Next handoff:** reviewer on the same card receives both exact paths/hashes, base SHA, source/runtime limitations and S1 as first recommendation. Reviewer assesses source correctness, scope, storyboards and shared acceptance reproducibility; bounded changes return to planner on this card. A reviewed plan is not a built product. After review, orchestrator presents the short journey for owner feedback and decides any follow-on implementation.

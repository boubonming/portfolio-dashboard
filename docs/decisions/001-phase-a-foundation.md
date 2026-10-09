---
title: Phase A foundation: holdings coverage and free market data
status: decision-record
reviewed: 2026-10-09
---

# Phase A foundation: holdings coverage and free market data

## Decision summary

This record freezes the Phase A defaults and records the market-data decision needed before scaffolding.

No single free provider is fully validated against this portfolio yet. The recommended MVP path is:

1. Use EOD Historical Data (EODHD) as the primary candidate for end-of-day equity, ETF, REIT and crypto quotes, subject to the key-gated symbol and plan checks in this record.
2. Use its exchange-symbol and search endpoints to validate every mapping before importing a quote.
3. Keep an explicit, sourced, dated manual quote override for any instrument or FX pair that the free account does not cover. An uncovered instrument remains unvalued; it is never assigned a fabricated or zero price.
4. Do not scaffold ingestion until the provider-key coverage check and the reconciled import preview have both been approved.

This is a personal, single-user, end-of-day/manual-refresh MVP decision. It does not authorize an account signup, credential access, deployment, or application implementation.

Evidence in this record was checked against official provider documentation on 2026-10-09. Provider plans, coverage and terms can change; the implementation must re-check the linked pages and record the result of its real key query.

## 1. Normalized coverage inventory

### Reconciliation rules

The authoritative source is `main/20-Personal/Investing/Portfolio.md`. It contains 33 source holding rows: 26 USD-denominated rows, two SGD rows, four MYR rows and one JPY row. NVDA and INTC each occur as two lots. The `NVDA total` and `INTC total` rows are aggregate presentations, not additional holdings, and are deliberately excluded from the inventory count. The normalized inventory therefore contains 31 distinct source symbols and 33 source lots/rows.

Source symbols, quantities and supplied costs are not copied or normalized here. The application import preview must preserve those values exactly and retain a source-note reference. The exchange and provider mappings below are hypotheses for identity validation, not replacements for the source symbols.

### Inventory

| Source symbol | Source rows | Asset type | Quoted currency | Likely exchange/market needing validation | Known ambiguity or validation note |
|---|---:|---|---|---|---|
| `NVDA` | 2 lots | US common equity | USD | US composite; likely NASDAQ | Two lots must reconcile to one instrument plus two opening-balance lots. Validate the exact US listing and provider suffix. |
| `INTC` | 2 lots | US common equity | USD | US composite; likely NASDAQ | Two lots must reconcile to one instrument plus two opening-balance lots. Do not count the aggregate row. |
| `MU` | 1 | US common equity | USD | US composite; likely NASDAQ | Validate US listing. |
| `AMD` | 1 | US common equity | USD | US composite; likely NASDAQ | Validate US listing. |
| `GOOG` | 1 | US common equity, Alphabet Class C | USD | US composite; likely NASDAQ | Preserve `GOOG`; do not silently substitute `GOOGL`. |
| `CNDX.L` | 1 | London-listed ETF | USD | London Stock Exchange (LSE) | User-confirmed USD quotation. Validate the provider's LSE code, instrument identity and whether the source's `.L` is a source notation or a provider suffix. |
| `ABNB` | 1 | US common equity | USD | US composite; likely NASDAQ | Validate US listing. |
| `IVV` | 1 | US ETF | USD | US composite; likely NYSE Arca | Validate ETF identity and US venue. |
| `MSFT` | 1 | US common equity | USD | US composite; likely NASDAQ | Validate US listing. |
| `KO` | 1 | US common equity | USD | US composite; likely NYSE | Validate US listing. |
| `ARM` | 1 | US-listed equity/ADR identity to confirm | USD | US composite; likely NASDAQ | Confirm whether the provider describes this as the US listing and preserve source identity. |
| `MCD` | 1 | US common equity | USD | US composite; likely NYSE | Validate US listing. |
| `UBER` | 1 | US common equity | USD | US composite; likely NYSE | Validate US listing. |
| `NTDOY` | 1 | OTC ADR | USD | US OTC market; underlying Nintendo listing in Japan | Validate OTC instrument, ADR ratio and corporate-action history. Do not replace it with the Japanese ordinary share. |
| `T.US` | 1 | US common equity | USD | US composite; likely NYSE | The source already includes `.US`; preserve the literal source symbol and query the provider's exact accepted form rather than stripping it silently. |
| `ETH` | 1 | cryptocurrency | USD | Crypto market / provider aggregate | Validate the provider's canonical crypto ID/pair and timestamp semantics; crypto is not an exchange-listed equity. |
| `DIS` | 1 | US common equity | USD | US composite; likely NYSE | Validate US listing. |
| `PTON` | 1 | US common equity | USD | US composite; likely NASDAQ | Validate US listing. |
| `XRP` | 1 | cryptocurrency | USD | Crypto market / provider aggregate | Validate canonical token identity and quoted pair. A symbol alone is not a sufficient token identity. |
| `SE` | 1 | US-listed ADR | USD | US composite; likely NYSE | Validate ADR identity and ratio; do not replace with a Singapore listing. |
| `U` | 1 | US common equity | USD | US composite; likely NYSE | Validate current listing and any symbol-history/corporate-action issue. |
| `BABA` | 1 | US-listed ADR | USD | US composite; likely NYSE | Validate ADR identity and ratio; distinguish from the Hong Kong ordinary listing. |
| `NIO` | 1 | US-listed ADR | USD | US composite; likely NYSE | Validate ADR identity and ratio; distinguish from the Hong Kong ordinary listing. |
| `BTC` | 1 | cryptocurrency | USD | Crypto market / provider aggregate | Validate canonical crypto ID/pair and timestamp semantics. |
| `D05` | 1 | Singapore common equity | SGD | Singapore Exchange (SGX) | Validate provider exchange code and exact symbol. The source symbol is preserved without a suffix. |
| `O39` | 1 | Singapore common equity | SGD | Singapore Exchange (SGX) | Validate provider exchange code and exact symbol. The source symbol is preserved without a suffix. |
| `1155` | 1 | Malaysian common equity | MYR | Bursa Malaysia / Kuala Lumpur (KLSE) | Validate the exchange suffix and instrument identity. |
| `1295` | 1 | Malaysian common equity | MYR | Bursa Malaysia / Kuala Lumpur (KLSE) | Validate the exchange suffix and instrument identity. |
| `5176` | 1 | Malaysian REIT | MYR | Bursa Malaysia / Kuala Lumpur (KLSE) | Validate REIT instrument type, exchange suffix and whether the provider calls it a common stock or REIT. |
| `5212` | 1 | Malaysian REIT | MYR | Bursa Malaysia / Kuala Lumpur (KLSE) | Validate REIT instrument type, exchange suffix and whether the provider calls it a common stock or REIT. |
| `5253` | 1 | Japanese common equity | JPY | Tokyo Stock Exchange (TSE/JPX) | Validate the provider's Japan exchange code, board and exact symbol. Preserve the source's numeric symbol. |

The inventory spans US, London, Singapore, Malaysia, Japan and crypto, with USD, SGD, MYR and JPY quoted values. FX coverage is a separate required inventory: at minimum USD/MYR, SGD/MYR and JPY/MYR (or explicitly inverted pairs) are needed to translate into the default reporting currency. FX pair identity, direction, quote timestamp and free-plan access must be validated independently of equity coverage.

## 2. Provider requirements and evidence

The MVP needs daily or manually refreshed end-of-day prices, explicit quote/fetch/market timestamps, original quote currency, exchange/instrument identity, symbol search, usable FX rates, and enough licensing permission for a private authenticated display. It must cover, or explicitly surface as missing, US, LSE, SGX, Bursa Malaysia, Japan and crypto. Corporate actions must not be hidden: the system needs a clear raw/adjusted basis and split/dividend metadata or a manual reconciliation path.

The following claims are limited to the linked official documentation. A provider's general marketing statement is not treated as proof that every holding is available on the free plan.

### EODHD (primary candidate)

- **Coverage and identity:** EODHD documents 150,000+ tickers across 70+ exchanges and provides exchange lists, per-exchange symbol lists and a search API: [quick start](https://eodhd.com/financial-apis/quick-start-with-our-financial-data-apis), [exchanges and ticker lists](https://eodhd.com/financial-apis/exchanges-api-list-of-tickers-and-trading-hours), and [covered tickers](https://eodhd.com/financial-apis/covered-tickers-eodhd). Its documented ticker form is `SYMBOL.EXCHANGE`, with examples `AAPL.US` and `BP.LSE`; `US` and `LSE` are explicitly shown in the exchange documentation. Its public exchange page documents Bursa Malaysia as `KLSE` and shows `5212.KLSE` in MYR: [KLSE exchange page](https://eodhd.com/exchange/KLSE). The official crypto documentation uses virtual exchange `CC` and pairs such as `BTC-USD.CC`: [crypto coverage](https://eodhd.com/financial-apis/list-supported-crypto-currencies). This supports a credible path for US, London, Malaysia and crypto, but SGX, the exact Japan code and every exact symbol remain key-query gates.
- **Free quota:** The free EOD plan documents 20 API calls per day and only the past year of history, with one symbol per EOD request: [EOD historical API](https://eodhd.com/financial-apis/api-for-historical-data-and-volumes) and [API limits](https://eodhd.com/financial-apis/api-limits). Exchange-symbol list, search and EOD requests are billed; failed unverified lookups are also billed. The implementation must enumerate and cache the relevant symbol lists before requesting prices and must not probe guesses in a loop.
- **Timestamps and FX:** The live/delayed documentation defines a Unix UTC `timestamp` and says stock prices are delayed 15–20 minutes, while the MVP will use completed EOD bars: [live delayed API](https://eodhd.com/financial-apis/live-ohlcv-stocks-api). The EOD endpoint supplies daily OHLC data; exact timestamp fields and the free-plan availability of the required FX endpoint must be checked with the real account. The FX candidate symbols must be validated before use; no inverse or zero-rate assumption is allowed.
- **Licensing/storage/display:** EODHD documents that it has direct exchange contracts for some EOD feeds and also uses market makers/aggregated sources for other data, and warns that data may not be accurate or real-time: [data sources and partners](https://eodhd.com/financial-apis/our-data-sources-and-data-partners). The pages consulted do not establish a complete free-tier right to display, retain or redistribute all exchange data in a private application. Before implementation, read the account's applicable terms and confirm that a private single-user authenticated display and the required local cache are permitted. Do not expose an EODHD response publicly or redistribute it.
- **Symbol search:** The official quick-start page identifies a Search API and the exchange-symbol list as the identity workflow. Use those endpoints to map each source symbol, record the returned provider symbol, name, type, exchange, currency and identity metadata, and retain the original symbol separately.
- **Corporate actions:** EODHD documents split/dividend endpoints and distinguishes raw, split-adjusted and fully adjusted prices: [EOD historical API](https://eodhd.com/financial-apis/api-for-historical-data-and-volumes) and [splits/dividends API](https://eodhd.com/financial-apis/api-splits-dividends). The provider's adjusted series must not be mixed with unadjusted cost-basis quantities. ADR ratios, ticker changes, delistings and ETF actions still need instrument-specific review.

### Marketstack (credible alternative, not selected as the primary)

- **Coverage and quota:** Its official documentation describes 70+ exchanges and 170,000+ tickers from more than 50 countries, while the free plan is 100 requests per month, EOD only and one year of history: [API documentation](https://docs.apilayer.com/marketstack/docs/api-endpoints-v1) and [pricing](https://marketstack.com/pricing). This is attractive for global stock/ETF discovery but does not document a crypto feed in the cited stock API material. The free quota is too small for an unverified 31-instrument plus FX workflow unless results are heavily cached.
- **Timestamps and identity:** The official EOD response documents an ISO-8601 UTC `date`, symbol, exchange MIC, raw and adjusted prices, split factor and dividend fields. The same documentation describes exchange and ticker endpoints. This is useful evidence for timestamp and corporate-action fields, but actual symbols for SGX, Bursa, Japan and the ADR/ETF cases still require a real account query.
- **Licensing/display:** The free plan is explicitly marked non-commercial on the official pricing page. It does not by itself establish permission to redistribute market data; private authenticated display and local retention must be confirmed in the applicable terms. It covers stock data, not the complete crypto requirement. It is therefore a fallback for equities only, not a complete provider.

### Twelve Data (credible alternative, free tier not suitable for this application)

- **Coverage and identity:** Twelve Data documents stocks, ETFs, forex and cryptocurrency pairs, exchange lists, symbol search and EOD/quote/time-series endpoints: [API documentation](https://twelvedata.com/docs). The documentation exposes exchange, currency and MIC metadata, timezone controls and cryptocurrency exchange lists. This is strong discovery evidence, but the exact source-to-provider mappings still require queries.
- **Free quota and display restriction:** The official pricing page states that Basic is free with 8 API credits per minute, 800 per day and “internal non-display usage”; global EOD equities/ETF display is listed under the paid Grow plan, and the free plan has only three markets: [pricing](https://twelvedata.com/pricing). The free tier therefore cannot be selected for a displayed portfolio dashboard without a separate permission or paid plan.
- **Licensing/storage:** The official terms allow access, processing and storage for internal use subject to the subscription tier, while external display or redistribution requires a permitted add-on or written agreement: [terms of use](https://twelvedata.com/terms). This is a clear reason not to use the free tier for the dashboard.
- **Corporate actions and timestamps:** Twelve Data documents dividends, splits, adjustment modes and exchange/local/UTC time-zone behavior: [fundamentals](https://twelvedata.com/docs/fundamentals), [splits](https://twelvedata.com/docs/fundamentals/splits), and [time series](https://twelvedata.com/docs/market-data/time-series). The application would still need to select and document raw versus adjusted data consistently.

### Alpha Vantage (credible alternative, not selected as the primary)

- **Coverage and quota:** Its official documentation advertises global stock/ETF/mutual-fund time series, symbol search and forex/crypto APIs: [documentation](https://www.alphavantage.co/documentation/). The official support page says the free service covers most datasets at 25 API requests per day; real-time and 15-minute delayed US data are premium-only: [support and limits](https://www.alphavantage.co/support/). This is compatible with a low-frequency EOD experiment, but the free quota and exact exchange mappings are not enough evidence for this portfolio without key queries.
- **Timestamps and corporate actions:** The documentation provides daily/weekly/monthly series and adjusted/raw choices; Alpha Vantage states that adjustments include splits and cash dividends and that raw data can also be requested: [support FAQ](https://www.alphavantage.co/support/) and [documentation](https://www.alphavantage.co/documentation/). Quote freshness and global exchange identity must be checked per response.
- **Licensing/display:** The official terms grant personal, non-commercial use and define use that permits other people/entities to access the information as commercial use; commercial use requires contacting Alpha Vantage: [terms of service](https://www.alphavantage.co/terms_of_service/). A private single-user display may fit the personal case, but the terms must be checked before any sharing or public route. Exact SGX, Bursa, Japan, London and crypto mappings are not treated as validated by the general global-coverage statement.

### Financial Modeling Prep (credible alternative, free plan is US-limited)

- **Quota and coverage:** The official pricing page gives Basic/free 250 calls per day and end-of-day data, but says the paid Starter plan adds US coverage and crypto/forex, Premium adds UK/Canada, and Ultimate adds global coverage: [pricing](https://site.financialmodelingprep.com/developer/docs/pricing). This makes the free plan unsuitable for the required multi-market and crypto inventory.
- **Licensing/display:** The same official page explicitly says displaying or redistributing FMP data requires a Data Display and Licensing Agreement. It cannot be selected for this dashboard's free MVP without that agreement.
- **Identity, timestamps and actions:** FMP has quote, reference, forex, crypto and historical endpoints in its official developer documentation, but free-plan entitlement, exact exchange coverage and display rights are decisive blockers. No mapping is approved from FMP.

### CoinGecko (crypto-only complement, not a complete provider)

- **Coverage and identity:** CoinGecko's official API offers coin IDs, symbols, names and platform/contract addresses, and its price endpoint can return a `last_updated_at` Unix timestamp: [coin list](https://docs.coingecko.com/reference/coins-list) and [simple price](https://docs.coingecko.com/reference/simple-price). This is useful for resolving BTC, ETH and XRP identity, but it has no equity exchange coverage and therefore cannot be the portfolio provider.
- **Quota and key handling:** The official authentication documentation requires a backend-held key for the Pro API and says successful requests consume monthly credits; the usage endpoint reports the plan's rate and monthly credits: [authentication](https://docs.coingecko.com/reference/authentication) and [usage](https://docs.coingecko.com/reference/api-usage). The public documentation consulted here does not establish a free quota suitable for the complete dashboard, so no free quota is assumed.
- **Corporate-action/licensing caveat:** Token symbol lookups can be ambiguous; use canonical IDs and contract/platform identity, not a bare `ETH`/`XRP` string. CoinGecko's crypto aggregate price is not an equity-style exchange close and must be labeled accordingly. Any attribution, storage and display requirements must be checked against the applicable plan terms before use.

## 3. Provider decision and quote policy

### Decision

EODHD is the primary free-provider candidate because its official documentation combines a free EOD endpoint, global exchange-symbol discovery, explicit US/LSE/KLSE evidence, crypto coverage and split/dividend endpoints. This is a candidate decision, not a claim that coverage is complete: the provider-key queries listed below are hard gates.

The evidence does not validate one free provider for every exact holding, every FX pair and every required display right. Until those gates pass, the system must treat the portfolio as partially valued. Any uncovered equity, ETF, ADR, REIT, crypto pair or FX pair receives a sourced manual quote override only when all of the following are recorded:

- provider/source name and direct source URL;
- exact source symbol or instrument identity;
- quote value and quote currency;
- market/as-of timestamp in UTC;
- retrieval timestamp in UTC;
- whether it is raw, adjusted, indicative or an exchange close;
- operator note explaining why the primary provider was not used; and
- audit identity and revision history.

Manual overrides are quotes, not silent imports or fabricated defaults. A missing quote remains missing, is excluded from complete totals, and is shown as a coverage warning. Never use zero as a substitute for an unknown price.

### Operational quote basis

The MVP uses completed end-of-day prices and manual refresh. A quote record must preserve source, market timestamp, retrieval timestamp, exchange/market, currency, raw/adjusted basis and freshness state. Weekends and exchange holidays are not automatically stale failures; the applicable exchange calendar must be considered. Current-FX translation is labeled as current-FX translation, not historical investment return, because the source note lacks dated transaction FX.

Corporate actions require a reconciliation decision before valuation: match splits, reverse splits, ticker changes, ADR ratio changes, ETF actions and delistings to the source quantity and cost basis. Until reconciled, block the affected valuation rather than silently adjusting the user's supplied lots.

## 4. Symbol mappings requiring a real provider-key query

No provider key was accessed for this decision record. The following queries must be run against the EODHD account before quote ingestion. The source symbol must remain unchanged beside the returned provider mapping.

### Equity, ETF and REIT identity queries

Query the relevant exchange-symbol list/search endpoint first; do not spend the 20-call daily allowance guessing individual EOD tickers. Confirm provider code, exchange, name, asset type, quote currency, MIC/venue where returned, active status and any ADR/ETF/REIT metadata.

- US composite candidates: `NVDA.US`, `INTC.US`, `MU.US`, `AMD.US`, `GOOG.US`, `ABNB.US`, `IVV.US`, `MSFT.US`, `KO.US`, `ARM.US`, `MCD.US`, `UBER.US`, `DIS.US`, `PTON.US`, `SE.US`, `U.US`, `BABA.US`, and `NIO.US`.
- Source value requiring literal-form validation: `T.US` (do not silently rewrite it; query the literal source form and the provider's documented US form, then record the accepted identity).
- London ETF candidate: `CNDX.LSE`; validate against the source `CNDX.L` and confirm USD quotation and ETF identity.
- OTC ADR candidate: `NTDOY` with its actual provider exchange suffix; confirm ADR ratio and distinguish it from the Japanese ordinary share.
- Singapore candidates: `D05.<provider-SGX-code>` and `O39.<provider-SGX-code>`; the exchange code and accepted tickers are not approved until the account query returns them.
- Malaysia candidates: `1155.KLSE`, `1295.KLSE`, `5176.KLSE`, and `5212.KLSE`; validate all four, including REIT type for `5176` and `5212`.
- Japan candidate: `5253.<provider-Japan-code>`; determine the provider's exact code and board, likely a Tokyo/JPX form, by query rather than assumption.

A search result that has the right company name but a different listing, ADR, fund share class or currency does not pass validation. The mapping must be approved per source symbol.

### Crypto identity queries

Resolve the source symbols to provider canonical identities and quote pairs:

- `BTC` -> provider canonical Bitcoin identity and USD pair;
- `ETH` -> provider canonical Ethereum identity and USD pair;
- `XRP` -> provider canonical XRP identity and USD pair.

For EODHD, the documented candidate form is `BTC-USD.CC`, `ETH-USD.CC` and `XRP-USD.CC`, but each must be confirmed in the current `CC` symbol list. Do not use a token symbol without identity metadata.

### FX identity queries

Validate exact direction and timestamp for at least:

- USD/MYR (candidate `USDMYR` or provider-documented equivalent);
- SGD/MYR (candidate `SGDMYR` or provider-documented equivalent); and
- JPY/MYR (candidate `JPYMYR` or provider-documented equivalent).

If the provider only returns inverse pairs, record the explicit inversion rule and precision. The implementation must not assume that `MYRUSD` is interchangeable with `USDMYR` without an explicit mathematical direction and timestamp.

### Least-privilege account/config requirement

If the free EODHD account is required, the operator must create only a free individual account and an API token with the provider's ordinary read-only API scope needed for exchange-symbol/search, EOD and permitted FX endpoints. No trading, brokerage, write, webhook or portfolio-upload permission is needed or requested. Store the token only in a server-side runtime secret/environment setting after implementation authorization; never put it in Git, the Obsidian vault, frontend assets, URLs, logs or this record. Do not request or expose the token in chat. The key must not be accessed during this documentation task.

Before using any provider data in the dashboard, the operator must confirm that the selected free plan permits the intended private authenticated display and local cache. If it does not, use a permitted provider/plan or retain sourced manual overrides; do not bypass terms.

## 5. Frozen Phase A defaults

These defaults are approved for implementation planning:

- **Reporting currency:** MYR is the default and must be changeable by the user. It is a presentation/reporting choice, not a conversion of the ledger.
- **Original currencies:** Retain each supplied holding, cost and quote in its original currency. Store the currency with every monetary value and preserve the source note's values and precision.
- **Time:** Store internal market, quote, fetch and review timestamps as timezone-aware UTC instants. Display human-facing dates and times in Singapore time (SGT, `Asia/Singapore`).
- **Refresh:** Use end-of-day market prices and an explicit manual refresh for the first release. Show source, as-of time and freshness; do not imply real-time coverage.
- **Incomplete data:** Unknown prices/rates remain unknown. Do not make partial coverage appear to be a complete total.
- **Deployment:** The user owns deployment. The project may supply a verified version-pinned image and minimal Compose bundle, but must not deploy, configure ingress, select a production hostname or change the home infrastructure on the user's behalf.
- **Access:** Keep the application private and authenticated, using the user's LAN/Tailscale deployment policy. No public portfolio page, brokerage login, trading credential or order-execution endpoint is part of this phase.
- **Pre-cutover authority:** Obsidian remains authoritative until a reconciled import preview is explicitly reviewed and approved. Do not modify the source note or silently correct its symbols, quantities, costs or currencies. After approval, the application ledger becomes authoritative and the original note/import backup remains traceable.
- **Provider budget:** No paid dependency is required for MVP. A free provider, valid private-display terms and actual coverage validation are gates; an unofficial scraper is prohibited.

## 6. Review integration contract and trust boundary

The review flow is advisory and snapshot-bound:

1. **Discord request:** The user requests a portfolio or holding review through Discord.
2. **Hermes reads:** Hermes uses only its scoped `portfolio:read` credential to read the immutable application snapshot API. It may not edit holdings, transactions or quotes and has no trading capability.
3. **Local model:** Hermes sends the minimum required snapshot/evidence payload to the local AI Server model by default. The portfolio container does not embed another agent runtime. Remote-model processing is not enabled by this decision.
4. **Hermes submits:** Hermes submits an advisory result through the application review API using only `reviews:write`, an idempotency key, the expected snapshot ID and the relevant instrument IDs.
5. **Application stores/displays:** The application validates the payload and stores the review tied to the exact snapshot used. A duplicate idempotency key must not create a second review.
6. **Staleness:** If holdings or prices change, a later snapshot is created. Existing reviews remain associated with their old snapshot and are visibly marked stale/outdated; they are never silently attached to the new portfolio state.

The trust boundary is explicit: Discord text, fetched articles, uploaded files and model output are untrusted content. They cannot grant permissions or instruct Hermes to edit holdings or trade. Authentication, authorization and schema validation are enforced by the application API, not by a UI visibility rule.

Every review separates:

- **Deterministic metrics:** application-calculated quantities, costs, prices, FX conversions, concentration, coverage and freshness, with calculation version and snapshot ID.
- **Sourced facts:** quote metadata, issuer or instrument facts and cited research, each with source URL and evidence/as-of date. Missing evidence is recorded as missing.
- **Opinion/advice:** model-generated interpretation, risks, counterarguments and conditional status, clearly labeled as advisory and not as a deterministic metric or sourced fact.

Hermes permissions are intentionally narrow:

- `portfolio:read`: read immutable snapshots and holding detail needed for a review;
- `reviews:write`: submit validated advisory reviews only;
- no holdings/transaction write permission;
- no quote write permission unless a separately authorized human/provider ingestion path is implemented; and
- no trade, brokerage or order-execution permission.

The review API contract planned by the canonical plan is:

- `GET /api/v1/portfolio/snapshots/latest`
- `GET /api/v1/portfolio/snapshots/{snapshot_id}`
- `GET /api/v1/holdings/{instrument_id}`
- `GET /api/v1/reviews`
- `POST /api/v1/reviews`

These endpoints are a future application contract, not an assertion that they exist today.

## 7. Hard gates before scaffolding

Scaffolding Go, React, SQLite, Docker or provider ingestion remains blocked until all of the following are complete:

1. **Import preview reconciliation:** Produce a preview directly from the authoritative holdings note, preserving every source symbol, quantity, original currency and supplied cost. Reconcile all 33 source rows/lot entries exactly once into 31 distinct instruments, with NVDA and INTC aggregate rows excluded from the import. Resolve or explicitly flag any ambiguous identity, ADR ratio, ETF listing, REIT classification and missing transaction metadata.
2. **Explicit import approval:** The user reviews and explicitly approves the reconciled preview. Approval must not modify the Obsidian source note.
3. **Provider-key coverage validation:** Obtain a free provider key only through an operator-controlled account workflow, then query exchange-symbol/search endpoints for every mapping in Section 4 and the required FX pairs. Record the exact returned symbol, exchange, asset type, currency, active status and plan entitlement. Do not place the secret in the repository or decision record.
4. **Free-plan quota test:** Confirm that the chosen refresh design fits the actual free daily/monthly quota after identity discovery, FX calls, retries and manual refresh. Cache metadata and avoid repeated failed lookups.
5. **Display/storage terms check:** Confirm that the selected plan permits the intended private authenticated display and local cache. If not, select a permitted alternative or use sourced manual overrides for the uncovered data.
6. **Timestamp and price-basis test:** Demonstrate a real response with a usable market/as-of timestamp, retrieval timestamp, quote currency and documented raw/adjusted basis. Confirm exchange holiday/weekend behavior and reject future or currency-mismatched data.
7. **Corporate-action review:** Reconcile splits, dividends, ticker changes and ADR/ETF/REIT actions for any affected instrument before using provider-adjusted values with the supplied lots.
8. **FX validation:** Validate USD/MYR, SGD/MYR and JPY/MYR direction, timestamp, precision and free-plan access. Do not present MYR totals when the required FX coverage is incomplete.
9. **Review privacy approval:** Confirm that the local AI Server model receives only the minimum snapshot/evidence fields required for an advisory review and that the scoped Hermes permissions remain read-snapshot/write-review only.
10. **Implementation authorization remains bounded:** Only after the gates above are accepted may the next task scaffold the application. No trading integration, public ingress, brokerage credential, alternate agent runtime or paid market-data dependency is implied by this record.

## Official source index

- Canonical plan: `/opt/data/obsidian-vault/projects/01-Active/Portfolio-Dashboard/Implementation Plan.md` (repository planning authority; not modified).
- Holdings source: `/opt/data/obsidian-vault/main/20-Personal/Investing/Portfolio.md` (pre-cutover authority; not modified).
- EODHD exchange/ticker lists: https://eodhd.com/financial-apis/exchanges-api-list-of-tickers-and-trading-hours
- EODHD EOD API: https://eodhd.com/financial-apis/api-for-historical-data-and-volumes
- EODHD API limits: https://eodhd.com/financial-apis/api-limits
- EODHD crypto list: https://eodhd.com/financial-apis/list-supported-crypto-currencies
- EODHD search API: https://eodhd.com/financial-apis/search-api-for-stocks-etfs-mutual-funds
- EODHD data sources/partners: https://eodhd.com/financial-apis/our-data-sources-and-data-partners
- EODHD splits/dividends: https://eodhd.com/financial-apis/api-splits-dividends
- EODHD pricing/register: https://eodhd.com/pricing and https://eodhd.com/register
- Marketstack docs/pricing: https://docs.apilayer.com/marketstack/docs/api-endpoints-v1 and https://marketstack.com/pricing
- Twelve Data docs/pricing/terms: https://twelvedata.com/docs, https://twelvedata.com/pricing, https://twelvedata.com/terms
- Alpha Vantage docs/support/terms: https://www.alphavantage.co/documentation/, https://www.alphavantage.co/support/, https://www.alphavantage.co/terms_of_service/
- Financial Modeling Prep pricing: https://site.financialmodelingprep.com/developer/docs/pricing
- CoinGecko authentication/usage/identity: https://docs.coingecko.com/reference/authentication, https://docs.coingecko.com/reference/api-usage, https://docs.coingecko.com/reference/coins-list, https://docs.coingecko.com/reference/simple-price

This record is documentation only. It does not create accounts, access credentials, modify the Obsidian vault, deploy infrastructure or begin application implementation.

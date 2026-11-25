(() => {
  const time = '2026-11-25T12:00:00.000000000Z'
  const accessed = '2026-11-24T11:00:00.000000000Z'
  const longDescription = 'A deliberately long same-symbol instrument description for synthetic exact-lot identification and responsive wrapping'
  const overview = {
    portfolio: { id: 'portfolio-default', name: 'Synthetic S8 Portfolio — long title for review finding and evidence navigation', default_reporting_currency: 'MYR' },
    reporting_currency: 'MYR',
    snapshot: { snapshot_id: 'snapshot-current', state: 'complete', complete: true, as_of: time, calculation_version: 'phase-b.decimal.v1', reporting_total: '18', subtotals: [{ currency: 'MYR', market_value: '18', cost_basis: '15', unrealised_pnl: '3' }] },
    holdings: [['lot-a', '1', '5', '6'], ['lot-b', '2', '10', '12']].map(([lot_id, quantity, cost_basis, value]) => ({ lot_id, symbol: 'SAME', name: longDescription, description: longDescription, quantity, currency: 'MYR', unit_cost: '5', cost_basis, latest_price: '6', latest_value: value, reporting_value: value, quote_source: 'synthetic', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false })),
  }
  const makeReview = (review_id, lot = 'lot-a', submitted_at = time) => ({
    review_id, portfolio_id: 'portfolio-default', snapshot_id: 'snapshot-old', calculation_version: 'phase-b.decimal.v1',
    scope: { type: 'holding', holding_ids: [lot], instrument_ids: [lot === 'lot-b' ? 'instrument-b' : 'instrument-a'] },
    status: 'incomplete', recommendation: 'no_recommendation', rationale: `Synthetic rationale ${review_id}`,
    risks: ['Review is advisory, not a trading instruction.'], counterarguments: ['Synthetic uncertainty.'], conditions: ['Check evidence.'],
    metrics: [{ name: 'Historical lot market value', value: lot === 'lot-b' ? '10' : '5', unit: 'MYR', as_of: accessed, source: 'snapshot', deterministic: true }],
    sourced_facts: [{ statement: `Synthetic sourced fact for ${review_id}`, evidence_ids: ['evidence-shared'] }], model_opinion: 'Synthetic model opinion; no trade is performed.',
    evidence: [{ evidence_id: 'evidence-shared', url: 'https://example.test/synthetic-source', title: `Long synthetic source title — ${longDescription}`, published_at: null, accessed_at: accessed, availability: 'available', freshness: 'current', excerpt: `Synthetic local evidence excerpt for ${review_id}` }],
    model: { provider: 'fixture', model: 'synthetic-model', version: '1', runtime: 'hermes-environment' }, generated_at: accessed, submitted_at,
    submitted_by: { principal_id: 'synthetic-machine', principal_type: 'machine', permissions: ['reviews:write'] },
    lifecycle: { historical_status: 'incomplete', current_snapshot_id: 'snapshot-current', outdated: true },
  })
  const firstPage = Array.from({ length: 20 }, (_, index) => makeReview(`review-page1-${String(index).padStart(2, '0')}`, 'lot-a', `2026-11-25T12:${String(20 - index).padStart(2, '0')}:00.000000000Z`))
  const target = makeReview('review-target-lot-b', 'lot-b', '2026-11-24T12:00:00.000000000Z')
  const requests = []
  let appendFailures = 1
  const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  const realFetch = window.fetch.bind(window)
  window.__s8 = { requests, resetFailures: () => { appendFailures = 1 } }
  window.fetch = (input, init = {}) => {
    const raw = String(input)
    const url = new URL(raw, location.origin)
    requests.push({ url: raw, method: init.method || 'GET', signal: init.signal })
    if (url.pathname.endsWith('/auth/session')) return Promise.resolve(response({ authenticated: true, csrf_token: 'synthetic-csrf', expires_at: new Date(Date.now() + 3600000).toISOString() }))
    if (url.pathname.endsWith('/auth/logout')) return Promise.resolve(response({}))
    if (url.pathname.endsWith('/overview')) return Promise.resolve(response(overview))
    if (url.pathname.endsWith('/activity')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.pathname.endsWith('/feedback')) return Promise.resolve(response({}, 409))
    if (url.pathname.endsWith('/reviews')) {
      const rows = [...firstPage, target]
      if (url.searchParams.has('cursor')) {
        if (appendFailures > 0) { appendFailures -= 1; return Promise.resolve(response({ error: { code: 'synthetic_failure' } }, 500)) }
        return Promise.resolve(response({ items: [target], has_more: false }))
      }
      return Promise.resolve(response({ items: rows.slice(0, 20), has_more: true, next_cursor: 'cursor-s8-1234567890' }))
    }
    if (url.pathname.includes('/reviews/')) return url.pathname.endsWith('/review-target-lot-b')
      ? Promise.resolve(response(target))
      : Promise.resolve(response({ error: { code: 'not_found' } }, 404))
    return realFetch(input, init)
  }
})()

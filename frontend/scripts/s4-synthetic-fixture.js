(() => {
  const session = { authenticated: true, csrf_token: 's4-fixture-token', expires_at: '2030-01-01T00:00:00.000Z' }
  const overview = {
    portfolio: { id: 'portfolio-default', name: 'S4 Synthetic Portfolio', default_reporting_currency: 'MYR' },
    reporting_currency: 'MYR',
    snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] },
    holdings: [{ lot_id: 'lot-s4', symbol: 'S4', name: 'S4 Synthetic Fund', description: 'Keyboard recovery fixture', quantity: '2.500', currency: 'USD', unit_cost: '100.25', cost_basis: '250.625', latest_price: '110', latest_value: '275', reporting_value: '370', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false }],
  }
  const state = {
    deferHistory: false,
    deferSave: false,
    uncertainNextSave: false,
    failLogoutOnce: false,
    historyCalls: 0,
    putBodies: [],
    committedBody: null,
    committedHistory: [],
    pendingHistory: [],
    pendingSaves: [],
    logoutCalls: 0,
  }
  const baseThesis = (overrides = {}) => ({
    id: 'thesis-s4', portfolio_id: 'portfolio-default', holding_id: 'lot-s4', instrument_id: 'instrument-s4',
    summary: 'Canonical S4 thesis', intended_holding_period: 'Long term', allocation_limit: null,
    reasons_to_hold: [], risks: [], reasons_to_sell: [], conditions_to_change: [], sources: [],
    revision: 1, revision_id: 'revision-s4-1', updated_at: '2026-01-02T12:00:00.000000000Z', ...overrides,
  })
  const response = (body, status = 200, extraHeaders = {}) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...extraHeaders } })
  const canonicalFromBody = (body, revision) => baseThesis({
    summary: body.summary,
    intended_holding_period: body.intended_holding_period,
    allocation_limit: body.allocation_limit,
    reasons_to_hold: body.reasons_to_hold,
    risks: body.risks,
    reasons_to_sell: body.reasons_to_sell,
    conditions_to_change: body.conditions_to_change,
    sources: body.sources,
    revision,
    revision_id: `revision-s4-${revision}`,
    updated_at: `2026-01-0${revision + 2}T12:00:00.000000000Z`,
  })
  function commit(body) {
    if (state.committedBody === body) return canonicalFromBody(JSON.parse(body), state.committedHistory.at(-1)?.revision || 2)
    const parsed = JSON.parse(body)
    const revision = state.committedHistory.length + 2
    const canonical = canonicalFromBody(parsed, revision)
    state.committedBody = body
    state.committedHistory.push({ ...canonical, created_at: canonical.updated_at })
    return canonical
  }
  window.__s4 = {
    state,
    configure(options = {}) {
      Object.assign(state, options)
      state.pendingHistory.length = 0
      state.pendingSaves.length = 0
    },
    reset() {
      state.deferHistory = false; state.deferSave = false; state.uncertainNextSave = false; state.failLogoutOnce = false
      state.historyCalls = 0; state.putBodies.length = 0; state.committedBody = null; state.committedHistory.length = 0
      state.pendingHistory.length = 0; state.pendingSaves.length = 0; state.logoutCalls = 0
    },
    resolveHistory(body = { items: [] }) {
      const pending = state.pendingHistory.splice(0)
      pending.forEach(({ resolve }) => resolve(response(body)))
    },
    resolveSave(body = baseThesis({ revision: 2, revision_id: 'revision-s4-2', summary: 'Resolved pending S4 thesis' })) {
      const pending = state.pendingSaves.splice(0)
      pending.forEach(({ resolve }) => resolve(response(body)))
    },
  }
  const originalFetch = window.fetch.bind(window)
  window.fetch = (input, init = {}) => {
    const url = String(input)
    if (url.includes('/api/v1/auth/session')) return Promise.resolve(response(session))
    if (url.includes('/api/v1/auth/logout')) {
      state.logoutCalls += 1
      if (state.failLogoutOnce && state.logoutCalls === 1) return Promise.resolve(response({}, 503))
      return Promise.resolve(response({ authenticated: false }))
    }
    if (url.includes('/overview')) return Promise.resolve(response(overview, 200, { ETag: '"portfolio-default-7"' }))
    if (url.endsWith('/history')) {
      state.historyCalls += 1
      if (state.deferHistory) return new Promise((resolve) => state.pendingHistory.push({ resolve, signal: init.signal }))
      return Promise.resolve(response({ items: [...state.committedHistory] }))
    }
    if (url.endsWith('/holdings/lot-s4/thesis') && init.method === 'PUT') {
      const body = String(init.body)
      state.putBodies.push(body)
      if (state.deferSave) return new Promise((resolve) => state.pendingSaves.push({ resolve, signal: init.signal }))
      const canonical = commit(body)
      if (state.uncertainNextSave) {
        state.uncertainNextSave = false
        return Promise.reject(new TypeError('synthetic response lost after commit'))
      }
      return Promise.resolve(response(canonical))
    }
    if (url.endsWith('/holdings/lot-s4/thesis')) return Promise.resolve(response(baseThesis()))
    if (url.includes('/activity')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.includes('/reviews')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.includes('/market-data/schedule')) return Promise.resolve(response({ portfolio_id: 'portfolio-default', enabled: false, cron: '', timezone: 'Asia/Singapore', reporting_currency: 'MYR', revision: 1, updated_at: null, source: 'saved', etag: '"fixture"', effective: false, degraded: false, runtime_status: 'disabled' }))
    return originalFetch(input, init)
  }
})()
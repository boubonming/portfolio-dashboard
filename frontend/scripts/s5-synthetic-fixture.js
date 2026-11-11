(() => {
  const session = { authenticated: true, csrf_token: 's5-fixture-token', expires_at: '2030-01-01T00:00:00.000Z' }
  const etag = (letter) => `"${letter.repeat(64)}"`
  const saved = (overrides = {}) => ({
    portfolio_id: 'portfolio-default', enabled: false, cron: '', timezone: 'Asia/Singapore',
    reporting_currency: '', revision: 1, updated_at: '2026-01-01T00:00:00.000000000Z', source: 'saved',
    etag: etag('a'), effective: false, degraded: false, runtime_status: 'disabled', ...overrides,
  })
  const state = {
    schedule: location.search.includes('s5degraded') ? saved({ enabled: true, cron: '0 5 * * *', effective: false, degraded: true, runtime_status: 'degraded' }) : saved(),
    getError: location.search.includes('s5error'),
    nextSchedule: null,
    conflictOnce: false,
    uncertainOnce: false,
    putCalls: [],
    getCalls: 0,
  }
  const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
  window.__s5 = {
    state,
    reset() {
      state.schedule = saved(); state.getError = false; state.nextSchedule = null; state.conflictOnce = false; state.uncertainOnce = false
      state.putCalls.length = 0; state.getCalls = 0
    },
    configure(options = {}) { Object.assign(state, options) },
  }
  const originalFetch = window.fetch.bind(window)
  window.fetch = (input, init = {}) => {
    const url = String(input)
    if (url.includes('/api/v1/auth/session')) return Promise.resolve(response(session))
    if (url.includes('/api/v1/auth/logout')) return Promise.resolve(response({ authenticated: false }))
    if (url.includes('/overview')) return Promise.resolve(response({
      portfolio: { id: 'portfolio-default', name: 'S5 Synthetic Portfolio', default_reporting_currency: 'MYR' },
      reporting_currency: 'MYR', snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] }, holdings: [],
    }))
    if (url.includes('/market-data/schedule')) {
      if (init.method === 'PUT') {
        const body = String(init.body)
        state.putCalls.push(body)
        if (state.conflictOnce) {
          state.conflictOnce = false
          return Promise.resolve(response({ error: 'stale_schedule' }, 412))
        }
        if (state.uncertainOnce) {
          state.uncertainOnce = false
          return Promise.reject(new TypeError('synthetic response lost'))
        }
        const request = JSON.parse(body)
        state.schedule = saved({
          ...state.schedule, enabled: request.enabled, cron: request.cron, timezone: request.timezone,
          reporting_currency: request.reporting_currency, revision: state.schedule.revision + 1,
          updated_at: '2026-01-02T12:00:00.000000000Z', source: 'saved', etag: etag('b'),
          effective: request.enabled, runtime_status: request.enabled ? 'active' : 'disabled', degraded: false,
        })
        return Promise.resolve(response(state.schedule))
      }
      state.getCalls += 1
      if (state.getError) return Promise.resolve(response({ error: 'fixture_unavailable' }, 503))
      if (state.nextSchedule) {
        state.schedule = state.nextSchedule
        state.nextSchedule = null
      }
      return Promise.resolve(response(state.schedule))
    }
    if (url.includes('/market-data')) return Promise.resolve(response({ portfolio_id: 'portfolio-default', etag: etag('s'), providers: [], instruments: [] }))
    if (url.includes('/activity')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.includes('/reviews')) return Promise.resolve(response({ items: [], has_more: false }))
    return originalFetch(input, init)
  }
})()

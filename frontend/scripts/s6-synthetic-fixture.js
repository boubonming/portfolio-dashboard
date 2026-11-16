(() => {
  const session = { authenticated: true, csrf_token: 's6-fixture-token', expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString() }
  const providers = [{ name: 'coingecko', configured: true, public: true }, { name: 'finnhub', configured: true, public: false }]
  const instruments = [
    { instrument_id: 'instrument-a', symbol: 'SAME', description: 'First synthetic instrument', currency: 'USD', mappings: [] },
    { instrument_id: 'instrument-b', symbol: 'SAME', description: 'Second synthetic instrument', currency: 'USD', mappings: [] },
  ]
  const mapping = (instrumentID, symbol) => ({ id: `mapping-${instrumentID}`, instrument_id: instrumentID, provider: 'coingecko', provider_symbol: symbol, quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z', provenance: 'S6 synthetic browser confirmation', created_at: '2026-01-01T00:00:00.000000000Z' })
  const setup = (etag = '"s6-setup-1"', saved = false) => ({ portfolio_id: 'portfolio-default', etag, providers, instruments: saved ? instruments.map((instrument, index) => { const savedMapping = mapping(instrument.instrument_id, `owner-${index}`); return { ...instrument, mappings: [{ provider: savedMapping.provider, provider_symbol: savedMapping.provider_symbol, quote_currency: savedMapping.quote_currency, active_from: savedMapping.active_from, provenance: savedMapping.provenance }] } }) : instruments })
  const overview = {
    portfolio: { id: 'portfolio-default', name: 'S6 Synthetic Portfolio', default_reporting_currency: 'MYR' }, reporting_currency: 'MYR',
    snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 's6.test.v1', reporting_total: '100.5', subtotals: [] },
    holdings: [{ lot_id: 'lot-s6-a', symbol: 'SAME', name: 'First synthetic instrument', description: 'First synthetic instrument', quantity: '1', currency: 'USD', unit_cost: '100', cost_basis: '100', latest_price: '110', latest_value: '110', reporting_value: '110', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false, broker: 'S6 Fixture', acquisition_date: '2026-01-01' }],
  }
  const state = { putCalls: 0, setupReads: 0, failedPostSaveRead: false, pendingRead: null, holdNextReconcile: false, saved: false }
  const response = (body, status = 200, headers = {}) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } })
  const discovery = { portfolio_id: 'portfolio-default', etag: '"s6-setup-1"', results: [
    { instrument_id: 'instrument-a', status: 'candidates', provider: 'coingecko', attempted_providers: ['coingecko'], candidates: [{ provider: 'coingecko', identifier: 'same-coin', symbol: 'SAME', name: 'Same synthetic coin', description: 'Distinguishing provider candidate', type: 'crypto', region: 'global', exchange: 'fixture', currency: 'USD' }], diagnostic: 'S6 populated candidate' },
    { instrument_id: 'instrument-b', status: 'unavailable', attempted_providers: ['finnhub'], candidates: [], diagnostic: 'S6 provider unavailable; verify manually' },
  ] }
  window.__s6 = {
    state,
    resolveRead: () => { state.holdNextReconcile = false; state.pendingRead?.(response(setup(state.saved ? '"s6-setup-3"' : '"s6-setup-1"', state.saved))); state.pendingRead = null },
  }
  const originalFetch = window.fetch.bind(window)
  window.fetch = (input, init) => {
    const url = String(input)
    if (url.includes('/api/v1/auth/session')) return Promise.resolve(response(session))
    if (url.includes('/market-data/discover')) return Promise.resolve(response(discovery))
    if (url.includes('/market-data/mappings')) {
      state.putCalls += 1
      if (state.putCalls === 1) { state.holdNextReconcile = true; return Promise.resolve(response({ error: 'stale setup' }, 412)) }
      if (state.putCalls === 2) return Promise.reject(new TypeError('synthetic uncertain network result'))
      state.saved = true
      return Promise.resolve(response({ status: 'saved', etag: '"s6-setup-3"', outcomes: instruments.map((instrument, index) => ({ instrument_id: instrument.instrument_id, status: 'saved', mapping: mapping(instrument.instrument_id, `owner-${index}`) })) }))
    }
    if (url.includes('/market-data')) {
      state.setupReads += 1
      if (state.holdNextReconcile) return new Promise((resolve) => { state.pendingRead = resolve })
      if (state.saved && !state.failedPostSaveRead) { state.failedPostSaveRead = true; return Promise.resolve(response({ error: 'synthetic read outage after confirmed save' }, 503)) }
      return Promise.resolve(response(setup(state.saved ? '"s6-setup-3"' : '"s6-setup-1"', state.saved)))
    }
    if (url.includes('/overview')) return Promise.resolve(response(overview, 200, { ETag: '"s6-overview-1"' }))
    if (url.includes('/activity') || url.includes('/reviews')) return Promise.resolve(response({ items: [], has_more: false }))
    return originalFetch(input, init)
  }
})()
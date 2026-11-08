(() => {
  const session = { authenticated: true, csrf_token: 's3-fixture-token', expires_at: '2030-01-01T00:00:00.000Z' }
  const overview = {
    portfolio: { id: 'portfolio-default', name: 'Synthetic Portfolio With A Long Label For Reflow', default_reporting_currency: 'MYR' },
    reporting_currency: 'MYR',
    snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] },
    holdings: [
      { lot_id: 'lot-same-symbol-1', symbol: 'SAME', name: 'First Synthetic Instrument', description: 'First synthetic instrument description that wraps', quantity: '2.500', currency: 'USD', unit_cost: '100.25', cost_basis: '250.625', latest_price: '110', latest_value: '275', reporting_value: '370', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false, broker: 'Synthetic Broker', acquisition_date: '2025-01-02' },
      { lot_id: 'lot-same-symbol-2', symbol: 'SAME', name: 'Second Synthetic Instrument', description: 'Second synthetic instrument description that wraps', quantity: '3.000', currency: 'USD', unit_cost: '90', cost_basis: '270', latest_price: '95', latest_value: '285', reporting_value: '380', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false, broker: 'Synthetic Broker', acquisition_date: '2025-02-02' },
    ],
  }
  const market = {
    portfolio_id: 'portfolio-default', etag: '"fixture-setup-1"',
    providers: [{ name: 'coingecko', configured: true, public: true }, { name: 'finnhub', configured: true, public: false }],
    instruments: [
      { instrument_id: 'instrument-same-1', symbol: 'SAME', description: 'First synthetic instrument description that wraps', currency: 'USD', mappings: [] },
      { instrument_id: 'instrument-same-2', symbol: 'SAME', description: 'Second synthetic instrument description that wraps', currency: 'USD', mappings: [] },
    ],
  }
  const schedule = { portfolio_id: 'portfolio-default', enabled: false, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: 'MYR', revision: 1, updated_at: '2026-01-02T12:00:00.000000000Z', source: 'saved', etag: `"${'a'.repeat(64)}"`, effective: false, degraded: false, runtime_status: 'disabled' }
  const originalFetch = window.fetch.bind(window)
  const response = (body, status = 200, extraHeaders = {}) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...extraHeaders } })
  window.fetch = (input, init) => {
    const url = String(input)
    if (url.includes('/api/v1/auth/session')) return Promise.resolve(response(session))
    if (url.includes('/market-data/schedule')) return Promise.resolve(response(schedule))
    if (url.includes('/market-data/discover')) return Promise.resolve(response({ portfolio_id: market.portfolio_id, etag: market.etag, results: market.instruments.map((instrument, index) => index === 0 ? ({ instrument_id: instrument.instrument_id, status: 'candidates', provider: 'coingecko', attempted_providers: ['coingecko'], candidates: [{ provider: 'coingecko', identifier: 'same-coin', symbol: 'SAME', name: 'Same synthetic coin', type: 'crypto', region: 'global', exchange: 'fixture', currency: 'USD' }], diagnostic: 'synthetic populated candidate' }) : ({ instrument_id: instrument.instrument_id, status: 'unavailable', attempted_providers: ['finnhub'], candidates: [], diagnostic: 'synthetic unavailable fixture' })) }))
    if (url.includes('/market-data')) return Promise.resolve(response(market))
    if (url.includes('/overview')) return Promise.resolve(response(overview, 200, { ETag: '"portfolio-default-7"' }))
    if (url.includes('/activity')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.includes('/reviews')) return Promise.resolve(response({ items: [], has_more: false }))
    return originalFetch(input, init)
  }
})()

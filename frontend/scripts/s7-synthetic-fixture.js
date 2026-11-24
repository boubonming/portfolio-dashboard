(() => {
  const session = { authenticated: true, csrf_token: 's7-fixture-token', expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString() }
  const overview = {
    portfolio: { id: 'portfolio-default', name: 'S7 Synthetic Portfolio With An Extraordinarily Long Label For Wrapping', default_reporting_currency: 'MYR' },
    reporting_currency: 'MYR',
    snapshot: { state: 'complete', complete: true, as_of: '2026-01-02T12:00:00.000000000Z', calculation_version: 'test.v1', reporting_total: '100.5', subtotals: [] },
    holdings: [{ lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', description: 'Synthetic instrument', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10', latest_price: '50.5', latest_value: '101', reporting_value: '100.5', quote_source: 'fixture', freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false }],
  }
  const market = {
    portfolio_id: 'portfolio-default', etag: '"s7-setup-1"',
    providers: [{ name: 'coingecko', configured: true, public: true }, { name: 'open_exchange_rates', configured: true, public: false }],
    instruments: [{ instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', mappings: [{ provider: 'coingecko', provider_symbol: 'aaa', quote_currency: 'USD', active_from: '2026-01-01T00:00:00.000000000Z' }] }],
  }
  const refresh = {
    refresh: { refresh_id: 'refresh-s7', portfolio_id: 'portfolio-default', status: 'complete', started_at: '2026-01-01T00:00:00.000000000Z', ended_at: '2026-01-01T00:00:01.000000000Z', observation_ids: ['quote-observation', 'fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], providers: [{ provider: 'coingecko', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['quote-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [], errors: [] }, { provider: 'open_exchange_rates', status: 'complete', request_count: 1, succeeded_count: 1, failed_count: 0, missing_mapping_count: 0, observation_ids: ['fx-observation'], quote_observations: [], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], errors: [] }] },
    snapshot: { snapshot_id: 'snapshot-s7', portfolio_id: 'portfolio-default', calculation_version: 'phase-b.decimal.v1', created_at: '2026-01-01T00:00:02.000000000Z', as_of: '2026-01-01T00:00:01.000000000Z', complete: true, reporting_currency: 'MYR', reporting_total: '12', currencies: ['USD'], subtotals: [{ currency: 'USD', market_value: '10', cost_basis: '8', unrealised_pnl: '2' }], missing_count: 0, stale_count: 0, future_count: 0, invalid_count: 0, missing_dependencies: [], stale_dependencies: [], future_dependencies: [], invalid_dependencies: [], quote_ids: ['quote-observation'], fx_ids: ['fx-observation'], quote_observations: [{ id: 'quote-observation', instrument_id: 'instrument-a', symbol: 'AAA', currency: 'USD', source: 'coingecko' }], fx_observations: [{ id: 'fx-observation', base_currency: 'USD', quote_currency: 'MYR', source: 'open_exchange_rates' }], source_timestamps: ['2026-01-01T00:00:00.000000000Z'], presentation_timezone: 'Asia/Singapore' },
    complete: true, freshness_policy: 'quotes and FX must be no older than the configured freshness window; incomplete data never becomes zero', missing_instruments: [],
  }
  const response = (body, status = 200, headers = {}) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } })
  const originalFetch = window.fetch.bind(window)
  window.fetch = (input, init) => {
    const url = String(input)
    if (url.includes('/api/v1/auth/session')) return Promise.resolve(response(session))
    if (url.endsWith('/market-data/refresh')) return Promise.resolve(response(refresh))
    if (url.includes('/market-data/schedule')) return Promise.resolve(response({ portfolio_id: 'portfolio-default', enabled: false, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: 'MYR', revision: 1, updated_at: '2026-01-02T12:00:00.000000000Z', source: 'saved', etag: '"s7-schedule"', effective: false, degraded: false, runtime_status: 'disabled' }))
    if (url.includes('/market-data')) return Promise.resolve(response(market))
    if (url.includes('/overview')) return Promise.resolve(response(overview, 200, { ETag: '"s7-overview"' }))
    if (url.includes('/activity')) return Promise.resolve(response({ items: [], has_more: false }))
    if (url.includes('/reviews')) return Promise.resolve(response({ items: [], has_more: false }))
    return originalFetch(input, init)
  }
})()
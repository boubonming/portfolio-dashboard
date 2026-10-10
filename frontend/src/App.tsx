import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

export type Freshness = {
  status: 'fresh' | 'stale' | 'missing' | 'future' | 'invalid' | string
  market_at?: string
  age?: string
}

export type Holding = {
  lot_id: string
  symbol: string
  name?: string
  description?: string
  quantity: string
  currency: string
  unit_cost?: string
  cost_basis?: string
  latest_price?: string
  latest_value?: string
  reporting_value?: string
  quote_source?: string
  freshness: Freshness
  data_quality: string[]
  broker?: string
  acquisition_date?: string
  missing_broker: boolean
  missing_acquisition_date: boolean
  source_row?: number
}

export type PortfolioOverview = {
  portfolio: {
    id: string
    name: string
    default_reporting_currency: string
  }
  reporting_currency: string
  snapshot: {
    state: 'complete' | 'incomplete' | 'no_snapshot' | string
    snapshot_id?: string
    calculation_version?: string
    created_at?: string
    as_of?: string
    complete: boolean
    reporting_total?: string
    missing_dependencies?: string[]
    stale_dependencies?: string[]
    invalid_dependencies?: string[]
    subtotals: Array<{ currency: string; market_value: string; cost_basis?: string; unrealised_pnl?: string }>
  }
  holdings: Holding[]
}

type SortKey = 'symbol' | 'quantity' | 'currency' | 'cost_basis' | 'latest_price' | 'reporting_value' | 'freshness'
type SortDirection = 'ascending' | 'descending'

// Compare canonical decimal strings without Number, parseFloat, or lexical
// ordering. It also safely handles the optional values used by incomplete data.
export function compareDecimalStrings(left: string | undefined, right: string | undefined): number {
  if (left === undefined && right === undefined) return 0
  if (left === undefined) return -1
  if (right === undefined) return 1
  const normalize = (value: string) => {
    const trimmed = value.trim()
    const negative = trimmed.startsWith('-')
    const unsigned = trimmed.replace(/^[+-]/, '')
    const [integerPart = '0', fractionPart = ''] = unsigned.split('.')
    const integer = integerPart.replace(/^0+(?=\d)/, '') || '0'
    const fraction = fractionPart.replace(/0+$/, '')
    const zero = integer === '0' && fraction === ''
    return { negative: negative && !zero, integer, fraction }
  }
  const a = normalize(left)
  const b = normalize(right)
  if (a.negative !== b.negative) return a.negative ? -1 : 1
  const sign = a.negative ? -1 : 1
  if (a.integer.length !== b.integer.length) return sign * (a.integer.length < b.integer.length ? -1 : 1)
  if (a.integer !== b.integer) return sign * (a.integer < b.integer ? -1 : 1)
  const width = Math.max(a.fraction.length, b.fraction.length)
  const af = a.fraction.padEnd(width, '0')
  const bf = b.fraction.padEnd(width, '0')
  if (af === bf) return 0
  return sign * (af < bf ? -1 : 1)
}

function displayDate(value?: string) {
  if (!value) return '—'
  return value.replace('T', ' ').replace(/\.\d+Z$/, 'Z')
}

function qualityLabel(holding: Holding) {
  const flags = [...holding.data_quality]
  if (holding.freshness.status && holding.freshness.status !== 'fresh' && !flags.includes(holding.freshness.status)) flags.unshift(holding.freshness.status)
  return flags.length ? flags.join(', ') : 'ok'
}

function qualityClass(holding: Holding) {
  if (holding.data_quality.some((flag) => flag.startsWith('invalid') || flag === 'future')) return 'quality-error'
  if (holding.data_quality.length || holding.freshness.status !== 'fresh') return 'quality-warning'
  return 'quality-ok'
}

export default function App() {
  const [currency, setCurrency] = useState('MYR')
  const [data, setData] = useState<PortfolioOverview | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('symbol')
  const [sortDirection, setSortDirection] = useState<SortDirection>('ascending')
  const controllerRef = useRef<AbortController | null>(null)
  const requestGeneration = useRef(0)

  const load = useCallback(() => {
    const generation = ++requestGeneration.current
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setState('loading')
    setError('')
    const url = `/api/v1/portfolios/portfolio-default/overview?currency=${encodeURIComponent(currency)}`
    fetch(url, { headers: { Accept: 'application/json' }, signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Request failed (${response.status})`)
        return response.json() as Promise<PortfolioOverview>
      })
      .then((overview) => {
        if (controller.signal.aborted || generation !== requestGeneration.current) return
        setData(overview)
        setState('ready')
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== requestGeneration.current) return
        setError(reason instanceof Error ? reason.message : 'Unable to load the portfolio')
        setState('error')
      })
  }, [currency])

  useEffect(() => {
    load()
    return () => {
      requestGeneration.current += 1
      controllerRef.current?.abort()
      controllerRef.current = null
    }
  }, [load])

  const visibleHoldings = useMemo(() => {
    if (!data) return []
    const needle = filter.trim().toLowerCase()
    const filtered = data.holdings.filter((holding) => {
      if (!needle) return true
      return [holding.symbol, holding.name, holding.description, holding.currency, holding.broker, qualityLabel(holding)].some((value) => value?.toLowerCase().includes(needle))
    })
    return [...filtered].sort((left, right) => {
      let comparison = 0
      if (sortKey === 'symbol' || sortKey === 'currency') comparison = (left[sortKey] || '').localeCompare(right[sortKey] || '')
      else if (sortKey === 'freshness') comparison = (left.freshness.status || '').localeCompare(right.freshness.status || '')
      else comparison = compareDecimalStrings(left[sortKey], right[sortKey])
      return sortDirection === 'ascending' ? comparison : -comparison
    })
  }, [data, filter, sortDirection, sortKey])

  const selectSort = (next: SortKey) => {
    if (sortKey === next) setSortDirection(sortDirection === 'ascending' ? 'descending' : 'ascending')
    else {
      setSortKey(next)
      setSortDirection('ascending')
    }
  }

  const sortButton = (label: string, key: SortKey) => (
    <button className="sort-button" type="button" onClick={() => selectSort(key)} aria-label={`Sort by ${label}`}>
      {label} {sortKey === key ? (sortDirection === 'ascending' ? '↑' : '↓') : '↕'}
    </button>
  )

  const sortHeader = (label: string, key: SortKey) => (
    <th scope="col" aria-sort={sortKey === key ? sortDirection : 'none'}>{sortButton(label, key)}</th>
  )

  return (
    <main className="shell">
      <header className="brand">
        <span className="eyebrow">Private workspace</span>
        <h1>Portfolio Dashboard</h1>
        <p className="muted">Read-only holdings and immutable valuation snapshots.</p>
      </header>

      <section className="toolbar" aria-label="Portfolio controls">
        <label htmlFor="currency">Reporting currency</label>
        <select id="currency" value={currency} onChange={(event) => setCurrency(event.target.value)}>
          <option value="MYR">MYR</option>
          <option value="USD">USD</option>
          <option value="SGD">SGD</option>
          <option value="JPY">JPY</option>
        </select>
        <label htmlFor="filter">Filter holdings</label>
        <input id="filter" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Symbol, currency or flag" />
      </section>

      {state === 'loading' && <section className="notice" role="status"><h2>Loading portfolio</h2><p>Reading the latest immutable snapshot…</p></section>}
      {state === 'error' && <section className="notice error" role="alert"><h2>Unable to load portfolio</h2><p>{error}</p><button type="button" onClick={load}>Try again</button></section>}

      {state === 'ready' && data && (
        <>
          <section className="overview" aria-labelledby="overview-title">
            <div>
              <span className="eyebrow">Overview</span>
              <h2 id="overview-title">{data.portfolio.name}</h2>
              <p className="muted">Reporting in {data.reporting_currency}</p>
            </div>
            <div className={`state-pill state-${data.snapshot.state}`}>
              {data.snapshot.state === 'complete' ? 'Complete valuation' : data.snapshot.state === 'incomplete' ? 'Incomplete valuation' : 'No snapshot'}
            </div>
            {data.snapshot.complete && data.snapshot.reporting_total && (
              <div className="total"><span className="muted">Reporting total</span><strong>{data.snapshot.reporting_total} {data.reporting_currency}</strong></div>
            )}
            <dl className="snapshot-meta">
              <div><dt>Snapshot as-of</dt><dd>{displayDate(data.snapshot.as_of)}</dd></div>
              <div><dt>Version</dt><dd>{data.snapshot.calculation_version || 'Not available'}</dd></div>
            </dl>
          </section>

          {data.snapshot.state === 'no_snapshot' && <section className="notice"><h2>No immutable snapshot yet</h2><p>Holdings are shown from the approved import, but prices and reporting values are withheld until a snapshot is created.</p></section>}
          {data.snapshot.state === 'incomplete' && <section className="notice warning"><h2>Valuation is incomplete</h2><p>Reporting total is intentionally withheld. Missing, stale or invalid market dependencies must be resolved before this view can be complete.</p></section>}

          <section className="table-card" aria-labelledby="holdings-title">
            <div className="section-heading"><div><span className="eyebrow">Source-traceable lots</span><h2 id="holdings-title">Holdings</h2></div><span className="muted">{visibleHoldings.length} shown · {new Set(data.holdings.map((holding) => holding.symbol)).size} symbols</span></div>
            <div className="table-scroll">
              <table>
                <thead><tr>
                  {sortHeader('Instrument', 'symbol')}
                  {sortHeader('Quantity', 'quantity')}
                  {sortHeader('Currency', 'currency')}
                  <th scope="col">Source unit cost</th>
                  {sortHeader('Cost basis', 'cost_basis')}
                  {sortHeader('Latest price', 'latest_price')}
                  {sortHeader('Reporting value', 'reporting_value')}
                  {sortHeader('Freshness', 'freshness')}
                  <th scope="col">Broker / date</th>
                </tr></thead>
                <tbody>
                  {visibleHoldings.map((holding) => <tr key={holding.lot_id}>
                    <th scope="row"><strong>{holding.symbol}</strong><span className="subline">{holding.description && holding.description !== holding.symbol ? holding.description : holding.name && holding.name !== holding.symbol ? holding.name : 'Instrument description unavailable'} · lot {holding.lot_id.slice(-8)}</span></th>
                    <td className="numeric">{holding.quantity}</td>
                    <td>{holding.currency}</td>
                    <td className="numeric">{holding.unit_cost || '—'}</td>
                    <td className="numeric">{holding.cost_basis || '—'}</td>
                    <td className="numeric">{holding.latest_price || '—'}<span className="subline">{holding.latest_value || 'value unavailable'}</span></td>
                    <td className="numeric">{data.snapshot.complete ? (holding.reporting_value || '—') : 'Withheld'}</td>
                    <td><span className={`flag ${qualityClass(holding)}`}>{qualityLabel(holding)}</span>{holding.quote_source && <span className="subline">{holding.quote_source}</span>}</td>
                    <td>{holding.broker || 'Missing'}<span className="subline">{holding.acquisition_date || 'Date missing'}</span></td>
                  </tr>)}
                </tbody>
              </table>
              {!visibleHoldings.length && <p className="empty">No holdings match this filter.</p>}
            </div>
          </section>
        </>
      )}
    </main>
  )
}

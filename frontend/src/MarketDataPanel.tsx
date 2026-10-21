import { useEffect, useMemo, useRef, useState } from 'react'
import { APIError, getMarketDataSetup, refreshMarketData, saveMarketMappings, type MarketDataSetup, type MappingBatchItem } from './api'

function newRequestKey() {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `refresh-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

type Draft = { provider: string; providerSymbol: string; quoteCurrency: string; provenance: string; confirmed: boolean }

function dependencyLabel(value: string, setup: MarketDataSetup | null): string {
  const quote = value.match(/^quote:([^:]+)$/)
  if (quote) {
    const instrument = setup?.instruments.find((item) => item.instrument_id === quote[1] || item.mappings.some((mapping) => mapping.provider_symbol === quote[1]))
    return instrument ? `quote for ${instrument.symbol} (${instrument.currency})` : `quote dependency (${quote[1]})`
  }
  const fx = value.match(/^(?:missing |stale |invalid )?fx:([A-Z]{3})\/([A-Z]{3})$/i)
  if (fx) return `FX conversion ${fx[1].toUpperCase()} to ${fx[2].toUpperCase()}`
  return value.replace(/^(missing |stale |invalid )?fx:/i, 'FX dependency ')
}

function providerErrorLabel(value: string): string {
  const labels: Record<string, string> = { configuration: 'credentials are not configured', auth: 'provider credentials were rejected', rate_limit: 'provider rate limit reached', timeout: 'provider request timed out', transport: 'provider could not be reached', invalid_value: 'provider returned an invalid value', invalid_timestamp: 'provider returned an invalid timestamp', currency_mismatch: 'provider did not return the requested currency', symbol_mismatch: 'provider returned a different symbol', provider_error: 'provider returned an error', persistence: 'the observation could not be stored', mapping_lookup: 'the mapping could not be read', mapping_ambiguous: 'more than one exact mapping matched; choose one explicitly' }
  return labels[value] || value.replace(/_/g, ' ')
}

export default function MarketDataPanel({ portfolioID, reportingCurrency, csrfToken, onUnauthorized, onReload }: { portfolioID: string; reportingCurrency: string; csrfToken?: string; onUnauthorized: () => void; onReload: () => void }) {
  const [setup, setSetup] = useState<MarketDataSetup | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [saving, setSaving] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [lastRefresh, setLastRefresh] = useState<Awaited<ReturnType<typeof refreshMarketData>> | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const mutationControllerRef = useRef<AbortController | null>(null)
  const savingRef = useRef(false)
  const refreshingRef = useRef(false)
  const generationRef = useRef(0)
  const scopeRef = useRef({ portfolioID, reportingCurrency })
  scopeRef.current = { portfolioID, reportingCurrency }
  const reloadRef = useRef(onReload)
  reloadRef.current = onReload
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized

  const load = () => {
    const generation = ++generationRef.current
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setState('loading'); setError(''); setMessage(''); setLastRefresh(null)
    getMarketDataSetup(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current.portfolioID) return
      setSetup(next)
      const nextDrafts: Record<string, Draft> = {}
      for (const instrument of next.instruments) {
        const mapping = instrument.mappings[0]
        nextDrafts[instrument.instrument_id] = { provider: mapping?.provider || '', providerSymbol: mapping?.provider_symbol || '', quoteCurrency: mapping?.quote_currency || instrument.currency, provenance: mapping?.provenance || '', confirmed: Boolean(mapping) }
      }
      setDrafts(nextDrafts); setState('ready')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { generationRef.current += 1; mutationControllerRef.current?.abort(); unauthorizedRef.current(); return }
      setError(reason instanceof Error ? reason.message : 'Unable to load market-data setup'); setState('error')
    })
  }

  useEffect(() => {
    load()
    return () => { generationRef.current += 1; controllerRef.current?.abort(); mutationControllerRef.current?.abort(); controllerRef.current = null; mutationControllerRef.current = null }
  }, [portfolioID, reportingCurrency])

  const update = (id: string, field: keyof Draft, value: string | boolean) => setDrafts((current) => ({ ...current, [id]: { ...current[id], [field]: value } }))
  const entries = useMemo(() => setup?.instruments || [], [setup])
  const mappings = entries.map((instrument): MappingBatchItem | null => {
    const draft = drafts[instrument.instrument_id]
    if (!draft?.provider && !draft?.providerSymbol && !draft?.provenance) return null
    if (!draft?.provider || !draft.providerSymbol || !draft.provenance || !draft.confirmed) return null
    return { instrument_id: instrument.instrument_id, provider: draft.provider, provider_symbol: draft.providerSymbol, quote_currency: draft.quoteCurrency, provenance: draft.provenance, confirm_unverified: draft.confirmed }
  }).filter((item): item is MappingBatchItem => item !== null)

  const save = async () => {
    if (!setup || savingRef.current || refreshingRef.current) return
    const generation = generationRef.current
    const controller = new AbortController()
    mutationControllerRef.current?.abort()
    mutationControllerRef.current = controller
    savingRef.current = true
    setSaving(true); setError(''); setMessage('')
    try {
      const result = await saveMarketMappings(portfolioID, setup.etag, mappings, csrfToken, controller.signal)
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
      const expectedIDs = new Set(entries.map((instrument) => instrument.instrument_id))
      const outcomeIDs = result.outcomes.map((outcome) => outcome.instrument_id)
      if (outcomeIDs.length !== expectedIDs.size || new Set(outcomeIDs).size !== outcomeIDs.length || outcomeIDs.some((id) => !expectedIDs.has(id))) throw new APIError(200, 'Invalid mapping batch response: stale instrument scope.')
      setMessage(`Saved ${result.outcomes.length} mapping${result.outcomes.length === 1 ? '' : 's'} as a new revision.`)
      setSaving(false); savingRef.current = false
      load()
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { generationRef.current += 1; unauthorizedRef.current(); return }
      setError(reason instanceof APIError && reason.status === 412 ? 'This setup changed in another window. Reload the setup before saving.' : reason instanceof Error ? reason.message : 'Unable to save mappings')
    } finally {
      if (generation === generationRef.current) { setSaving(false); savingRef.current = false }
      if (mutationControllerRef.current === controller) mutationControllerRef.current = null
    }
  }

  const refresh = async () => {
    if (refreshingRef.current || savingRef.current) return
    const generation = generationRef.current
    const controller = new AbortController()
    mutationControllerRef.current?.abort()
    mutationControllerRef.current = controller
    refreshingRef.current = true
    setRefreshing(true); setError(''); setMessage('Refreshing prices and creating an immutable snapshot…')
    try {
      const result = await refreshMarketData(portfolioID, reportingCurrency, newRequestKey(), csrfToken, controller.signal, setup || undefined)
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID || scopeRef.current.reportingCurrency !== reportingCurrency) return
      if (result.refresh.portfolio_id !== portfolioID || result.snapshot.portfolio_id !== portfolioID || result.snapshot.reporting_currency !== reportingCurrency.toUpperCase()) throw new APIError(200, 'Invalid market refresh response: stale portfolio scope.')
      setLastRefresh(result)
      if (result.complete) { setMessage('Prices refreshed and a complete immutable snapshot was created.'); reloadRef.current() }
      else setMessage('Refresh completed, but the snapshot remains incomplete. No partial total is shown.')
      if (result.missing_instruments.length) setError(`Missing exact mappings: ${result.missing_instruments.join(', ')}`)
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { generationRef.current += 1; unauthorizedRef.current(); return }
      setError(reason instanceof Error ? reason.message : 'Unable to refresh prices')
    } finally {
      if (generation === generationRef.current) { setRefreshing(false); refreshingRef.current = false }
      if (mutationControllerRef.current === controller) mutationControllerRef.current = null
    }
  }

  return <section className="market-data-card" aria-labelledby="market-data-title">
    <div className="section-heading"><div><span className="eyebrow">One-time setup, revisioned</span><h2 id="market-data-title">Market data</h2></div><span className="muted">Provider keys authorize access; mappings identify exact listings.</span></div>
    <p className="muted">Mappings are explicit and human-reviewed. They are not required again for each refresh. International listings, ADRs and crypto IDs must use the exact provider identifier; the dashboard never guesses from a holding symbol.</p>
    <div className="provider-status" aria-label="Provider readiness">{setup?.providers.map((provider) => <span className={`provider-chip ${provider.configured ? 'ready' : 'not-ready'} `} key={provider.name}>{provider.name}: {provider.configured ? 'ready' : 'not configured'}</span>)}</div>
    {state === 'loading' && <p role="status" aria-live="polite">Loading market-data setup…</p>}
    {state === 'error' && <p className="notice" role="status">Unable to load market-data setup. <button type="button" onClick={load}>Try again</button></p>}
    {state === 'ready' && <>
      <div className="table-scroll"><table className="market-data-table"><caption>Exact provider mapping setup for every active portfolio instrument</caption><thead><tr><th scope="col">Instrument</th><th scope="col">Provider</th><th scope="col">Exact provider symbol or ID</th><th scope="col">Quote currency</th><th scope="col">Provenance / confirmation</th></tr></thead><tbody>{entries.map((instrument) => {
        const draft = drafts[instrument.instrument_id]
        const available = setup?.providers.filter((provider) => provider.name !== 'open_exchange_rates' && (provider.configured || provider.public)) || []
        return <tr key={instrument.instrument_id}><th scope="row"><strong>{instrument.symbol}</strong><span className="subline">{instrument.description || 'Description unavailable'} · {instrument.currency}</span><span className="subline">{instrument.mappings.length ? `Current: ${instrument.mappings.map((mapping) => `${mapping.provider} / ${mapping.provider_symbol}`).join(', ')}` : 'Not mapped'}</span></th><td><select aria-label={`Provider for ${instrument.symbol}`} value={draft?.provider || ''} onChange={(event) => update(instrument.instrument_id, 'provider', event.target.value)}><option value="">Choose exact provider</option>{available.map((provider) => <option key={provider.name} value={provider.name}>{provider.name}</option>)}</select></td><td><input aria-label={`Exact provider identifier for ${instrument.symbol}`} value={draft?.providerSymbol || ''} onChange={(event) => update(instrument.instrument_id, 'providerSymbol', event.target.value)} placeholder="Exact listing / coin ID" /></td><td><input aria-label={`Quote currency for ${instrument.symbol}`} value={draft?.quoteCurrency || instrument.currency} maxLength={3} onChange={(event) => update(instrument.instrument_id, 'quoteCurrency', event.target.value.toUpperCase())} /></td><td><input aria-label={`Provenance for ${instrument.symbol}`} value={draft?.provenance || ''} onChange={(event) => update(instrument.instrument_id, 'provenance', event.target.value)} placeholder="Where you verified it" /><label className="checkbox-label"><input type="checkbox" checked={draft?.confirmed || false} onChange={(event) => update(instrument.instrument_id, 'confirmed', event.target.checked)} /> I confirmed this exact identifier</label></td></tr>
      })}</tbody></table></div>
      <div className="market-actions"><button type="button" onClick={save} disabled={saving || refreshing}>{saving ? 'Saving mappings…' : 'Save all mappings'}</button><button type="button" onClick={refresh} disabled={refreshing || saving}>{refreshing ? 'Refreshing prices…' : 'Refresh prices'}</button></div>
      <p className="muted">Save validates the whole batch before committing. A stale setup is rejected rather than overwriting another revision.</p>
    </>}
    {lastRefresh && <section className="market-refresh-status" aria-labelledby="refresh-status-title" aria-live="polite"><h3 id="refresh-status-title">Latest refresh: {lastRefresh.complete ? 'complete' : 'incomplete'}</h3><p className="muted">{lastRefresh.freshness_policy}</p><ul>{lastRefresh.refresh.providers.map((provider) => <li key={provider.provider}><strong>{provider.provider}</strong>: {provider.status} · {provider.succeeded_count} succeeded · {provider.failed_count} failed · {provider.missing_mapping_count} missing{provider.errors.length > 0 && <span> — {provider.errors.map(providerErrorLabel).join('; ')}</span>}</li>)}</ul>{!lastRefresh.complete && <div className="notice warning" role="status"><strong>Dependencies requiring attention:</strong><ul>{[...lastRefresh.missing_instruments.map((item) => `missing mapping: ${item}`), ...lastRefresh.snapshot.missing_dependencies.map((item) => `missing ${dependencyLabel(item, setup)}`), ...lastRefresh.snapshot.stale_dependencies.map((item) => `stale ${dependencyLabel(item, setup)}`), ...lastRefresh.snapshot.invalid_dependencies.map((item) => `invalid or future ${dependencyLabel(item, setup)}`)].map((item) => <li key={item}>{item}</li>)}</ul><p>Totals are withheld until every quote and FX dependency is valid and fresh.</p></div>}{lastRefresh.snapshot.complete ? <p role="status">Immutable snapshot is complete.</p> : <p className="notice warning" role="status">Immutable snapshot is incomplete; totals remain withheld.</p>}</section>}
    {message && <p className="notice" role="status" aria-live="polite">{message}</p>}
    {error && <p className="notice error" role="alert" aria-live="polite">{error}</p>}
  </section>
}

import { useEffect, useMemo, useRef, useState } from 'react'
import { APIError, discoverMarketData, getMarketDataSetup, saveMarketMappings, type MarketDataSetup, type MappingBatchItem, type MarketDiscoveryCandidate } from './api'
import { prefillMappingSuggestion } from './marketMappingSuggestions'

type Draft = { provider: string; providerSymbol: string; quoteCurrency: string; provenance: string; confirmed: boolean; suggested: boolean; guidance: string; candidates: MarketDiscoveryCandidate[] }

function canonicalQuoteCurrency(value: string | undefined): string | null {
  const trimmed = value?.trim() || ''
  return /^[A-Z]{3}$/.test(trimmed) ? trimmed : null
}

function quoteCurrencyError(value: string | undefined): string {
  return value?.trim() ? 'Use a three-letter uppercase currency code, such as USD.' : 'Quote currency is required before saving this mapping.'
}

function normalizedSymbol(value: string | undefined): string {
  return (value || '').trim().toUpperCase()
}

function candidateEvidence(candidate: MarketDiscoveryCandidate): string {
  return [candidate.identifier, candidate.symbol, candidate.name, candidate.type, candidate.region, candidate.exchange, candidate.currency].filter(Boolean).join(' · ')
}

export default function MarketDataPanel({ portfolioID, reportingCurrency, csrfToken, onUnauthorized, onReload }: { portfolioID: string; reportingCurrency: string; csrfToken?: string; onUnauthorized: () => void; onReload: () => void }) {
  const [setup, setSetup] = useState<MarketDataSetup | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const controllerRef = useRef<AbortController | null>(null)
  const discoveryControllerRef = useRef<AbortController | null>(null)
  const mutationControllerRef = useRef<AbortController | null>(null)
  const savingRef = useRef(false)

  const generationRef = useRef(0)
  const scopeRef = useRef({ portfolioID, reportingCurrency })
  scopeRef.current = { portfolioID, reportingCurrency }
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized

  const load = () => {
    const generation = ++generationRef.current
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setState('loading'); setError(''); setMessage('')
    getMarketDataSetup(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current.portfolioID) return
      setSetup(next)
      const nextDrafts: Record<string, Draft> = {}
      for (const instrument of next.instruments) {
        const seed = prefillMappingSuggestion(instrument, next.providers)
        nextDrafts[instrument.instrument_id] = { ...seed, candidates: [] }
      }
      setDrafts(nextDrafts); setState('ready')
      const discoveryController = new AbortController()
      discoveryControllerRef.current = discoveryController
      setMessage('Contacting configured providers for unverified candidates…')
      discoverMarketData(portfolioID, next.etag, csrfToken, discoveryController.signal, next).then((discovery) => {
        if (discoveryController.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
        setDrafts((current) => {
          const updated = { ...current }
          for (const result of discovery.results) {
            const instrument = next.instruments.find((item) => item.instrument_id === result.instrument_id)
            const existing = updated[result.instrument_id]
            if (!instrument || !existing || instrument.mappings.length > 0 || result.status === 'committed') continue
            const exact = result.candidates.filter((candidate) => normalizedSymbol(candidate.symbol || candidate.identifier) === normalizedSymbol(instrument.symbol) && (!candidate.currency || candidate.currency === instrument.currency))
            if (exact.length === 1) {
              const candidate = exact[0]
              const evidence = candidateEvidence(candidate)
              updated[result.instrument_id] = { ...existing, provider: candidate.provider, providerSymbol: candidate.identifier, provenance: `Suggested by ${candidate.provider} — unverified. Provider-reported identity: ${evidence}`, confirmed: false, suggested: true, candidates: result.candidates, guidance: result.diagnostic || 'A unique exact provider candidate was returned; verify it before confirming.' }
            } else {
              updated[result.instrument_id] = { ...existing, candidates: result.candidates, guidance: exact.length > 1 ? 'Multiple exact provider candidates were returned. Choose one explicitly, then verify it.' : (result.diagnostic || 'No unique exact provider candidate was returned. Enter and verify the identifier manually.') }
            }
          }
          return updated
        })
        setMessage('Provider discovery completed. Suggestions remain unverified until you confirm them.')
      }).catch((reason: unknown) => {
        if (discoveryController.signal.aborted || generation !== generationRef.current) return
        if (reason instanceof APIError && reason.status === 401) { generationRef.current += 1; mutationControllerRef.current?.abort(); unauthorizedRef.current(); return }
        setMessage('Provider discovery was unavailable. Enter and verify exact identifiers manually.')
      })
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { generationRef.current += 1; mutationControllerRef.current?.abort(); unauthorizedRef.current(); return }
      setError(reason instanceof Error ? reason.message : 'Unable to load market-data setup'); setState('error')
    })
  }

  useEffect(() => {
    load()
    return () => { generationRef.current += 1; controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort(); controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null }
  }, [portfolioID, reportingCurrency])

  const update = (id: string, field: keyof Draft, value: string | boolean) => setDrafts((current) => ({ ...current, [id]: { ...current[id], [field]: value } }))
  const manualUpdate = (id: string, field: 'provider' | 'providerSymbol' | 'provenance', value: string) => setDrafts((current) => ({ ...current, [id]: { ...current[id], [field]: value, ...(field === 'provider' || field === 'providerSymbol' ? { suggested: false, confirmed: false, provenance: 'Manual entry — owner must verify the exact provider identity.' } : {}) } }))
  const entries = useMemo(() => setup?.instruments || [], [setup])
  const mappings = entries.map((instrument): MappingBatchItem | null => {
    const draft = drafts[instrument.instrument_id]
    if (!draft?.provider?.trim() && !draft?.providerSymbol?.trim() && !draft?.provenance?.trim()) return null
    const quoteCurrency = canonicalQuoteCurrency(draft?.quoteCurrency)
    if (!draft?.provider?.trim() || !draft.providerSymbol.trim() || !draft.provenance.trim() || !draft.confirmed || !quoteCurrency) return null
    return { instrument_id: instrument.instrument_id, provider: draft.provider, provider_symbol: draft.providerSymbol, quote_currency: quoteCurrency, provenance: draft.provenance, confirm_unverified: draft.confirmed }
  }).filter((item): item is MappingBatchItem => item !== null)
  const canSave = entries.length > 0 && mappings.length === entries.length

  const save = async () => {
    if (!setup || savingRef.current) return
    if (mappings.length !== entries.length) {
      setError('Confirm an exact provider mapping for every active instrument before saving the batch.')
      return
    }
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
        const instrumentLabel = `${instrument.symbol} (${instrument.description || instrument.currency})`
        const quoteCurrency = draft?.quoteCurrency ?? ''
        const quoteCurrencyID = `quote-currency-${instrument.instrument_id}`
        const quoteCurrencyValid = canonicalQuoteCurrency(quoteCurrency) !== null
        return <tr key={instrument.instrument_id}><th scope="row"><strong>{instrument.symbol}</strong><span className="subline">{instrument.description || 'Description unavailable'} · {instrument.currency}</span>{instrument.mappings.length ? <span className="subline">Current / confirmed: {instrument.mappings.map((mapping) => `${mapping.provider} / ${mapping.provider_symbol}`).join(', ')}</span> : <><span className="subline">{draft?.guidance || 'No mapping suggestion is available.'}</span>{draft?.suggested && <span className="subline">Suggested by {draft.provider} — unverified. Verify provider-reported identity before confirming.</span>}{draft?.candidates && draft.candidates.length > 0 && <fieldset className="candidate-list"><legend>Provider candidates — choose explicitly</legend>{draft.candidates.map((candidate) => <label className="candidate-choice" key={`${candidate.provider}:${candidate.identifier}`}><input type="radio" name={`candidate-${instrument.instrument_id}`} checked={draft.provider === candidate.provider && draft.providerSymbol === candidate.identifier} onChange={() => { update(instrument.instrument_id, 'provider', candidate.provider); update(instrument.instrument_id, 'providerSymbol', candidate.identifier); update(instrument.instrument_id, 'provenance', `Selected ${candidate.provider} candidate — unverified. Provider-reported identity: ${candidateEvidence(candidate)}`); update(instrument.instrument_id, 'confirmed', false) }} /> <span>{candidate.provider}: {candidate.identifier}{candidate.name ? ` — ${candidate.name}` : ''}{candidate.currency ? ` (${candidate.currency})` : ''}</span></label>)}</fieldset>}</>}</th><td><select aria-label={`Provider for ${instrumentLabel}`} value={draft?.provider || ''} onChange={(event) => manualUpdate(instrument.instrument_id, 'provider', event.target.value)}><option value="">Choose exact provider</option>{available.map((provider) => <option key={provider.name} value={provider.name}>{provider.name}</option>)}</select></td><td><input aria-label={`Exact provider identifier for ${instrumentLabel}`} value={draft?.providerSymbol || ''} onChange={(event) => manualUpdate(instrument.instrument_id, 'providerSymbol', event.target.value)} placeholder="Exact listing / coin ID" /></td><td><input aria-label={`Quote currency for ${instrumentLabel}`} aria-describedby={quoteCurrencyID} aria-invalid={!quoteCurrencyValid} value={quoteCurrency} onChange={(event) => update(instrument.instrument_id, 'quoteCurrency', event.target.value)} /><span id={quoteCurrencyID} className="subline">{quoteCurrencyValid ? 'Three-letter uppercase currency code.' : quoteCurrencyError(quoteCurrency)}</span></td><td><input aria-label={`Provenance for ${instrumentLabel}`} value={draft?.provenance || ''} onChange={(event) => update(instrument.instrument_id, 'provenance', event.target.value)} placeholder="Where you verified it" /><label className="checkbox-label"><input type="checkbox" checked={draft?.confirmed || false} onChange={(event) => update(instrument.instrument_id, 'confirmed', event.target.checked)} /> I confirmed this exact identifier for {instrumentLabel}</label></td></tr>
      })}</tbody></table></div>
      <div className="market-actions"><button type="button" onClick={save} disabled={saving || !canSave}>{saving ? 'Saving mappings…' : 'Save all mappings'}</button></div>
      <p className="muted">Save validates the whole batch before committing. Every row needs an exact editable identifier and explicit confirmation; unverified suggestions remain local until you confirm them. A stale setup is rejected rather than overwriting another revision.</p>
    </>}
    {message && <p className="notice" role="status" aria-live="polite">{message}</p>}
    {error && <p className="notice error" role="alert" aria-live="polite">{error}</p>}
  </section>
}

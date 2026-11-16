import { useEffect, useMemo, useRef, useState } from 'react'
import { APIError, discoverMarketData, getMarketDataSetup, saveMarketMappings, type MarketDataSetup, type MappingBatchItem, type MarketDiscoveryCandidate } from './api'
import { prefillMappingSuggestion, type MappingDraftSeed } from './marketMappingSuggestions'

type Draft = MappingDraftSeed & { touched: boolean; candidates: MarketDiscoveryCandidate[] }
type SaveConflict = 'stale' | 'uncertain' | null
type ReadOptions = { discover?: boolean; adoptDrafts?: boolean; reconcile?: boolean; resetDrafts?: boolean; acknowledge?: boolean }

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
  return [
    `Identifier: ${candidate.identifier}`,
    `Symbol: ${candidate.symbol || 'Not supplied'}`,
    `Name: ${candidate.name || 'Not supplied'}`,
    `Description: ${candidate.description || 'Not supplied'}`,
    `Type: ${candidate.type || 'Not supplied'}`,
    `Region: ${candidate.region || 'Not supplied'}`,
    `Exchange: ${candidate.exchange || 'Not supplied'}`,
    `Currency: ${candidate.currency || 'Not supplied'}`,
  ].join(' · ')
}

function draftSeeds(next: MarketDataSetup): Record<string, Draft> {
  return Object.fromEntries(next.instruments.map((instrument) => [instrument.instrument_id, { ...prefillMappingSuggestion(instrument, next.providers), touched: false, candidates: [] }]))
}

export default function MarketDataPanel({ portfolioID, reportingCurrency, csrfToken, onUnauthorized }: { portfolioID: string; reportingCurrency: string; csrfToken?: string; onUnauthorized: () => void }) {
  const [setup, setSetup] = useState<MarketDataSetup | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [state, setState] = useState<'loading' | 'ready' | 'error'>('loading')
  const [saving, setSaving] = useState(false)
  const [discoveryMessage, setDiscoveryMessage] = useState('')
  const [readError, setReadError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [saveReceipt, setSaveReceipt] = useState('')
  const [conflict, setConflict] = useState<SaveConflict>(null)
  const [adoptionChoice, setAdoptionChoice] = useState(false)
  const [reconciliationRequired, setReconciliationRequired] = useState(false)
  const controllerRef = useRef<AbortController | null>(null)
  const discoveryControllerRef = useRef<AbortController | null>(null)
  const mutationControllerRef = useRef<AbortController | null>(null)
  const savingRef = useRef(false)
  const generationRef = useRef(0)
  const setupRef = useRef<MarketDataSetup | null>(null)
  const conflictRef = useRef<SaveConflict>(null)
  const scopeRef = useRef({ portfolioID, reportingCurrency })
  scopeRef.current = { portfolioID, reportingCurrency }
  const unauthorizedRef = useRef(onUnauthorized)
  unauthorizedRef.current = onUnauthorized
  setupRef.current = setup
  conflictRef.current = conflict
  const rowRefs = useRef<Record<string, HTMLTableRowElement | null>>({})

  const expireSession = () => {
    generationRef.current += 1
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
    controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null
    savingRef.current = false
    setSaving(false); setSetup(null); setDrafts({}); setState('loading')
    setDiscoveryMessage(''); setReadError(''); setSaveError(''); setSaveReceipt('')
    setConflict(null); setAdoptionChoice(false); setReconciliationRequired(false)
    setupRef.current = null; conflictRef.current = null
    unauthorizedRef.current()
  }

  const applyDiscovery = (next: MarketDataSetup, generation: number, controller: AbortController) => {
    discoverMarketData(portfolioID, next.etag, csrfToken, controller.signal, next).then((discovery) => {
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
      setDrafts((current) => {
        const updated = { ...current }
        for (const result of discovery.results) {
          const instrument = next.instruments.find((item) => item.instrument_id === result.instrument_id)
          const existing = updated[result.instrument_id]
          if (!instrument || !existing || existing.touched || existing.confirmed || instrument.mappings.length > 0 || result.status === 'committed') continue
          const exact = result.candidates.filter((candidate) => normalizedSymbol(candidate.symbol || candidate.identifier) === normalizedSymbol(instrument.symbol) && (!candidate.currency || candidate.currency === instrument.currency))
          let guidance = result.diagnostic || 'No unique exact provider candidate was returned. Enter and verify the identifier manually.'
          if (result.status === 'no_result') guidance = result.diagnostic || 'No provider candidate was found. Enter and verify the exact identifier manually.'
          if (result.status === 'unavailable' || result.status === 'error') guidance = result.diagnostic || 'Provider discovery failed for this instrument. Enter and verify the exact identifier manually.'
          if (result.status === 'candidates' && exact.length > 1) guidance = 'Multiple exact provider candidates were returned. Choose one explicitly, then verify it.'
          if (result.status === 'candidates' && exact.length === 1) {
            const candidate = exact[0]
            updated[result.instrument_id] = { ...existing, provider: candidate.provider, providerSymbol: candidate.identifier, provenance: `Suggested by ${candidate.provider} — unverified. Provider-reported identity: ${candidateEvidence(candidate)}`, confirmed: false, suggested: true, candidates: result.candidates, guidance: result.diagnostic || 'A unique exact provider candidate was returned; verify it before confirming.' }
          } else updated[result.instrument_id] = { ...existing, candidates: result.candidates, guidance }
        }
        return updated
      })
      setDiscoveryMessage('Provider discovery completed. Suggestions remain unverified until you confirm them.')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      setDiscoveryMessage('Provider discovery was unavailable. Enter and verify exact identifiers manually.')
    })
  }

  const readSetup = (options: ReadOptions = {}) => {
    const discover = options.discover === true
    const adoptDrafts = options.adoptDrafts === true
    const reconcile = options.reconcile === true
    const resetDrafts = options.resetDrafts === true
    const acknowledge = options.acknowledge === true
    const generation = ++generationRef.current
    controllerRef.current?.abort(); discoveryControllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    if (!setup) setState('loading')
    setReadError('')
    if (adoptDrafts) setAdoptionChoice(false)
    getMarketDataSetup(portfolioID, controller.signal).then((next) => {
      if (controller.signal.aborted || generation !== generationRef.current || next.portfolio_id !== scopeRef.current.portfolioID) return
      const previousSetup = setupRef.current
      setSetup(next)
      setupRef.current = next
      if (resetDrafts || adoptDrafts || previousSetup?.portfolio_id !== next.portfolio_id) setDrafts(draftSeeds(next))
      setState('ready'); setReconciliationRequired(false)
      if (adoptDrafts || acknowledge || (reconcile && conflictRef.current !== 'uncertain')) {
        setConflict(null)
        conflictRef.current = null
      }
      if (adoptDrafts || (acknowledge && conflictRef.current === null)) { setSaveError('') }
      if (adoptDrafts) setSaveReceipt('')
      if (!adoptDrafts && conflictRef.current === 'stale') setAdoptionChoice(true)
      if (discover && next.instruments.length > 0) {
        const discoveryController = new AbortController()
        discoveryControllerRef.current = discoveryController
        setDiscoveryMessage('Contacting configured providers for unverified candidates…')
        applyDiscovery(next, generation, discoveryController)
      } else if (discover) setDiscoveryMessage('No active instruments require provider discovery.')
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      const message = reason instanceof Error ? reason.message : 'Unable to load market-data setup'
      if (setup) setReadError(`Could not reload Market Data setup: ${message}. Use Reload setup to try again.`)
      else { setReadError(message); setState('error') }
    })
  }

  useEffect(() => {
    readSetup({ discover: true, resetDrafts: true })
    return () => {
      generationRef.current += 1
      controllerRef.current?.abort(); discoveryControllerRef.current?.abort(); mutationControllerRef.current?.abort()
      controllerRef.current = null; discoveryControllerRef.current = null; mutationControllerRef.current = null
      savingRef.current = false
      setSaving(false); setSetup(null); setDrafts({}); setState('loading')
      setDiscoveryMessage(''); setSaveReceipt(''); setSaveError(''); setReadError('')
      setConflict(null); setAdoptionChoice(false); setReconciliationRequired(false)
      setupRef.current = null; conflictRef.current = null
    }
  }, [portfolioID, reportingCurrency])

  const editDraft = (id: string, field: keyof Draft, value: string | boolean) => {
    setDrafts((current) => {
      const existing = current[id]
      if (!existing) return current
      const identityEdit = field === 'provider' || field === 'providerSymbol' || field === 'quoteCurrency'
      return { ...current, [id]: { ...existing, [field]: value, touched: true, ...(identityEdit ? { confirmed: false, suggested: false } : {}) } }
    })
  }

  const manualUpdate = (id: string, field: 'provider' | 'providerSymbol' | 'provenance', value: string) => {
    setDrafts((current) => {
      const existing = current[id]
      if (!existing) return current
      return { ...current, [id]: { ...existing, [field]: value, touched: true, ...(field === 'provider' || field === 'providerSymbol' ? { suggested: false, confirmed: false, provenance: 'Manual entry — owner must verify the exact provider identity.' } : field === 'provenance' ? { suggested: false, confirmed: false } : {}) } }
    })
  }

  const entries = useMemo(() => setup?.instruments || [], [setup])
  const mappings = entries.map((instrument): MappingBatchItem | null => {
    const draft = drafts[instrument.instrument_id]
    if (!draft?.provider?.trim() && !draft?.providerSymbol?.trim() && !draft?.provenance?.trim()) return null
    const quoteCurrency = canonicalQuoteCurrency(draft?.quoteCurrency)
    if (!draft?.provider?.trim() || !draft.providerSymbol.trim() || !draft.provenance.trim() || !draft.confirmed || !quoteCurrency) return null
    return { instrument_id: instrument.instrument_id, provider: draft.provider, provider_symbol: draft.providerSymbol, quote_currency: quoteCurrency, provenance: draft.provenance, confirm_unverified: draft.confirmed }
  }).filter((item): item is MappingBatchItem => item !== null)
  const confirmedCount = mappings.length
  const firstIncomplete = entries.find((instrument) => !mappings.some((mapping) => mapping.instrument_id === instrument.instrument_id))
  const firstIncompleteLabel = firstIncomplete ? `${firstIncomplete.symbol} (${firstIncomplete.description || firstIncomplete.currency})` : ''
  const saveDisabledReason = entries.length === 0 ? 'No active instruments to map. Add a holding in Holdings first.' : firstIncomplete ? `Finish mapping for ${firstIncompleteLabel}. Finish ${firstIncompleteLabel} before saving: choose a provider, enter the exact identifier, provide provenance, use a valid quote currency, and confirm the mapping.` : ''
  const canSave = entries.length > 0 && mappings.length === entries.length && !saving && !reconciliationRequired && conflict === null
  const focusNextUnfinished = () => {
    const row = firstIncomplete && rowRefs.current[firstIncomplete.instrument_id]
    row?.querySelector<HTMLElement>('select, input:not([type="hidden"]), button')?.focus()
  }
  const reloadForConflict = () => { setAdoptionChoice(true); readSetup({ discover: false }) }

  const save = async () => {
    if (!setup || savingRef.current || !canSave) return
    const generation = generationRef.current
    const saveMappings = mappings
    const expectedIDs = new Set(entries.map((instrument) => instrument.instrument_id))
    const controller = new AbortController()
    let reloadAfterSave = false
    mutationControllerRef.current?.abort(); mutationControllerRef.current = controller
    savingRef.current = true; setSaving(true); setSaveError(''); setReadError('')
    try {
      const result = await saveMarketMappings(portfolioID, setup.etag, saveMappings, csrfToken, controller.signal)
      if (controller.signal.aborted || generation !== generationRef.current || scopeRef.current.portfolioID !== portfolioID) return
      const outcomeIDs = result.outcomes.map((outcome) => outcome.instrument_id)
      if (outcomeIDs.length !== expectedIDs.size || new Set(outcomeIDs).size !== outcomeIDs.length || outcomeIDs.some((id) => !expectedIDs.has(id))) throw new APIError(200, 'Invalid mapping batch response: stale instrument scope.')
      setSaveReceipt(`Saved ${result.outcomes.length} mapping${result.outcomes.length === 1 ? '' : 's'} as a new revision.`)
      setConflict(null)
      setReconciliationRequired(true)
      reloadAfterSave = true
    } catch (reason) {
      if (controller.signal.aborted || generation !== generationRef.current) return
      if (reason instanceof APIError && reason.status === 401) { expireSession(); return }
      if (reason instanceof APIError && reason.status === 412) {
        setConflict('stale')
        setSaveError('Mappings were not saved because the setup changed in another window. Reload setup, then choose an explicit draft option.')
      } else {
        const uncertain = !(reason instanceof APIError) || reason.status === 200 || reason.status >= 500
        if (uncertain) {
          setConflict('uncertain')
          conflictRef.current = 'uncertain'
          setReconciliationRequired(true)
          const detail = reason instanceof Error ? ` ${reason.message}` : ''
          setSaveError(`Mapping save outcome is uncertain and not confirmed.${detail} Reload setup to reconcile before trying Save again.`)
          reloadAfterSave = true
        } else {
          setSaveError(reason instanceof Error ? `Mapping save was rejected. Your edits are kept. ${reason.message}` : 'Mapping save was rejected. Your edits are kept.')
        }
      }
    } finally {
      if (generation === generationRef.current) { setSaving(false); savingRef.current = false }
      if (mutationControllerRef.current === controller) mutationControllerRef.current = null
      if (reloadAfterSave && generation === generationRef.current) readSetup({ discover: false, reconcile: true })
    }
  }

  const controlsDisabled = saving || reconciliationRequired || conflict !== null
  return <section className="market-data-card" aria-labelledby="market-data-title">
    <div className="section-heading"><div><span className="eyebrow">One-time setup, revisioned</span><h2 id="market-data-title">Market data</h2></div><span className="muted">Provider keys authorize access; mappings identify exact listings.</span></div>
    <p className="muted">Mappings are explicit and human-reviewed. They are not required again for each refresh. International listings, ADRs and crypto IDs must use the exact provider identifier; the dashboard never guesses from a holding symbol.</p>
    <div className="provider-status" aria-label="Provider readiness">{setup?.providers.map((provider) => <span className={`provider-chip ${provider.configured ? 'ready' : 'not-ready'} `} key={provider.name}>{provider.name}: {provider.configured ? 'ready' : 'not configured'}</span>)}</div>
    {state === 'loading' && <p role="status" aria-live="polite">Loading market-data setup…</p>}
    {state === 'error' && <p className="notice" role="status">Unable to load market-data setup. <button type="button" onClick={() => readSetup({ discover: true })}>Try again</button></p>}
    {readError && <p className="notice error" role="alert" aria-live="polite">{readError} <button type="button" aria-label="Reload setup" onClick={() => readSetup({ discover: false, acknowledge: true })}>Retry</button></p>}
    {state === 'ready' && <>
      {entries.length === 0 ? <p className="empty-state"><strong>No active instruments to map.</strong> <span>Add a holding in Holdings first.</span></p> : <>
        <div className="mapping-progress" aria-live="polite"><strong>{confirmedCount} of {entries.length} mappings confirmed.</strong> {!firstIncomplete && <span>All active instruments are ready for one atomic save.</span>} {firstIncomplete && <button type="button" onClick={focusNextUnfinished} disabled={controlsDisabled} aria-label="Next unfinished mapping">Next unfinished mapping</button>}</div>
        <div className="table-scroll horizontal-scroll-region market-data-table-scroll" tabIndex={0} role="region" aria-label="Market data mappings table horizontal scroll region" aria-describedby="market-data-table-scroll-help"><p id="market-data-table-scroll-help" className="visually-hidden">Use horizontal scrolling to view all Market Data mapping columns. The sticky Instrument column identifies each mapping while you scan.</p><table className="market-data-table"><caption>Exact provider mapping setup for every active portfolio instrument</caption><thead><tr><th className="mapping-identity-cell" scope="col">Instrument</th><th scope="col">Provider</th><th scope="col">Exact provider symbol or ID</th><th scope="col">Quote currency</th><th scope="col">Provenance / confirmation</th></tr></thead><tbody>{entries.map((instrument) => {
          const draft = drafts[instrument.instrument_id]
          const available = setup?.providers.filter((provider) => provider.name !== 'open_exchange_rates' && (provider.configured || provider.public)) || []
          const instrumentLabel = `${instrument.symbol} (${instrument.description || instrument.currency})`
          const quoteCurrency = draft?.quoteCurrency ?? ''
          const quoteCurrencyID = `quote-currency-${instrument.instrument_id}`
          const quoteCurrencyValid = canonicalQuoteCurrency(quoteCurrency) !== null
          return <tr key={instrument.instrument_id} ref={(row) => { rowRefs.current[instrument.instrument_id] = row }}><th className="mapping-identity-cell" scope="row"><strong>{instrument.symbol}</strong><span className="subline">{instrument.description || 'Description unavailable'} · {instrument.currency}</span>{instrument.mappings.length > 0 && !draft?.touched ? <span className="subline">Current / confirmed: {instrument.mappings.map((mapping) => `${mapping.provider} / ${mapping.provider_symbol}`).join(', ')}</span> : <><span className="subline">{draft?.guidance || 'No mapping suggestion is available.'}</span>{draft?.suggested && <span className="subline">Suggested by {draft.provider} — unverified. Verify provider-reported identity before confirming.</span>}{draft?.candidates && draft.candidates.length > 0 && <fieldset className="candidate-list"><legend>Provider candidates — choose explicitly</legend>{draft.candidates.map((candidate) => <label className="candidate-choice" key={`${candidate.provider}:${candidate.identifier}`}><input type="radio" name={`candidate-${instrument.instrument_id}`} disabled={controlsDisabled} checked={draft.provider === candidate.provider && draft.providerSymbol === candidate.identifier} onChange={() => setDrafts((current) => ({ ...current, [instrument.instrument_id]: { ...current[instrument.instrument_id], provider: candidate.provider, providerSymbol: candidate.identifier, provenance: `Selected ${candidate.provider} candidate — unverified. Provider-reported identity: ${candidateEvidence(candidate)}`, confirmed: false, suggested: false, touched: true } }))} /> <span>{candidate.provider}: {candidateEvidence(candidate)}</span></label>)}</fieldset>}</>}</th><td><select aria-label={`Provider for ${instrumentLabel}`} value={draft?.provider || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'provider', event.target.value)}><option value="">Choose exact provider</option>{available.map((provider) => <option key={provider.name} value={provider.name}>{provider.name}</option>)}</select></td><td><input aria-label={`Exact provider identifier for ${instrumentLabel}`} placeholder="Exact listing / coin ID" value={draft?.providerSymbol || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'providerSymbol', event.target.value)} /></td><td><input id={quoteCurrencyID} aria-label={`Quote currency for ${instrumentLabel}`} aria-invalid={!quoteCurrencyValid} aria-describedby={`${quoteCurrencyID}-help`} value={quoteCurrency} disabled={controlsDisabled} onChange={(event) => editDraft(instrument.instrument_id, 'quoteCurrency', event.target.value)} /><span className="subline" id={`${quoteCurrencyID}-help`}>{quoteCurrencyValid ? 'Three-letter uppercase currency code.' : quoteCurrencyError(quoteCurrency)}</span></td><td><input aria-label={`Provenance for ${instrumentLabel}`} placeholder="Where you verified it" value={draft?.provenance || ''} disabled={controlsDisabled} onChange={(event) => manualUpdate(instrument.instrument_id, 'provenance', event.target.value)} /><label className="checkbox-label"><input type="checkbox" disabled={controlsDisabled} checked={draft?.confirmed || false} onChange={(event) => editDraft(instrument.instrument_id, 'confirmed', event.target.checked)} /> <span>I confirmed this exact identifier for {instrumentLabel}</span></label></td></tr>
        })}</tbody></table></div>
        <div className="market-actions"><button type="button" onClick={save} disabled={!canSave} aria-describedby="save-disabled-reason">Save all mappings</button>{saving && <span role="status" aria-live="polite">Saving mappings…</span>}</div>
        <p id="save-disabled-reason" className="muted">{controlsDisabled && saving ? 'Save is paused while the pending mapping request completes.' : controlsDisabled && reconciliationRequired ? 'Save is disabled until a GET reconciliation completes; no second PUT will be sent automatically.' : controlsDisabled && conflict === 'uncertain' ? 'Save is disabled until you acknowledge the reconciled uncertain result with Reload setup; no second PUT will be sent automatically.' : controlsDisabled && conflict === 'stale' ? 'Save is disabled until the changed setup is reconciled and a draft choice is made.' : saveDisabledReason || 'All active mappings are complete and confirmed.'}</p>
        <p className="muted">Save validates the whole batch before committing. Every row needs an exact editable identifier and explicit confirmation; unverified suggestions remain local until you confirm them. A stale setup is rejected rather than overwriting another revision.</p>
      </>}
      {conflict === 'stale' && adoptionChoice && <div className="market-actions" role="group" aria-label="Stale setup choices"><button type="button" onClick={() => { setAdoptionChoice(false); setConflict(null); conflictRef.current = null; setSaveError('Mappings remain local and unsaved. The current setup revision is active; review the rows before saving again.') }}>Keep editing with current setup</button><button type="button" onClick={() => { setAdoptionChoice(false); readSetup({ discover: false, adoptDrafts: true }) }}>Discard edits and reload</button></div>}
    </>}
    {discoveryMessage && <p className="notice" role="status" aria-live="polite">{discoveryMessage}</p>}
    {saveReceipt && <div className="notice success" role="status" aria-live="polite"><strong>Mappings saved</strong><span className="subline">{saveReceipt} Prices have not been refreshed by this setup save.</span></div>}
    {saveError && <p className="notice error" role="alert" aria-live="polite">{saveError}{conflict === 'stale' && <> {' '}<button type="button" onClick={reloadForConflict}>Reload setup</button></>}{conflict === 'uncertain' && <> {' '}<button type="button" onClick={() => readSetup({ discover: false, reconcile: true, acknowledge: true })}>Reload setup</button></>}</p>}
  </section>
}
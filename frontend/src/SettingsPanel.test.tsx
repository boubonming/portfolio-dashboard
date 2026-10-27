import userEvent from '@testing-library/user-event'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import SettingsPanel, { scheduleCronForPreset, schedulePresetFromCron, validScheduleTime } from './SettingsPanel'

const setup = () => ({
  portfolio_id: 'portfolio-a', etag: '"setup-1"', providers: [], instruments: [],
})
function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, headers: new Headers(), json: async () => body } as Response)
}
const scheduleEtag = (letter: string) => `"${letter.repeat(64)}"`
const environmentSchedule = (overrides: Record<string, unknown> = {}) => ({ portfolio_id: 'portfolio-a', enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '', revision: 0, updated_at: null, source: 'environment', etag: scheduleEtag('a'), effective: true, degraded: false, runtime_status: 'active', ...overrides })
const savedSchedule = (overrides: Record<string, unknown> = {}) => ({ ...environmentSchedule({ source: 'saved', revision: 1, updated_at: '2026-01-01T00:00:00.000000000Z', etag: scheduleEtag('b') }), ...overrides })
const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise })
  return { promise, resolve, reject }
}

function renderRoutedSchedule({
  schedule = environmentSchedule(),
  putResponse = json(savedSchedule()),
  portfolioID = 'portfolio-a',
  onUnauthorized = vi.fn(),
  onReload = vi.fn(),
  scheduleGet,
}: {
  schedule?: unknown
  putResponse?: Promise<Response>
  portfolioID?: string
  onUnauthorized?: () => void
  onReload?: () => void
  scheduleGet?: Promise<Response>
} = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith('/market-data/schedule')) return init?.method === 'PUT' ? putResponse : (scheduleGet || json(schedule))
    if (path.endsWith('/market-data')) return json({ ...setup(), portfolio_id: portfolioID })
    return json({})
  })
  vi.stubGlobal('fetch', fetchMock)
  const rendered = render(<SettingsPanel portfolioID={portfolioID} csrfToken="csrf-token" onUnauthorized={onUnauthorized} onReload={onReload} />)
  return { rendered, fetchMock, onUnauthorized, onReload }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('SettingsPanel', () => {
  it('converts Daily, Weekdays, and Custom schedule controls without accepting invalid local times', () => {
    expect(scheduleCronForPreset('daily', '05:07')).toBe('7 5 * * *')
    expect(scheduleCronForPreset('weekdays', '23:59')).toBe('59 23 * * 1-5')
    expect(schedulePresetFromCron('7 5 * * *')).toBe('daily')
    expect(schedulePresetFromCron('59 23 * * 1-5')).toBe('weekdays')
    expect(schedulePresetFromCron('0 5 1 * *')).toBe('custom')
    expect(validScheduleTime('00:00')).toBe(true)
    expect(validScheduleTime('23:59')).toBe(true)
    expect(validScheduleTime('24:00')).toBe(false)
    expect(validScheduleTime('5:07')).toBe(false)
  })
  it.each([
    ['enabled environment', environmentSchedule(), true, 'environment', 'active'],
    ['enabled saved', savedSchedule(), true, 'saved', 'active'],
    ['saved disabled', savedSchedule({ enabled: false, cron: '', effective: false, runtime_status: 'disabled' }), false, 'saved', 'disabled'],
    ['degraded environment', environmentSchedule({ degraded: true, effective: false, runtime_status: 'degraded' }), true, 'environment', 'degraded'],
  ])('renders the %s schedule state with visible source and runtime status', async (_label, schedule, enabled, source, runtimeStatus) => {
    renderRoutedSchedule({ schedule })
    await waitFor(() => expect(screen.getByLabelText('Schedule source')).toBeTruthy())
    expect(screen.getByLabelText('Schedule source').textContent).toContain(source)
    expect(screen.getByLabelText('Schedule runtime status').textContent).toContain(runtimeStatus)
    expect((screen.getByRole('checkbox', { name: /enable scheduled refreshes/i }) as HTMLInputElement).checked).toBe(enabled)
    if (runtimeStatus === 'degraded') {
      expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(true)
      expect(screen.getByRole('alert').textContent).toMatch(/retry or reconcile/i)
    }
  })

  it('drives Daily, Weekdays, and Custom cron bodies plus currency validation through user events', async () => {
    const user = userEvent.setup()
    const { fetchMock } = renderRoutedSchedule()
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Preset' })).toBeTruthy())
    const preset = screen.getByRole('combobox', { name: 'Preset' })
    const time = screen.getByLabelText('Schedule local time')
    expect((time as HTMLInputElement).value).toBe('05:00')

    const currency = screen.getByRole('textbox', { name: 'Reporting currency' })
    await user.type(currency, 'US')
    expect((currency as HTMLInputElement).value).toBe('US')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    expect(screen.getByRole('alert').textContent).toMatch(/blank or three uppercase letters/i)
    expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(0)
    await user.clear(currency)
    await user.type(currency, 'usd')
    expect((currency as HTMLInputElement).value).toBe('USD')

    await user.selectOptions(preset, 'daily')
    await user.clear(time)
    await user.type(time, '05:07')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1))
    expect(JSON.parse(String((fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT') as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '7 5 * * *', timezone: 'Asia/Singapore', reporting_currency: 'USD' })

    await user.selectOptions(preset, 'weekdays')
    await user.clear(time)
    await user.type(time, '23:59')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(2))
    expect(JSON.parse(String((fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')[1] as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '59 23 * * 1-5', timezone: 'Asia/Singapore', reporting_currency: 'USD' })

    await user.selectOptions(preset, 'custom')
    const cron = screen.getByRole('textbox', { name: 'Custom five-field cron' })
    expect((cron as HTMLInputElement).value).toBe('59 23 * * 1-5')
    await user.clear(cron)
    await user.type(cron, '0 4 * * 0')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(3))
    expect(JSON.parse(String((fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')[2] as [RequestInfo | URL, RequestInit])[1].body))).toEqual({ enabled: true, cron: '0 4 * * 0', timezone: 'Asia/Singapore', reporting_currency: 'USD' })
  })

  it('saves through the rendered button with exact request metadata and displays the returned revision and ETag', async () => {
    const user = userEvent.setup()
    const next = savedSchedule({ revision: 7, etag: scheduleEtag('c'), cron: '7 5 * * *' })
    const { fetchMock } = renderRoutedSchedule({ putResponse: json(next) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1))
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT') as unknown as [RequestInfo | URL, RequestInit]
    expect(JSON.parse(String(call[1].body))).toEqual({ enabled: true, cron: '0 5 * * *', timezone: 'Asia/Singapore', reporting_currency: '' })
    const headers = new Headers(call[1].headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')
    expect(headers.get('If-Match')).toBe(scheduleEtag('a'))
    expect(call[1].credentials).toBe('same-origin')
    expect(call[1].signal).toBeInstanceOf(AbortSignal)
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(`Schedule revision: 7 · ETag: ${scheduleEtag('c')}`))
    expect(screen.getByLabelText('Schedule source').textContent).toContain('saved')
  })

  it('uses the in-flight save guard for double activation and recovers after the PUT resolves', async () => {
    const user = userEvent.setup()
    const pending = deferred<Response>()
    const { fetchMock } = renderRoutedSchedule({ putResponse: pending.promise })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    const save = screen.getByRole('button', { name: 'Save schedule' })
    await user.click(save)
    await user.click(save)
    expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/schedule') && init?.method === 'PUT')).toHaveLength(1)
    expect((save as HTMLButtonElement).disabled).toBe(true)
    pending.resolve(await json(savedSchedule({ revision: 2, etag: scheduleEtag('d') })))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.getAllByRole('status').some((status) => status.textContent?.includes('Schedule saved.'))).toBe(true)
  })

  it('handles unauthorized, stale revision reload, and degraded PUT failures without leaving controls locked', async () => {
    const user = userEvent.setup()
    const onUnauthorized = vi.fn()
    const unauthorized = renderRoutedSchedule({ putResponse: json({ error: 'unauthorized' }, 401), onUnauthorized })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledOnce())
    expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false)
    unauthorized.rendered.unmount()

    let scheduleGets = 0
    const latest = savedSchedule({ revision: 8, etag: scheduleEtag('e') })
    const latestGet = deferred<Response>()
    const onReload = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/schedule')) return init?.method === 'PUT' ? json({ error: 'stale' }, 412) : (scheduleGets++ === 0 ? json(environmentSchedule()) : latestGet.promise)
      if (path.endsWith('/discover')) return json({ portfolio_id: 'portfolio-a', etag: '"setup-1"', results: [] })
      return json(setup())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SettingsPanel portfolioID="portfolio-a" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={onReload} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(onReload).toHaveBeenCalledOnce())
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/changed in another window/i))
    latestGet.resolve(await json(latest))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain('Schedule revision: 8'))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain('ETag: ' + scheduleEtag('e'))
    cleanup()

    const degradedFailure = renderRoutedSchedule({ putResponse: json({ error: 'runtime degraded' }, 503) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/runtime recovery is unresolved/i))
    expect((screen.getByRole('button', { name: 'Save schedule' }) as HTMLButtonElement).disabled).toBe(false)
    degradedFailure.rendered.unmount()
  })

  it.each([
    ['malformed', { portfolio_id: 'portfolio-a' }],
    ['unknown-field', { ...environmentSchedule(), unexpected: true }],
    ['contradictory', environmentSchedule({ enabled: true, effective: false, runtime_status: 'active' })],
  ])('fails closed for %s schedule success payloads', async (_label, payload) => {
    renderRoutedSchedule({ schedule: payload })
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/Invalid market refresh schedule response/i))
    expect(screen.queryByRole('button', { name: 'Save schedule' })).toBeNull()
    expect(screen.queryByRole('combobox', { name: 'Preset' })).toBeNull()
  })

  it('aborts every pending schedule request on scope change and unmount, ignoring stale completions', async () => {
    const user = userEvent.setup()
    const initialGet = deferred<Response>()
    const nextGet = deferred<Response>()
    const finalGet = deferred<Response>()
    const pendingPut = deferred<Response>()
    const scheduleGetSignals: AbortSignal[] = []
    let pendingPutSignal: AbortSignal | null | undefined
    let portfolio = 'portfolio-a'
    const oldSchedule = environmentSchedule()
    const newSchedule = environmentSchedule({ portfolio_id: 'portfolio-b', etag: scheduleEtag('f') })
    const finalSchedule = environmentSchedule({ portfolio_id: 'portfolio-c', etag: scheduleEtag('8') })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/schedule')) {
        if (init?.method === 'PUT') {
          pendingPutSignal = init.signal
          return pendingPut.promise
        }
        if (init?.signal) scheduleGetSignals.push(init.signal)
        if (portfolio === 'portfolio-a') return initialGet.promise
        if (portfolio === 'portfolio-b') return nextGet.promise
        return finalGet.promise
      }
      if (path.endsWith('/discover')) return json({ portfolio_id: portfolio, etag: '"setup-1"', results: [] })
      return json({ ...setup(), portfolio_id: portfolio })
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<SettingsPanel portfolioID="portfolio-a" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(1))
    portfolio = 'portfolio-b'
    rendered.rerender(<SettingsPanel portfolioID="portfolio-b" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(2))
    expect(scheduleGetSignals[0].aborted).toBe(true)
    nextGet.resolve(await json(newSchedule))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(newSchedule.etag))
    initialGet.resolve(await json(oldSchedule))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain(newSchedule.etag)

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(pendingPutSignal).toBeInstanceOf(AbortSignal))
    portfolio = 'portfolio-c'
    rendered.rerender(<SettingsPanel portfolioID="portfolio-c" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(pendingPutSignal?.aborted).toBe(true))
    await waitFor(() => expect(scheduleGetSignals).toHaveLength(3))
    finalGet.resolve(await json(finalSchedule))
    await waitFor(() => expect(screen.getByLabelText('Schedule revision').textContent).toContain(finalSchedule.etag))
    pendingPut.resolve(await json(savedSchedule({ portfolio_id: 'portfolio-a', revision: 99, etag: scheduleEtag('9') })))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByLabelText('Schedule revision').textContent).toContain(finalSchedule.etag)
    rendered.unmount()
    expect(scheduleGetSignals.every((signal) => signal.aborted)).toBe(true)
  })

  it('aborts a pending schedule GET on unmount and ignores its stale completion', async () => {
    const pendingGet = deferred<Response>()
    let getSignal: AbortSignal | undefined
    let staleResponseRead = false
    const staleSchedule = savedSchedule({ revision: 99, etag: scheduleEtag('9') })
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      getSignal = init?.signal as AbortSignal
      return pendingGet.promise
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<SettingsPanel portfolioID="portfolio-a" csrfToken="csrf-token" onUnauthorized={vi.fn()} onReload={vi.fn()} />)
    await waitFor(() => expect(getSignal).toBeInstanceOf(AbortSignal))
    expect(getSignal?.aborted).toBe(false)
    rendered.unmount()
    expect(getSignal?.aborted).toBe(true)
    pendingGet.resolve({ ok: true, status: 200, headers: new Headers(), json: async () => { staleResponseRead = true; return staleSchedule } } as Response)
    await waitFor(() => expect(staleResponseRead).toBe(true))
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('aborts a pending rendered schedule PUT on unmount and ignores its stale response', async () => {
    const user = userEvent.setup()
    const pendingPut = deferred<Response>()
    const onUnauthorized = vi.fn()
    const onReload = vi.fn()
    let pendingPutSignal: AbortSignal | null | undefined
    let staleResponseRead = false
    const staleSchedule = savedSchedule({ revision: 99, etag: scheduleEtag('9') })
    const stalePutResponse = { ok: true, status: 200, headers: new Headers(), json: async () => { staleResponseRead = true; return staleSchedule } } as Response
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/market-data/schedule')) {
        if (init?.method === 'PUT') {
          pendingPutSignal = init.signal
          return pendingPut.promise
        }
        return json(environmentSchedule())
      }
      if (path.endsWith('/market-data/discover')) return json({ portfolio_id: 'portfolio-a', etag: '"setup-1"', results: [] })
      return json(setup())
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<SettingsPanel portfolioID="portfolio-a" csrfToken="csrf-token" onUnauthorized={onUnauthorized} onReload={onReload} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy())
    const baselineRevision = screen.getByLabelText('Schedule revision').textContent
    const baselineSource = screen.getByLabelText('Schedule source').textContent
    expect(baselineRevision).toContain(`Schedule revision: 0 · ETag: ${scheduleEtag('a')}`)
    expect(baselineSource).toContain('environment')

    const observer = new MutationObserver(() => undefined)
    observer.observe(rendered.container, { attributes: true, characterData: true, childList: true, subtree: true })
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(pendingPutSignal).toBeInstanceOf(AbortSignal))
    const putCall = fetchMock.mock.calls.find(([input, request]) => String(input).endsWith('/market-data/schedule') && request?.method === 'PUT') as unknown as [RequestInfo | URL, RequestInit]
    expect(putCall[1].signal).toBeInstanceOf(AbortSignal)
    expect(putCall[1].signal).toBe(pendingPutSignal)
    const abortSignal = putCall[1].signal as AbortSignal
    expect(abortSignal.aborted).toBe(false)
    observer.takeRecords()

    rendered.unmount()
    observer.takeRecords()
    expect(abortSignal.aborted).toBe(true)

    pendingPut.resolve(stalePutResponse)
    await waitFor(() => expect(staleResponseRead).toBe(true))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(observer.takeRecords()).toHaveLength(0)
    expect(onReload).not.toHaveBeenCalled()
    expect(onUnauthorized).not.toHaveBeenCalled()
    expect(fetchMock.mock.calls.filter(([input, request]) => String(input).endsWith('/market-data/schedule') && request?.method !== 'PUT')).toHaveLength(1)
    observer.disconnect()
  })

  it('keeps the rendered schedule contract accessible and verifies production callbacks before activation', async () => {
    const onUnauthorized = vi.fn()
    const onReload = vi.fn()
    const { fetchMock } = renderRoutedSchedule({ onUnauthorized, onReload })
    expect(onUnauthorized).toBeDefined()
    expect(onReload).toBeDefined()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Market refresh schedule' })).toBeTruthy())
    expect(screen.getByRole('checkbox', { name: /enable scheduled refreshes/i }).getAttribute('aria-describedby')).toBeNull()
    expect(screen.getByRole('combobox', { name: 'Preset' })).toBeTruthy()
    expect(screen.getByLabelText('Schedule local time')).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'Schedule timezone' })).toBeTruthy()
    expect(screen.getByRole('textbox', { name: 'Reporting currency' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Save schedule' })).toBeTruthy()
    expect(screen.getByLabelText('Schedule source').getAttribute('aria-label')).toBe('Schedule source')
    expect(screen.getByLabelText('Schedule runtime status').getAttribute('aria-live')).toBe('polite')
    expect(screen.getByLabelText('Schedule revision')).toBeTruthy()
    expect(fetchMock).toHaveBeenCalled()
  })
})

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { StrictMode } from 'react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import HoldingMutations, { calculateRowMenuPosition, validateHoldingMutationReceipt, validatePortfolioETag } from './HoldingMutations'
import { hasMatchingDeclaration, matchingStyleRules, installCommittedStyles } from './cssTestUtils'

const holding = {
  lot_id: 'lot-a', symbol: 'AAA', name: 'AAA Fund', quantity: '2', currency: 'USD', unit_cost: '5', cost_basis: '10',
  freshness: { status: 'fresh' }, data_quality: [], missing_broker: false, missing_acquisition_date: false,
}
const receipt = {
  operation: 'create', result: 'applied', holding: {
    portfolio_id: 'portfolio-default', holding_id: 'lot-new', instrument_id: 'instrument-new', symbol: 'NEW',
    quantity: '2', unit_cost: '5', cost_basis: '10', currency: 'USD', account_id: null,
    acquisition_date: null, lifecycle: 'active', created_at: '2026-01-02T12:00:00.000000000Z', removed_at: null,
  }, portfolio_revision: 8, snapshot_state: 'no_snapshot', current_snapshot_id: null,
  actor: { category: 'browser_user', id: 'local-owner', permissions: ['portfolio:read'] }, audit_event_id: 'audit-1',
  activity_event_id: 'activity-1', idempotency_key: 'create-key-123456',
}

const baseProps = () => ({
  portfolioId: 'portfolio-default', overviewETag: '"portfolio-default-7"', csrfToken: 'csrf-token',
  holdings: [holding], active: true, scopeKey: 'portfolio-default:MYR:overview', onRefresh: vi.fn(async () => '"portfolio-default-8"'), onUnauthorized: vi.fn(),
})

function customModalRoot(ariaHidden: string, inert: boolean) {
  const root = document.createElement('div')
  root.id = 'root'
  root.setAttribute('aria-hidden', ariaHidden)
  root.inert = inert
  document.body.appendChild(root)
  return root
}

function expectModalRootRestored(root: HTMLElement, ariaHidden: string, inert: boolean) {
  expect(root.getAttribute('aria-hidden')).toBe(ariaHidden)
  expect(root.inert).toBe(inert)
}

function openArchive(lotId = 'lot-a') {
  const openButton = screen.queryByRole('button', { name: 'Archive a holding' })
  if (openButton) fireEvent.click(openButton)
  fireEvent.change(screen.getByLabelText('Holding to archive'), { target: { value: lotId } })
  fireEvent.click(screen.getByRole('button', { name: 'Continue to archive' }))
}

function retainedHandler<T extends (...args: any[]) => unknown>(element: HTMLElement, name: string) {
  const propsKey = Object.keys(element).find((key) => key.startsWith('__reactProps$'))
  const handler = propsKey ? (element as unknown as Record<string, Record<string, T | undefined>>)[propsKey]?.[name] : undefined
  expect(handler).toBeTypeOf('function')
  return handler as T
}

function expectNoLegacyMutationStyles(element: Element | null | undefined, state?: 'focus-visible' | 'disabled') {
  expect(element).toBeInstanceOf(Element)
  if (!element) throw new Error('Expected a DOM element for CSSOM evidence.')
  const legacyRules = matchingStyleRules(element, state).filter(({ selectorText }) => selectorText.includes('.holding-mutation-card'))
  expect(legacyRules).toEqual([])
  for (const property of ['background', 'border', 'padding', 'outline', 'accent-color', 'opacity']) {
    expect(hasMatchingDeclaration(element, property, property === 'opacity' ? '.65' : property === 'accent-color' ? '#52c7b8' : property === 'outline' ? '3px' : property === 'border' ? '1px' : property === 'padding' ? '.55rem' : '#172322', state)).toBe(false)
  }
}

function expectMUIClasses(element: Element | null | undefined, ...classes: string[]): asserts element is Element {
  expect(element).toBeInstanceOf(Element)
  if (!element) throw new Error('Expected a real MUI element.')
  for (const className of classes) expect(element.classList.contains(className), `${className} missing from ${element.className}`).toBe(true)
}

afterEach(() => cleanup())

describe('HoldingMutations', () => {
  it('binds card controls and both portaled dialogs while excluding MUI mutation primitives', async () => {
    const removeStyles = installCommittedStyles()
    const fetchMock = vi.fn(() => new Promise<Response>(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    try {
      render(<HoldingMutations {...baseProps()} />)
      const add = screen.getByRole('button', { name: 'Add holding' })
      expect(hasMatchingDeclaration(add, 'background', '#172322')).toBe(false)
      fireEvent.click(add)
      const symbol = screen.getByLabelText('Symbol')
      expect(hasMatchingDeclaration(symbol, 'background', '#172322')).toBe(false)
      symbol.focus()
      expect(hasMatchingDeclaration(symbol, 'outline', '3px', 'focus-visible')).toBe(false)
      const addCancel = screen.getByRole('button', { name: 'Cancel' })
      expect(hasMatchingDeclaration(addCancel, 'background', '#172322')).toBe(false)
      fireEvent.click(addCancel)
      const pickerToggle = screen.getByRole('button', { name: 'Archive a holding' })
      fireEvent.click(pickerToggle)
      const picker = screen.getByLabelText('Holding to archive')
      expect(hasMatchingDeclaration(picker, 'background', '#172322')).toBe(false)
      fireEvent.change(picker, { target: { value: 'lot-a' } })
      fireEvent.click(screen.getByRole('button', { name: 'Continue to archive' }))
      const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
      const cancelArchive = screen.getByRole('button', { name: 'Cancel archive' })
      expect(hasMatchingDeclaration(confirm, 'background', '#172322')).toBe(false)
      cancelArchive.focus()
      expect(hasMatchingDeclaration(cancelArchive, 'outline', '3px', 'focus-visible')).toBe(false)
      fireEvent.click(confirm)
      await waitFor(() => expect(screen.getByRole('button', { name: 'Archiving holding…' })).toBeTruthy())
      const busyConfirm = screen.getByRole('button', { name: 'Archiving holding…' })
      expect((busyConfirm as HTMLButtonElement).disabled).toBe(true)
      expect(hasMatchingDeclaration(busyConfirm, 'opacity', '.65', 'disabled')).toBe(false)
      expectNoLegacyMutationStyles(busyConfirm, 'disabled')
      cleanup()
      render(<HoldingMutations {...baseProps()}>{(renderActions) => <div>{renderActions(holding)}</div>}</HoldingMutations>)
      const muiAdd = screen.getByRole('button', { name: 'Add holding' })
      const rowIcon = screen.getByRole('button', { name: 'Actions for AAA lot lot-a' })
      expect(muiAdd.className).toContain('MuiButtonBase-root')
      expect(rowIcon.className).toContain('MuiButtonBase-root')
      expect(matchingStyleRules(muiAdd).every(({ selectorText }) => !selectorText.includes('.holding-mutation-card'))).toBe(true)
      expect(matchingStyleRules(rowIcon).every(({ selectorText }) => !selectorText.includes('.holding-mutation-card'))).toBe(true)
    } finally {
      removeStyles()
    }
  })

  it('uses real MUI dialog, menu, and form primitives for mutation surfaces', () => {
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
    render(<HoldingMutations {...baseProps()}>{renderRows}</HoldingMutations>)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    expect(document.querySelector('.MuiDialog-root')).toBeTruthy()
    expect(screen.getByRole('dialog').querySelector('.MuiDialogTitle-root')).toBeTruthy()
    expect(screen.getByLabelText('Symbol').className).toContain('MuiInputBase-input')
    expect(screen.getByLabelText('New inline instrument').className).toContain('PrivateSwitchBase-input')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'Actions for AAA lot lot-a' }))
    expect(document.querySelector('.MuiMenu-root')).toBeTruthy()
    expect(screen.getByRole('menu').className).toContain('MuiMenu-list')
    fireEvent.keyDown(document, { key: 'Escape' })
  })

  it('proves every migrated MUI primitive excludes the committed legacy mutation rules', () => {
    const removeStyles = installCommittedStyles()
    try {
      const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
      render(<HoldingMutations {...baseProps()} />)
      const add = screen.getByRole('button', { name: 'Add holding' })
      expectMUIClasses(add, 'MuiButton-root', 'MuiButtonBase-root')
      expectNoLegacyMutationStyles(add)
      const addIcon = add.querySelector('svg')
      expect(addIcon).toBeInstanceOf(SVGElement)
      expectMUIClasses(addIcon, 'MuiSvgIcon-root')
      expect(addIcon.getAttribute('aria-hidden')).toBe('true')
      expectNoLegacyMutationStyles(addIcon)

      fireEvent.click(add)
      const dialog = screen.getByRole('dialog')
      expectMUIClasses(dialog, 'MuiPaper-root', 'MuiDialog-paper')
      const dialogRoot = dialog.closest('.MuiDialog-root')
      expectMUIClasses(dialogRoot, 'MuiDialog-root')
      expect(dialog.getAttribute('aria-labelledby')).toBe('holding-form-title')
      const dialogPaper = dialog
      const dialogTitle = dialog.querySelector('.MuiDialogTitle-root')
      expect(dialogTitle).toBeInstanceOf(HTMLElement)
      expectMUIClasses(dialogTitle, 'MuiDialogTitle-root')
      expect(dialogTitle.id).toBe('holding-form-title')
      const dialogContent = dialog.querySelector('.MuiDialogContent-root')
      expect(dialogContent).toBeInstanceOf(HTMLElement)
      expectMUIClasses(dialogContent, 'MuiDialogContent-root')
      const dialogActions = dialog.querySelector('.MuiDialogActions-root')
      expect(dialogActions).toBeInstanceOf(HTMLElement)
      expectMUIClasses(dialogActions, 'MuiDialogActions-root')
      const textInput = screen.getByLabelText('Symbol')
      expectMUIClasses(textInput, 'MuiInputBase-input')
      expect(textInput.getAttribute('id')).toBe('symbol')
      const radioInput = screen.getByLabelText('New inline instrument')
      expectMUIClasses(radioInput, 'PrivateSwitchBase-input')
      expect(radioInput.getAttribute('type')).toBe('radio')
      const radioLabel = radioInput.closest('label')
      expect(radioLabel).toBeInstanceOf(HTMLElement)
      expectMUIClasses(radioLabel, 'MuiFormControlLabel-root')
      const radioControl = radioLabel.querySelector('.MuiRadio-root')
      expectMUIClasses(radioControl, 'MuiRadio-root', 'MuiButtonBase-root')
      expect(radioLabel.contains(radioInput)).toBe(true)
      expect(radioLabel.contains(radioControl)).toBe(true)
      const cancel = screen.getByRole('button', { name: 'Cancel' })
      expectMUIClasses(cancel, 'MuiButton-root', 'MuiButtonBase-root')
      expect(cancel.querySelector('svg')).toBeNull()
      for (const element of [dialogRoot, dialog, dialogPaper, dialogTitle, dialogContent, dialogActions, textInput, radioInput, radioControl, cancel]) expectNoLegacyMutationStyles(element)
      textInput.focus()
      expectNoLegacyMutationStyles(textInput, 'focus-visible')
      expectNoLegacyMutationStyles(radioInput)
      expectNoLegacyMutationStyles(radioControl)
      fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
      const addAlert = screen.getByRole('alert')
      expectMUIClasses(addAlert, 'MuiAlert-root')
      expectNoLegacyMutationStyles(addAlert)

      fireEvent.click(cancel)
      const archiveToggle = screen.getByRole('button', { name: 'Archive a holding' })
      expectMUIClasses(archiveToggle, 'MuiButton-root', 'MuiButtonBase-root')
      expectNoLegacyMutationStyles(archiveToggle)
      fireEvent.click(archiveToggle)
      const nativeSelect = screen.getByLabelText('Holding to archive')
      expectMUIClasses(nativeSelect, 'MuiNativeSelect-select', 'MuiInputBase-input')
      expect(nativeSelect.getAttribute('id')).toBe('archive-holding')
      expect(nativeSelect.closest('.MuiFormControl-root')).toBeInstanceOf(HTMLElement)
      expectNoLegacyMutationStyles(nativeSelect)
      fireEvent.change(nativeSelect, { target: { value: 'lot-a' } })
      const continueArchive = screen.getByRole('button', { name: 'Continue to archive' })
      expectMUIClasses(continueArchive, 'MuiButton-root', 'MuiButtonBase-root')
      expectNoLegacyMutationStyles(continueArchive)
      fireEvent.click(continueArchive)
      const archiveDialog = screen.getByRole('dialog')
      expectMUIClasses(archiveDialog, 'MuiPaper-root', 'MuiDialog-paper')
      const archiveDialogRoot = archiveDialog.closest('.MuiDialog-root')
      expectMUIClasses(archiveDialogRoot, 'MuiDialog-root')
      expect(archiveDialog.getAttribute('aria-labelledby')).toBe('archive-dialog-title')
      expect(archiveDialog.getAttribute('aria-describedby')).toBe('archive-dialog-description')
      const archivePaper = archiveDialog
      const archiveTitle = archiveDialog.querySelector('.MuiDialogTitle-root')
      expect(archiveTitle).toBeInstanceOf(HTMLElement)
      expectMUIClasses(archiveTitle, 'MuiDialogTitle-root')
      expect(archiveTitle.id).toBe('archive-dialog-title')
      const archiveContent = archiveDialog.querySelector('.MuiDialogContent-root')
      expect(archiveContent).toBeInstanceOf(HTMLElement)
      expectMUIClasses(archiveContent, 'MuiDialogContent-root')
      expect(archiveContent.querySelector('#archive-dialog-description')).toBeInstanceOf(HTMLElement)
      const archiveActions = archiveDialog.querySelector('.MuiDialogActions-root')
      expect(archiveActions).toBeInstanceOf(HTMLElement)
      expectMUIClasses(archiveActions, 'MuiDialogActions-root')
      const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
      const cancelArchive = screen.getByRole('button', { name: 'Cancel archive' })
      expectMUIClasses(confirm, 'MuiButton-root', 'MuiButtonBase-root')
      expectMUIClasses(cancelArchive, 'MuiButton-root', 'MuiButtonBase-root')
      for (const element of [archiveDialogRoot, archiveDialog, archivePaper, archiveTitle, archiveContent, archiveActions, confirm, cancelArchive]) expectNoLegacyMutationStyles(element)
      fireEvent.click(cancelArchive)

      cleanup()
      render(<HoldingMutations {...baseProps()}>{renderRows}</HoldingMutations>)
      const rowTrigger = screen.getByRole('button', { name: /Actions for AAA lot lot-a/ })
      expectMUIClasses(rowTrigger, 'MuiIconButton-root', 'MuiButtonBase-root')
      expect(rowTrigger.getAttribute('aria-haspopup')).toBe('menu')
      expect(rowTrigger.getAttribute('data-archive-trigger-lot-id')).toBe('lot-a')
      expectNoLegacyMutationStyles(rowTrigger)
      const rowIcon = rowTrigger.querySelector('svg')
      expect(rowIcon).toBeInstanceOf(SVGElement)
      expectMUIClasses(rowIcon, 'MuiSvgIcon-root')
      expect(rowIcon.getAttribute('aria-hidden')).toBe('true')
      expectNoLegacyMutationStyles(rowIcon)
      fireEvent.click(rowTrigger)
      const menuPaper = document.querySelector('.MuiPopover-paper')
      expect(menuPaper).toBeInstanceOf(HTMLElement)
      expectMUIClasses(menuPaper, 'MuiPopover-paper', 'MuiPaper-root', 'row-action-menu')
      const menu = screen.getByRole('menu')
      expectMUIClasses(menu, 'MuiMenu-list', 'row-action-menu-list')
      expect(menu.getAttribute('data-archive-menu-lot-id')).toBe('lot-a')
      const menuItem = screen.getByRole('menuitem', { name: 'Archive holding' })
      expectMUIClasses(menuItem, 'MuiMenuItem-root', 'MuiButtonBase-root')
      const menuIcon = menuItem.querySelector('svg')
      expect(menuIcon).toBeInstanceOf(SVGElement)
      expectMUIClasses(menuIcon, 'MuiSvgIcon-root')
      expect(menuIcon.getAttribute('aria-hidden')).toBe('true')
      for (const element of [menuPaper, menu, menuItem, menuIcon]) expectNoLegacyMutationStyles(element)
      fireEvent.keyDown(document, { key: 'Escape' })
    } finally {
      removeStyles()
    }
  })

  it('uses one trigger per rendered lot and portaled MUI backdrops for Add and archive dialogs', async () => {
    const root = customModalRoot('root-visible', false)
    const lots = [holding, { ...holding, lot_id: 'lot-b', symbol: 'BBB' }]
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{lots.map((lot) => <span key={lot.lot_id}>{renderActions(lot)}</span>)}</div>
    render(<HoldingMutations {...baseProps()} holdings={lots}>{renderRows}</HoldingMutations>, { container: root })
    expect(screen.getAllByRole('button', { name: 'Add holding' })).toHaveLength(1)
    expect(screen.getAllByRole('button', { name: /Actions for/ })).toHaveLength(lots.length)
    expect(document.querySelectorAll('[data-archive-trigger-lot-id]')).toHaveLength(lots.length)
    expect(document.querySelectorAll('[data-archive-trigger-lot-id][aria-hidden="true"]')).toHaveLength(0)

    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    const addBackdrop = document.querySelector('.MuiBackdrop-root')
    expect(addBackdrop).toBeTruthy()
    expect(root.contains(addBackdrop)).toBe(false)
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect(root.inert).toBe(true)
    expect(document.querySelectorAll('.MuiDialog-root')).toHaveLength(1)
    expect(document.querySelectorAll('.MuiMenu-root')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    fireEvent.click(screen.getByRole('button', { name: /Actions for AAA lot lot-a/ }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Archive holding' }))
    const archiveBackdrop = document.querySelector('.MuiBackdrop-root')
    expect(archiveBackdrop).toBeTruthy()
    expect(root.contains(archiveBackdrop)).toBe(false)
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect(root.inert).toBe(true)
    expect(document.querySelectorAll('.MuiDialog-root')).toHaveLength(1)
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(document.querySelectorAll('.MuiBackdrop-root')).toHaveLength(0)
    root.remove()
  })

  it('keeps archive management collapsed until a human-readable holding is selected', () => {
    render(<HoldingMutations {...baseProps()} holdings={[{ ...holding, symbol: 'NVDA', name: 'NVIDIA Corporation', quantity: '2.500', currency: 'USD', unit_cost: '100.25', cost_basis: '250.625', lot_id: 'lot-immutable-nvda-123456789' }]} />)
    expect(screen.getByRole('button', { name: 'Archive a holding' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Archive lot/i })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))

    expect(screen.getByLabelText('Holding to archive')).toBeTruthy()
    expect(screen.getByRole('option', { name: /NVDA.*NVIDIA Corporation.*2\.500 USD.*unit cost 100\.25 USD.*cost basis 250\.625 USD/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Continue to archive' })).toBeTruthy()
  })

  it('binds duplicate-symbol archive selections and confirmation copy to the exact lot', async () => {
    const props = { ...baseProps(), holdings: [
      { ...holding, symbol: 'SAME', name: 'First lot', lot_id: 'lot-first', quantity: '1', unit_cost: '5', cost_basis: '5' },
      { ...holding, symbol: 'SAME', name: 'Second lot', lot_id: 'lot-second', quantity: '2', unit_cost: '7', cost_basis: '14' },
    ] }
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-second', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))
    fireEvent.change(screen.getByLabelText('Holding to archive'), { target: { value: 'lot-second' } })
    fireEvent.click(screen.getByRole('button', { name: 'Continue to archive' }))

    expect(screen.getByRole('heading', { name: /Archive SAME holding/i })).toBeTruthy()
    expect(within(screen.getByRole('dialog')).getByText(/Second lot.*2 USD.*unit cost 7 USD.*cost basis 14 USD/i)).toBeTruthy()
    expect(screen.getByText(/Full lot ID: lot-second/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /Confirm archive holding/i })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(String(fetchMock.mock.calls[0][0])).toContain('/holdings/lot-second')
  })

  it('accepts only a strong canonical ETag bound to the requested portfolio', () => {
    expect(validatePortfolioETag('"portfolio-default-0"', 'portfolio-default')).toBe(0)
    expect(validatePortfolioETag('"portfolio-default-12"', 'portfolio-default')).toBe(12)
    expect(validatePortfolioETag('W/"portfolio-default-12"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"portfolio-default-012"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"other-12"', 'portfolio-default')).toBeNull()
    expect(validatePortfolioETag('"portfolio-default-1, portfolio-default-2"', 'portfolio-default')).toBeNull()
  })

  it('submits an inline holding with exact decimal strings and the overview witness', async () => {
    const props = baseProps()
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    const origin = screen.getByRole('button', { name: 'Add holding' })
    fireEvent.click(origin)
    fireEvent.change(screen.getByLabelText('Symbol'), { target: { value: 'NEW' } })
    fireEvent.change(screen.getByLabelText('Instrument currency'), { target: { value: 'USD' } })
    fireEvent.change(screen.getByLabelText('Quantity'), { target: { value: '2' } })
    fireEvent.change(screen.getByLabelText('Unit cost'), { target: { value: '5' } })
    fireEvent.change(screen.getByLabelText('Cost basis'), { target: { value: '10' } })
    fireEvent.change(screen.getByLabelText('Holding currency'), { target: { value: 'USD' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(document.activeElement).toBe(origin)
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    const payload = JSON.parse(String(init?.body)) as Record<string, unknown>
    expect(payload.quantity).toBe('2')
    expect(payload.unit_cost).toBe('5')
    expect(payload.cost_basis).toBe('10')
    expect(new Headers(init?.headers as HeadersInit).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('archives the exact lot only after explicit confirmation', async () => {
    const props = baseProps()
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', result: 'applied', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    openArchive('lot-a')
    expect(screen.getByRole('dialog').textContent).toMatch(/logical removal/i)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(fetchMock).not.toHaveBeenCalled()
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(String(fetchMock.mock.calls[0][0])).toContain('/holdings/lot-a')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('DELETE')
  })

  it('invalidates a retained archive handler synchronously after a 401 logout', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    const rendered = render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    const retainedConfirm = screen.getByRole('button', { name: 'Confirm archive holding' })
    const retainedConfirmHandler = retainedHandler<() => Promise<void>>(retainedConfirm, 'onClick')
    fireEvent.click(retainedConfirm)

    await waitFor(() => expect(props.onUnauthorized).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.hasAttribute('aria-hidden')).toBe(false)
    expect(root.inert).toBe(false)
    await retainedConfirmHandler()
    rendered.rerender(<HoldingMutations {...props} active={false} />)
    rendered.rerender(<HoldingMutations {...props} active />)
    await retainedConfirmHandler()
    await Promise.resolve()
    expect(fetchMock).toHaveBeenCalledOnce()
    root.remove()
  })

  it('rejects a receipt with unexpected fields', () => {
    expect(() => validateHoldingMutationReceipt({ ...receipt, unexpected: true }, { operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key, body: JSON.stringify({}), lotId: undefined })).toThrow()
  })

  it('accepts an exact receipt whose cost basis is the maximum 80-character product', () => {
    const quantity = '9999999999999999999999999999999999999999'
    const costBasis = '99999999999999999999999999999999999999980000000000000000000000000000000000000001'
    const maximumReceipt = { ...receipt, holding: { ...receipt.holding, quantity, unit_cost: quantity, cost_basis: costBasis } }
    expect(() => validateHoldingMutationReceipt(maximumReceipt, {
      operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key,
      body: JSON.stringify({ instrument: { instrument_id: 'instrument-new' }, quantity, unit_cost: quantity, cost_basis: costBasis, currency: 'USD' }),
    })).not.toThrow()
  })

  it('accepts a tiny fractional create receipt and preserves every declared scale', () => {
    const quantity = '0.000000000000000001'
    const costBasis = '0.000000000000000000000000000000000001'
    const fractionalReceipt = {
      ...receipt,
      holding: { ...receipt.holding, quantity, unit_cost: quantity, cost_basis: costBasis },
    }
    expect(() => validateHoldingMutationReceipt(fractionalReceipt, {
      operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key,
      body: JSON.stringify({ instrument: { instrument_id: 'instrument-new' }, quantity, unit_cost: quantity, cost_basis: costBasis, currency: 'USD' }),
    })).not.toThrow()
  })

  it('accepts a fractional archive receipt for a machine-created lot', () => {
    const fractionalReceipt = {
      ...receipt,
      operation: 'remove',
      holding: { ...receipt.holding, quantity: '0.5', unit_cost: '2.000', cost_basis: '1.000', holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' },
    }
    expect(() => validateHoldingMutationReceipt(fractionalReceipt, {
      operation: 'remove', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key, body: '', lotId: 'lot-a',
    })).not.toThrow()
  })

  it('accepts canonical positive integer and fractional quantity forms', () => {
    for (const [quantity, unitCost, costBasis] of [['0.5', '2', '1'], ['0.000000000000000001', '1', '0.000000000000000001'], ['1', '1', '1'], ['1.000', '1', '1.000']] as const) {
      expect(() => validateHoldingMutationReceipt({ ...receipt, holding: { ...receipt.holding, quantity, unit_cost: unitCost, cost_basis: costBasis } }, {
        operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key,
        body: JSON.stringify({ instrument: { instrument_id: 'instrument-new' }, quantity, unit_cost: unitCost, cost_basis: costBasis, currency: 'USD' }),
      }), quantity).not.toThrow()
    }
  })

  it('rejects zero, malformed, over-precise, and overlong quantities', () => {
    const invalidQuantities = [
      '0',
      '0.0',
      '0.000000000000000000',
      '00.5',
      '1e-1',
      '0.1234567890123456789',
      '1'.repeat(41),
    ]
    for (const quantity of invalidQuantities) {
      expect(() => validateHoldingMutationReceipt({ ...receipt, holding: { ...receipt.holding, quantity } }, {
        operation: 'create', portfolioId: 'portfolio-default', idempotencyKey: receipt.idempotency_key,
        body: JSON.stringify({ instrument: { instrument_id: 'instrument-new' }, quantity, unit_cost: '1', cost_basis: quantity, currency: 'USD' }),
      }), quantity).toThrow()
    }
  })

  it('submits a tiny fractional holding through the real form with exact strings', async () => {
    const props = baseProps()
    const quantity = '0.000000000000000001'
    const costBasis = '0.000000000000000000000000000000000001'
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({
      ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }),
      json: async () => ({ ...receipt, holding: { ...receipt.holding, quantity, unit_cost: quantity, cost_basis: costBasis }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }),
    } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.click(screen.getByLabelText('Existing instrument ID'))
    fireEvent.change(screen.getByLabelText('Instrument ID'), { target: { value: 'instrument-new' } })
    for (const [label, value] of [['Quantity', quantity], ['Unit cost', quantity], ['Cost basis', costBasis], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const payload = JSON.parse(String(fetchMock.mock.calls[0][1]?.body)) as Record<string, unknown>
    expect(payload.quantity).toBe(quantity)
    expect(payload.unit_cost).toBe(quantity)
    expect(payload.cost_basis).toBe(costBasis)
  })

  it('accepts successful archive of a fractional lot instead of reporting uncertainty', async () => {
    const fractionalHolding = { ...holding, quantity: '0.5', unit_cost: '2.000', cost_basis: '1.000' }
    const props = { ...baseProps(), holdings: [fractionalHolding] }
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({
      ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }),
      json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, quantity: '0.5', unit_cost: '2.000', cost_basis: '1.000', holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }),
    } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(screen.queryByText(/uncertainty/i)).toBeNull()
  })

  it('accepts the maximum exact product through the holding form', async () => {
    const props = baseProps()
    const quantity = '9999999999999999999999999999999999999999'
    const costBasis = '99999999999999999999999999999999999999980000000000000000000000000000000000000001'
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({
      ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }),
      json: async () => ({ ...receipt, holding: { ...receipt.holding, quantity, unit_cost: quantity, cost_basis: costBasis }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }),
    } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.click(screen.getByLabelText('Existing instrument ID'))
    fireEvent.change(screen.getByLabelText('Instrument ID'), { target: { value: 'instrument-new' } })
    for (const [label, value] of [['Quantity', quantity], ['Unit cost', quantity], ['Cost basis', costBasis], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const payload = JSON.parse(String(fetchMock.mock.calls[0][1]?.body)) as Record<string, unknown>
    expect(payload.cost_basis).toBe(costBasis)
  })

  it('reuses the exact uncertain create attempt after the overview ETag changes', async () => {
    const props = baseProps()
    let calls = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_input, init) => {
      calls += 1
      if (calls === 1) return Promise.reject(new TypeError('connection lost'))
      return Promise.resolve({ ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/uncertainty/i))
    const firstInit = fetchMock.mock.calls[0][1]
    const firstKey = new Headers(firstInit?.headers as HeadersInit).get('Idempotency-Key')
    rerender(<HoldingMutations {...props} overviewETag={'"portfolio-default-99"'} />)
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const retryInit = fetchMock.mock.calls[1][1]
    expect(new Headers(retryInit?.headers as HeadersInit).get('Idempotency-Key')).toBe(firstKey)
    expect(new Headers(retryInit?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    expect(retryInit?.body).toBe(firstInit?.body)
  })

  it('reuses the exact uncertain archive attempt after the overview ETag changes', async () => {
    const props = baseProps()
    let calls = 0
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>((_input, init) => {
      calls += 1
      if (calls === 1) return Promise.reject(new TypeError('connection lost'))
      return Promise.resolve({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(screen.getByText(/uncertainty/i)).toBeTruthy())
    const firstInit = fetchMock.mock.calls[0][1]
    const firstKey = new Headers(firstInit?.headers as HeadersInit).get('Idempotency-Key')
    rerender(<HoldingMutations {...props} overviewETag={'"portfolio-default-99"'} />)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    const retryInit = fetchMock.mock.calls[1][1]
    expect(new Headers(retryInit?.headers as HeadersInit).get('Idempotency-Key')).toBe(firstKey)
    expect(new Headers(retryInit?.headers as HeadersInit).get('If-Match')).toBe('"portfolio-default-7"')
    expect(retryInit?.body).toBe(firstInit?.body)
  })

  it('contains archive focus, supports Escape, and returns focus to the exact duplicate-symbol lot', () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a', symbol: 'SAME' }, { ...holding, lot_id: 'lot-b', symbol: 'SAME' }] }
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))
    fireEvent.change(screen.getByLabelText('Holding to archive'), { target: { value: 'lot-b' } })
    const lotB = screen.getByRole('button', { name: 'Continue to archive' })
    fireEvent.click(lotB)
    expect(screen.getByRole('dialog').contains(screen.getByRole('button', { name: 'Cancel archive' }))).toBe(true)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(lotB)
  })

  it('contains the complete application background and exposes the dialog description', () => {
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    const { unmount } = render(<HoldingMutations {...baseProps()} />, { container: root })
    openArchive('lot-a')
    const dialog = screen.getByRole('dialog')
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect((root as HTMLElement).inert).toBe(true)
    expect(dialog.getAttribute('aria-labelledby')).toBe('archive-dialog-title')
    expect(dialog.getAttribute('aria-describedby')).toBe('archive-dialog-description')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(root.hasAttribute('aria-hidden')).toBe(false)
    expect((root as HTMLElement).inert).toBe(false)
    unmount()
    root.remove()
  })

  it('wraps focus from the programmatically focused heading and unexpected outside focus', () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a' }, { ...holding, lot_id: 'lot-b' }] }
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-b')
    const dialog = screen.getByRole('dialog')
    const heading = screen.getByRole('heading', { name: /archive AAA holding/i })
    const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
    const cancel = screen.getByRole('button', { name: 'Cancel archive' })
    expect(document.activeElement).toBe(heading)
    fireEvent.keyDown(heading, { key: 'Tab' })
    expect(document.activeElement).toBe(confirm)
    fireEvent.keyDown(heading, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(cancel)
    const outside = document.createElement('button')
    root.appendChild(outside)
    outside.focus()
    fireEvent.keyDown(outside, { key: 'Tab' })
    expect(dialog.contains(document.activeElement)).toBe(true)
    root.remove()
  })

  it('keeps archive failures visible and focused inside the active dialog', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async () => ({ ok: false, status: 415, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/unsupported request format/i))
    const dialog = screen.getByRole('dialog')
    const alert = screen.getByRole('alert')
    expect(dialog.contains(alert)).toBe(true)
    expect(document.activeElement).toBe(alert)
    expect((screen.getByRole('button', { name: 'Confirm archive holding' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('wraps forward and reverse Tab from a focused archive alert', async () => {
    const props = { ...baseProps(), holdings: [{ ...holding, lot_id: 'lot-a' }, { ...holding, lot_id: 'lot-b' }] }
    const fetchMock = vi.fn(async () => ({ ok: false, status: 415, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy())
    const alert = screen.getByRole('alert')
    const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
    const cancel = screen.getByRole('button', { name: 'Cancel archive' })
    expect(document.activeElement).toBe(alert)
    fireEvent.keyDown(alert, { key: 'Tab' })
    expect(document.activeElement).toBe(confirm)
    fireEvent.keyDown(alert, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(cancel)
  })

  it('tears down an archive modal before a changed portfolio and scope can reuse it', () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'false')
    root.inert = true
    document.body.appendChild(root)
    const props = baseProps()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    expect(screen.getByRole('dialog')).toBeTruthy()

    rerender(<HoldingMutations {...props} portfolioId="portfolio-next" overviewETag={'"portfolio-next-7"'} scopeKey="portfolio-next:SGD:overview" />)

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('false')
    expect(root.inert).toBe(true)
    expect(fetchMock).not.toHaveBeenCalled()
    root.remove()
  })

  it('clears a selected archive target when the active scope changes', () => {
    const props = baseProps()
    const { rerender } = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))
    fireEvent.change(screen.getByLabelText('Holding to archive'), { target: { value: 'lot-a' } })
    expect((screen.getByLabelText('Holding to archive') as HTMLSelectElement).value).toBe('lot-a')

    rerender(<HoldingMutations {...props} scopeKey="portfolio-default:USD:holdings" />)

    expect(screen.queryByLabelText('Holding to archive')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))
    expect((screen.getByLabelText('Holding to archive') as HTMLSelectElement).value).toBe('')
  })

  it('aborts an in-flight archive and restores the background on scope replacement', async () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'previous')
    root.inert = false
    document.body.appendChild(root)
    const props = baseProps()
    let resolveFetch: (response: Response) => void = () => undefined
    const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) => new Promise<Response>((resolve) => { resolveFetch = resolve }))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    const request = fetchMock.mock.calls[0][1]

    rerender(<HoldingMutations {...props} scopeKey="portfolio-default:SGD:overview" />)

    expect((request?.signal as AbortSignal).aborted).toBe(true)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(false)
    resolveFetch({ ok: true, status: 200, headers: new Headers(), json: async () => ({}) } as Response)
    await Promise.resolve()
    expect(props.onRefresh).not.toHaveBeenCalled()
    root.remove()
  })

  it('restores modal containment for active logout, unmount, and StrictMode effect cycles', () => {
    const root = document.createElement('div')
    root.id = 'root'
    root.setAttribute('aria-hidden', 'previous')
    root.inert = true
    document.body.appendChild(root)
    const props = baseProps()
    const { rerender, unmount } = render(<StrictMode><HoldingMutations {...props} /></StrictMode>, { container: root })
    openArchive('lot-a')
    expect(screen.getByRole('dialog')).toBeTruthy()
    rerender(<StrictMode><HoldingMutations {...props} active={false} /></StrictMode>)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(true)
    rerender(<StrictMode><HoldingMutations {...props} /></StrictMode>)
    openArchive('lot-a')
    expect(screen.getByRole('dialog')).toBeTruthy()
    unmount()
    expect(root.getAttribute('aria-hidden')).toBe('previous')
    expect(root.inert).toBe(true)
    root.remove()
  })

  it('does not restore focus to a detached trigger after a successful single-lot archive', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Archive a holding' }))
    const origin = screen.getByRole('button', { name: 'Continue to archive' })
    fireEvent.change(screen.getByLabelText('Holding to archive'), { target: { value: 'lot-a' } })
    fireEvent.click(origin)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    rerender(<HoldingMutations {...props} holdings={[]} overviewETag={'"portfolio-default-8"'} />)
    await waitFor(() => expect(document.activeElement).not.toBe(origin))
    expect((document.activeElement as HTMLElement).isConnected).toBe(true)
    expect(document.activeElement).toBe(screen.getByRole('status'))
  })

  it('focuses the first invalid field and announces the form error', () => {
    const props = baseProps()
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    expect(screen.getByRole('alert').textContent).toMatch(/fix the highlighted/i)
    expect(document.activeElement).toBe(screen.getByLabelText('Symbol'))
    expect(screen.getByLabelText('Symbol').getAttribute('aria-describedby')).toContain('symbol-helper-text')
  })

  it('exercises the production row-menu path for exact-lot archive, outside close, and focus restoration', async () => {
    const lots = [
      { ...holding, symbol: 'SAME', name: 'First lot', lot_id: 'lot-first', quantity: '1', unit_cost: '5', cost_basis: '5' },
      { ...holding, symbol: 'SAME', name: 'Second lot', lot_id: 'lot-second', quantity: '2', unit_cost: '7', cost_basis: '14' },
    ]
    const props = { ...baseProps(), holdings: lots }
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-second', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{lots.map((lot) => <span key={lot.lot_id}>{renderActions(lot)}</span>)}</div>
    const rendered = render(<HoldingMutations {...props}>{renderRows}</HoldingMutations>)
    const triggers = screen.getAllByRole('button', { name: /Actions for SAME lot/ })
    fireEvent.click(triggers[1])
    const menu = screen.getByRole('menu')
    const paper = document.querySelector('.MuiPopover-paper') as HTMLElement
    expect(menu.className).toContain('row-action-menu-portal')
    expect(menu.getAttribute('data-archive-menu-lot-id')).toBe('lot-second')
    expect(paper.className).toContain('row-action-menu')
    const retainedMenuItem = screen.getByRole('menuitem', { name: 'Archive holding' })
    rendered.rerender(<HoldingMutations {...props} scopeKey="portfolio-default:USD:holdings">{renderRows}</HoldingMutations>)
    expect(screen.queryByRole('menu')).toBeNull()
    fireEvent.click(retainedMenuItem)
    expect(fetchMock).not.toHaveBeenCalled()
    rendered.rerender(<HoldingMutations {...props}>{renderRows}</HoldingMutations>)
    const currentTriggers = screen.getAllByRole('button', { name: /Actions for SAME lot/ })
    fireEvent.pointerDown(document.body)
    expect(screen.queryByRole('menu')).toBeNull()

    fireEvent.click(currentTriggers[1])
    fireEvent.click(screen.getByRole('menuitem', { name: 'Archive holding' }))
    expect(screen.getByRole('heading', { name: /Archive SAME holding/i })).toBeTruthy()
    expect(screen.getByText(/Second lot.*2 USD.*unit cost 7 USD.*cost basis 14 USD/i)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel archive' }))
    expect(document.activeElement).toBe(triggers[1])

    fireEvent.click(currentTriggers[1])
    fireEvent.click(screen.getByRole('menuitem', { name: 'Archive holding' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    expect(String(fetchMock.mock.calls[0][0])).toContain('/holdings/lot-second')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('DELETE')
    rendered.rerender(<HoldingMutations {...props} holdings={[lots[0]]}>{(renderActions) => <div>{[lots[0]].map((lot) => renderActions(lot))}</div>}</HoldingMutations>)
    await waitFor(() => expect(document.activeElement).toBe(rendered.container.querySelector('[role="status"]')))
  })

  it('keeps create failures in the active Add dialog with draft and retry state intact', async () => {
    const props = baseProps()
    const fetchMock = vi.fn(async () => ({ ok: false, status: 500, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    render(<HoldingMutations {...props} />, { container: root })
    const origin = screen.getByRole('button', { name: 'Add holding' })
    fireEvent.click(origin)
    fireEvent.change(screen.getByLabelText('Symbol'), { target: { value: 'RETRY' } })
    fireEvent.change(screen.getByLabelText('Instrument currency'), { target: { value: 'USD' } })
    fireEvent.change(screen.getByLabelText('Quantity'), { target: { value: '2' } })
    fireEvent.change(screen.getByLabelText('Unit cost'), { target: { value: '5' } })
    fireEvent.change(screen.getByLabelText('Cost basis'), { target: { value: '10' } })
    fireEvent.change(screen.getByLabelText('Holding currency'), { target: { value: 'USD' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/server could not apply/i))
    const dialog = screen.getByRole('dialog')
    expect(dialog.contains(screen.getByRole('alert'))).toBe(true)
    expect(document.activeElement).toBe(screen.getByRole('alert'))
    expect((screen.getByLabelText('Symbol') as HTMLInputElement).value).toBe('RETRY')
    expect(root.getAttribute('aria-hidden')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(document.activeElement).toBe(origin)
    expect(root.hasAttribute('aria-hidden')).toBe(false)
    root.remove()
  })

  it('rejects retained production row handlers after same-scope session replacement', () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
    const rendered = render(<HoldingMutations {...props}>{renderRows}</HoldingMutations>)
    const trigger = screen.getByRole('button', { name: /Actions for AAA lot lot-a/ })
    const retainedTrigger = retainedHandler<() => unknown>(trigger, 'onClick')
    fireEvent.click(trigger)
    const menuItem = screen.getByRole('menuitem', { name: 'Archive holding' })
    const retainedMenuItem = retainedHandler<(event: { currentTarget: HTMLElement }) => unknown>(menuItem, 'onClick')
    rendered.rerender(<HoldingMutations {...props} sessionKey="s2">{renderRows}</HoldingMutations>)
    retainedTrigger()
    retainedMenuItem({ currentTarget: menuItem })
    expect(screen.queryByRole('menu')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Confirm archive holding' })).toBeNull()
  })

  it('keeps retained archive confirmation powerless after session replacement', () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
    const rendered = render(<HoldingMutations {...props}>{renderRows}</HoldingMutations>)
    fireEvent.click(screen.getByRole('button', { name: /Actions for AAA lot lot-a/ }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Archive holding' }))
    const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
    const retainedConfirm = retainedHandler<() => unknown>(confirm, 'onClick')
    rendered.rerender(<HoldingMutations {...props} sessionKey="s2">{renderRows}</HoldingMutations>)
    retainedConfirm()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('keeps retained row handlers powerless immediately after a 401 teardown', async () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
    render(<HoldingMutations {...props}>{renderRows}</HoldingMutations>)
    const trigger = screen.getByRole('button', { name: /Actions for AAA lot lot-a/ })
    const retainedTrigger = retainedHandler<() => unknown>(trigger, 'onClick')
    fireEvent.click(trigger)
    const menuItem = screen.getByRole('menuitem', { name: 'Archive holding' })
    const retainedMenuItem = retainedHandler<(event: { currentTarget: HTMLElement }) => unknown>(menuItem, 'onClick')
    fireEvent.click(menuItem)
    const confirm = screen.getByRole('button', { name: 'Confirm archive holding' })
    const retainedConfirm = retainedHandler<() => unknown>(confirm, 'onClick')
    fireEvent.click(confirm)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    retainedTrigger()
    retainedMenuItem({ currentTarget: menuItem })
    retainedConfirm()
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(screen.queryByRole('menu')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('rejects a retained Add submit synchronously after session and scope replacement', () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const rendered = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    const form = screen.getByRole('dialog').querySelector('form') as HTMLFormElement
    const retainedSubmit = retainedHandler<(event: { preventDefault: () => void }) => unknown>(form, 'onSubmit')
    rendered.rerender(<HoldingMutations {...props} sessionKey="s2" scopeKey="portfolio-default:USD:holdings" />)
    retainedSubmit({ preventDefault: vi.fn() })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(props.onRefresh).not.toHaveBeenCalled()
  })

  it('rejects every retained Add control after session and scope replacement', () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    fireEvent.change(screen.getByLabelText('Symbol'), { target: { value: 'DRAFT' } })
    const retainedInput = retainedHandler<(event: { target: { value: string } }) => unknown>(screen.getByLabelText('Symbol'), 'onChange')
    const retainedRadio = retainedHandler<(event: any) => unknown>(screen.getByLabelText('Existing instrument ID'), 'onChange')
    const retainedCancel = retainedHandler<() => unknown>(screen.getByRole('button', { name: 'Cancel' }), 'onClick')
    const retainedSubmit = retainedHandler<(event: { preventDefault: () => void }) => unknown>(screen.getByRole('dialog').querySelector('form') as HTMLFormElement, 'onSubmit')
    const invokeRetainedControls = () => {
      retainedInput({ target: { value: 'STALE' } })
      retainedRadio({ target: { checked: true }, nativeEvent: {} } as any)
      retainedCancel()
      retainedSubmit({ preventDefault: vi.fn() })
    }

    rendered.rerender(<HoldingMutations {...props} sessionKey="s2" />)
    invokeRetainedControls()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByDisplayValue('STALE')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(props.onRefresh).not.toHaveBeenCalled()

    rendered.rerender(<HoldingMutations {...props} sessionKey="s2" scopeKey="portfolio-default:USD:holdings" />)
    invokeRetainedControls()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByDisplayValue('STALE')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(props.onRefresh).not.toHaveBeenCalled()
  })

  it('rejects a retained Add submit after a 401 before any second request', async () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    const form = screen.getByRole('dialog').querySelector('form') as HTMLFormElement
    const retainedSubmit = retainedHandler<(event: { preventDefault: () => void }) => unknown>(form, 'onSubmit')
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    retainedSubmit({ preventDefault: vi.fn() })
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('rejects every retained Add control after a 401 without restoring stale UI', async () => {
    const props = { ...baseProps(), sessionKey: 's1' }
    const root = customModalRoot('add-401-sentinel', true)
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    const retainedInput = retainedHandler<(event: { target: { value: string } }) => unknown>(screen.getByLabelText('Symbol'), 'onChange')
    const retainedRadio = retainedHandler<(event: any) => unknown>(screen.getByLabelText('Existing instrument ID'), 'onChange')
    const retainedCancel = retainedHandler<() => unknown>(screen.getByRole('button', { name: 'Cancel' }), 'onClick')
    const retainedSubmit = retainedHandler<(event: { preventDefault: () => void }) => unknown>(screen.getByRole('dialog').querySelector('form') as HTMLFormElement, 'onSubmit')
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onUnauthorized).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'add-401-sentinel', true)
    retainedInput({ target: { value: 'STALE' } })
    retainedRadio({ target: { checked: true }, nativeEvent: {} } as any)
    retainedCancel()
    retainedSubmit({ preventDefault: vi.fn() })
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(props.onRefresh).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByDisplayValue('STALE')).toBeNull()
    root.remove()
  })

  it('restores custom modal root state after Add success', async () => {
    const props = baseProps()
    const root = customModalRoot('add-success-sentinel', false)
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => ({ ok: true, status: 201, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'add-success-sentinel', false)
    root.remove()
  })

  it('restores custom modal root state after Add Escape', () => {
    const root = customModalRoot('add-escape-sentinel', true)
    render(<HoldingMutations {...baseProps()} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect(root.inert).toBe(true)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'add-escape-sentinel', true)
    root.remove()
  })

  it('restores custom modal root state after Add 401', async () => {
    const props = baseProps()
    const root = customModalRoot('add-401-modal-sentinel', false)
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(props.onUnauthorized).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'add-401-modal-sentinel', false)
    root.remove()
  })

  it('restores custom modal root state after Add session replacement', () => {
    const props = { ...baseProps(), sessionKey: 'add-session-1' }
    const root = customModalRoot('add-session-sentinel', true)
    const rendered = render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    rendered.rerender(<HoldingMutations {...props} sessionKey="add-session-2" />)
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'add-session-sentinel', true)
    root.remove()
  })

  it('restores custom modal root state after archive success', async () => {
    const props = baseProps()
    const root = customModalRoot('archive-success-sentinel', true)
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (_input, init) => ({ ok: true, status: 200, headers: new Headers({ ETag: '"portfolio-default-8"' }), json: async () => ({ ...receipt, operation: 'remove', holding: { ...receipt.holding, holding_id: 'lot-a', lifecycle: 'removed', removed_at: '2026-01-02T12:00:00.000000000Z' }, idempotency_key: new Headers(init?.headers as HeadersInit).get('Idempotency-Key') }) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onRefresh).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'archive-success-sentinel', true)
    root.remove()
  })

  it('restores custom modal root state after archive Escape', () => {
    const root = customModalRoot('archive-escape-sentinel', false)
    render(<HoldingMutations {...baseProps()} />, { container: root })
    openArchive('lot-a')
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'archive-escape-sentinel', false)
    root.remove()
  })

  it('restores custom modal root state after archive 401', async () => {
    const props = baseProps()
    const root = customModalRoot('archive-401-sentinel', true)
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, headers: new Headers(), json: async () => ({}) } as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive holding' }))
    await waitFor(() => expect(props.onUnauthorized).toHaveBeenCalledOnce())
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'archive-401-sentinel', true)
    root.remove()
  })

  it('restores custom modal root state after archive session replacement', () => {
    const props = { ...baseProps(), sessionKey: 'archive-session-1' }
    const root = customModalRoot('archive-session-sentinel', false)
    const rendered = render(<HoldingMutations {...props} />, { container: root })
    openArchive('lot-a')
    rendered.rerender(<HoldingMutations {...props} sessionKey="archive-session-2" />)
    expect(screen.queryByRole('dialog')).toBeNull()
    expectModalRootRestored(root, 'archive-session-sentinel', false)
    root.remove()
  })

  it('activates the production row trigger from keyboard and returns focus on Escape', async () => {
    const renderRows = (renderActions: (lot: typeof holding) => ReactNode) => <div>{renderActions(holding)}</div>
    render(<HoldingMutations {...baseProps()}>{renderRows}</HoldingMutations>)
    const trigger = screen.getByRole('button', { name: /Actions for AAA lot lot-a/ })
    fireEvent.keyDown(trigger, { key: 'Enter' })
    fireEvent.keyUp(trigger, { key: 'Enter' })
    expect(screen.getByRole('menuitem', { name: 'Archive holding' })).toBe(document.activeElement)
    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(document.activeElement).toBe(trigger))
    fireEvent.keyDown(trigger, { key: ' ' })
    fireEvent.keyUp(trigger, { key: ' ' })
    expect(screen.getByRole('menu')).toBeTruthy()
  })

  it('contains Add focus from its heading, live error, and outside node', async () => {
    const props = baseProps()
    const root = document.createElement('div')
    root.id = 'root'
    document.body.appendChild(root)
    const fetchMock = vi.fn(async () => ({ ok: false, status: 500, headers: new Headers(), clone: () => ({ json: async () => ({}) }) } as unknown as Response))
    vi.stubGlobal('fetch', fetchMock)
    render(<HoldingMutations {...props} />, { container: root })
    fireEvent.click(screen.getByRole('button', { name: 'Add holding' }))
    const heading = screen.getByRole('heading', { name: 'Add one holding' })
    screen.getByLabelText('Existing instrument ID')
    fireEvent.keyDown(heading, { key: 'Tab' })
    expect(document.activeElement).toBe(screen.getByLabelText('New inline instrument'))
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    outside.focus()
    expect(document.activeElement).toBe(heading)
    for (const [label, value] of [['Symbol', 'NEW'], ['Instrument currency', 'USD'], ['Quantity', '2'], ['Unit cost', '5'], ['Cost basis', '10'], ['Holding currency', 'USD']] as const) fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Create holding' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toMatch(/server could not apply/i))
    const alert = screen.getByRole('alert')
    expect(document.activeElement).toBe(alert)
    fireEvent.keyDown(alert, { key: 'Tab' })
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
    outside.remove()
    root.remove()
  })

  it('places a portaled row menu inside a narrow viewport and recomputes on descendant scroll, window scroll, and resize', () => {
    expect(calculateRowMenuPosition({ top: 190, bottom: 214, left: 290, right: 314 }, { width: 100, height: 70 }, 320, 240)).toEqual({ top: 116, left: 212 })
    const props = baseProps()
    const originalWidth = window.innerWidth
    const originalHeight = window.innerHeight
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 320 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 240 })
    let triggerRect = { top: 190, bottom: 214, left: 290, right: 314 }
    let menuRect = { width: 100, height: 70 }
    const originalRect = HTMLElement.prototype.getBoundingClientRect
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.classList.contains('row-action-trigger')) return { ...triggerRect, width: 24, height: 24 } as DOMRect
      if (this.classList.contains('row-action-menu')) return { top: 0, left: 0, ...menuRect, right: menuRect.width, bottom: menuRect.height } as DOMRect
      return originalRect.call(this)
    })
    render(<HoldingMutations {...props}>{(renderActions) => <div className="table-scroll">{renderActions(holding)}</div>}</HoldingMutations>)
    fireEvent.click(screen.getByRole('button', { name: /Actions for AAA lot lot-a/ }))
    const menu = screen.getByRole('menu')
    const paper = document.querySelector('.MuiPopover-paper') as HTMLElement
    expect(menu.getAttribute('data-archive-menu-lot-id')).toBe('lot-a')
    expect(screen.getByRole('menuitem', { name: 'Archive holding' })).toBe(document.activeElement)
    expect(paper.style.left).toBe('212px')
    expect(paper.style.top).toBe('116px')
    const scrollContainer = document.querySelector('.table-scroll') as HTMLElement
    triggerRect = { top: 100, bottom: 124, left: 250, right: 274 }
    menuRect = { width: 120, height: 80 }
    fireEvent.scroll(scrollContainer)
    expect(paper.style.left).toBe('192px')
    expect(paper.style.top).toBe('128px')
    expect(Number.parseFloat(paper.style.left) + menuRect.width).toBeLessThanOrEqual(320 - 8)
    expect(Number.parseFloat(paper.style.top) + menuRect.height).toBeLessThanOrEqual(240 - 8)

    triggerRect = { top: 20, bottom: 44, left: 10, right: 34 }
    fireEvent.scroll(window)
    expect(paper.style.top).toBe('48px')
    expect(paper.style.left).toBe('10px')

    triggerRect = { top: 210, bottom: 234, left: 300, right: 324 }
    menuRect = { width: 100, height: 100 }
    fireEvent.resize(window)
    expect(paper.style.left).toBe('212px')
    expect(paper.style.top).toBe('106px')
    expect(Number.parseFloat(paper.style.left)).toBeGreaterThanOrEqual(8)
    expect(Number.parseFloat(paper.style.top)).toBeGreaterThanOrEqual(8)
    expect(Number.parseFloat(paper.style.left) + menuRect.width).toBeLessThanOrEqual(320 - 8)
    expect(Number.parseFloat(paper.style.top) + menuRect.height).toBeLessThanOrEqual(240 - 8)
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: originalHeight })
  })
})

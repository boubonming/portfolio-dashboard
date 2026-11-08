#!/usr/bin/env node
// Run against a loopback production bundle with Chromium CDP enabled:
// S3_APP_URL=http://127.0.0.1:4173/ S3_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s3-production-layout-check.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const appURL = process.env.S3_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S3_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const fixturePath = new URL('./s3-synthetic-fixture.js', import.meta.url)
const fixture = await readFile(fixturePath, 'utf8')

async function targetWebSocketURL() {
  if (cdpEndpoint.startsWith('ws://') || cdpEndpoint.startsWith('wss://')) return cdpEndpoint
  const response = await fetch(`${cdpEndpoint.replace(/\/$/, '')}/json/list`)
  assert.equal(response.ok, true, `Unable to read CDP targets from ${cdpEndpoint}`)
  const targets = await response.json()
  const page = targets.find((target) => target.type === 'page')
  assert.ok(page?.webSocketDebuggerUrl, 'No page target is available in the configured CDP endpoint')
  return page.webSocketDebuggerUrl
}

const socket = new WebSocket(await targetWebSocketURL())
let nextID = 0
const pending = new Map()
socket.addEventListener('message', (event) => {
  const message = JSON.parse(event.data)
  if (message.id && pending.has(message.id)) {
    const { resolve, reject } = pending.get(message.id)
    pending.delete(message.id)
    if (message.error) reject(new Error(`${message.error.code}: ${message.error.message}`))
    else resolve(message.result)
  }
})
await new Promise((resolve, reject) => {
  socket.addEventListener('open', resolve, { once: true })
  socket.addEventListener('error', reject, { once: true })
})
function command(method, params = {}) {
  const id = ++nextID
  socket.send(JSON.stringify({ id, method, params }))
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }))
}
async function evaluate(expression) {
  const result = await command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || 'Browser evaluation failed')
  return result.result?.value
}
async function waitFor(expression, timeout = 5000) {
  const started = Date.now()
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return
    await new Promise((resolve) => setTimeout(resolve, 75))
  }
  throw new Error(`Timed out waiting for ${expression}`)
}
async function settle() {
  await new Promise((resolve) => setTimeout(resolve, 75))
}
async function activateTab(label) {
  await evaluate(`(() => { const tab = [...document.querySelectorAll('[role="tab"]')].find((node) => node.textContent?.includes(${JSON.stringify(label)})); tab?.click(); return Boolean(tab); })()`)
  await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.textContent?.includes(${JSON.stringify(label)}) && node.getAttribute('aria-selected') === 'true'))`)
}
async function keyPress(key, { shift = false, wait = true } = {}) {
  const params = { key, code: key, modifiers: shift ? 8 : 0 }
  const virtualKeyCodes = { Enter: 13, Tab: 9, ArrowRight: 39 }
  if (virtualKeyCodes[key]) Object.assign(params, { windowsVirtualKeyCode: virtualKeyCodes[key], nativeVirtualKeyCode: virtualKeyCodes[key] })
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  if (wait) await settle()
}
async function horizontalWheel(rect, deltaX) {
  await command('Input.dispatchMouseEvent', {
    type: 'mouseWheel',
    x: rect.left + rect.width / 2,
    y: Math.max(2, Math.min(rect.top + 20, 780)),
    deltaX,
    deltaY: 0,
  })
  await settle()
}
function assertRootWithinViewport(value, label) {
  assert.ok(value.rootScrollWidth <= value.innerWidth + 1, `${label} root overflow: ${JSON.stringify(value)}`)
  assert.ok(value.bodyScrollWidth <= value.innerWidth + 1, `${label} body overflow: ${JSON.stringify(value)}`)
}

await command('Page.enable')
await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
await command('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false })
await command('Page.navigate', { url: appURL })
await waitFor('Boolean(document.querySelector("[role=tab]"))')
await waitFor('Boolean([...document.querySelectorAll("button")].some((button) => button.textContent?.includes("Sign out")))')

let shell = await evaluate(`(() => ({ innerWidth, clientWidth: document.documentElement.clientWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, tabs: [...document.querySelectorAll('[role="tab"]')].length, signOut: [...document.querySelectorAll('button')].some((node) => node.textContent?.includes('Sign out')) }))()`)
const desktopShell = shell
assert.equal(shell.tabs, 6)
assertRootWithinViewport(shell, 'desktop shell')
assert.equal(shell.signOut, true)

for (const view of ['Overview', 'Holdings', 'Activity', 'Reviews', 'Market Data', 'Settings']) {
  await activateTab(view)
  const viewCheck = await evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth, heading: document.querySelector('h1')?.id, skip: Boolean(document.querySelector('.skip-link')), selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent, bodyScrollWidth: document.body.scrollWidth }))()`)
  assertRootWithinViewport(viewCheck, `desktop ${view}`)
  assert.ok(viewCheck.heading, `${view} has no current heading`)
  assert.equal(viewCheck.skip, true, `${view} has no skip link`)
  assert.equal(viewCheck.selected?.includes(view), true, `${view} selected tab mismatch`)
  await evaluate(`(() => { document.querySelector('.skip-link')?.focus(); return document.activeElement?.classList.contains('skip-link') })()`)
  await keyPress('Enter', { wait: false })
  await waitFor('document.activeElement?.id === "portfolio-content"')
}

await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 800, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await settle()
shell = await evaluate(`(() => ({ innerWidth, clientWidth: document.documentElement.clientWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, tabs: [...document.querySelectorAll('[role="tab"]')].length, signOut: [...document.querySelectorAll('button')].some((node) => node.textContent?.includes('Sign out')) }))()`)
assert.equal(shell.tabs, 6)
assertRootWithinViewport(shell, '320px shell')
assert.equal(shell.signOut, true)
for (const view of ['Overview', 'Holdings', 'Activity', 'Reviews', 'Market Data', 'Settings']) {
  await activateTab(view)
  const viewCheck = await evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth, heading: document.querySelector('h1')?.id, skip: Boolean(document.querySelector('.skip-link')), selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent, bodyScrollWidth: document.body.scrollWidth }))()`)
  assertRootWithinViewport(viewCheck, `320px ${view}`)
  assert.ok(viewCheck.heading, `${view} has no current heading`)
  assert.equal(viewCheck.skip, true, `${view} has no skip link`)
  assert.equal(viewCheck.selected?.includes(view), true, `${view} selected tab mismatch`)
  await evaluate(`(() => { document.querySelector('.skip-link')?.focus(); return document.activeElement?.classList.contains('skip-link') })()`)
  await keyPress('Enter', { wait: false })
  await waitFor('document.activeElement?.id === "portfolio-content"')
}

async function marketState() {
  return evaluate(`(() => {
    const region = document.querySelector('[role="region"][aria-label*="Market"]')
    const table = region?.querySelector('table')
    const rows = region ? [...region.querySelectorAll('tbody tr')] : []
    const controls = rows.flatMap((row) => [...row.querySelectorAll('select, input')]).filter((control) => !control.disabled)
    const identities = rows.map((row) => row.querySelector('.mapping-identity-cell'))
    const rr = region?.getBoundingClientRect()
    const rect = (node) => node?.getBoundingClientRect().toJSON()
    const focused = document.activeElement
    const focusedRect = focused && focused !== document.body ? focused.getBoundingClientRect() : null
    const edgeHit = (x, y) => { const node = document.elementFromPoint(x, y); return node === focused || Boolean(node && focused?.contains(node)) }
    return {
      innerWidth,
      rootScrollWidth: document.documentElement.scrollWidth,
      bodyScrollWidth: document.body.scrollWidth,
      scrollLeft: region?.scrollLeft,
      region: rect(region),
      table: rect(table),
      scrollWidth: region?.scrollWidth,
      clientWidth: region?.clientWidth,
      rows: rows.length,
      symbols: rows.map((row) => row.querySelector('.mapping-identity-cell strong')?.textContent?.trim()),
      identities: identities.map(rect),
      candidateRadios: rows.flatMap((row) => [...row.querySelectorAll('input[type="radio"]')]).length,
      unavailableRows: rows.filter((row) => /unavailable/i.test(row.textContent || '')).length,
      controls: controls.map((control) => ({ type: control.getAttribute('type') || control.tagName.toLowerCase(), label: control.getAttribute('aria-label') || '' })),
      rowControlCounts: rows.map((row) => [...row.querySelectorAll('select, input')].filter((control) => !control.disabled).length),
      focused: focused && focused !== document.body ? { index: controls.indexOf(focused), type: focused.getAttribute('type') || focused.tagName.toLowerCase(), rect: focusedRect?.toJSON(), identityRect: focused.closest('.mapping-identity-cell')?.getBoundingClientRect().toJSON(), leftHit: focusedRect ? edgeHit(focusedRect.left + 1, focusedRect.top + focusedRect.height / 2) : false, rightHit: focusedRect ? edgeHit(focusedRect.right - 1, focusedRect.top + focusedRect.height / 2) : false } : null,
    }
  })()`)
}
function assertMarketState(state, label, { requireRows = true } = {}) {
  assertRootWithinViewport(state, label)
  if (!requireRows) return
  assert.equal(state.rows, 2, `${label}: expected both same-symbol mapping rows: ${JSON.stringify(state)}`)
  assert.deepEqual(state.symbols, ['SAME', 'SAME'], `${label}: expected same-symbol fixture rows`)
  assert.equal(state.candidateRadios, 1, `${label}: populated candidate path was not exercised`)
  assert.equal(state.unavailableRows, 1, `${label}: unavailable discovery row was not exercised`)
  assert.equal(state.controls.length, 11, `${label}: expected radio plus five mapping controls per row`)
  assert.deepEqual(state.rowControlCounts, [6, 5], `${label}: expected populated-candidate and unavailable rows with all controls`)
  assert.deepEqual(state.controls.reduce((counts, control) => ({ ...counts, [control.type]: (counts[control.type] || 0) + 1 }), {}), { radio: 1, select: 2, input: 6, checkbox: 2 }, `${label}: expected candidate/provider/field/confirmation control types`)
  assert.ok(state.table.width <= state.scrollWidth + 1, `${label}: table exceeds scroll extent: ${JSON.stringify(state)}`)
  if (state.scrollWidth > state.clientWidth + 1) assert.ok(Math.abs(state.table.width - state.scrollWidth) <= 2, `${label}: table/scroll extent diverged: ${JSON.stringify(state)}`)
  assert.ok(state.identities.every((identity) => identity.left >= state.region.left - 1 && identity.right <= state.region.right + 1), `${label}: identity left the usable region: ${JSON.stringify(state)}`)
}
function assertFocusedMarket(state, expectedIndex, label) {
  const focused = state.focused
  assert.ok(focused, `${label}: no active mapping control`)
  assert.equal(focused.index, expectedIndex, `${label}: native focus reached the wrong mapping control`)
  assert.ok(focused.rect.left >= state.region.left - 1, `${label}: focused control left edge is clipped: ${JSON.stringify(state)}`)
  assert.ok(focused.rect.right <= state.region.right + 1, `${label}: focused control right edge is clipped: ${JSON.stringify(state)}`)
  if (focused.identityRect) {
    assert.ok(focused.rect.left >= state.region.left - 1 && focused.rect.right <= focused.identityRect.right + 1, `${label}: candidate control escaped its sticky identity cell: ${JSON.stringify(state)}`)
  } else {
    assert.ok(focused.rect.left >= state.identities[0].right + 4, `${label}: focused control is hidden under sticky identity: ${JSON.stringify(state)}`)
  }
  assert.equal(focused.leftHit, true, `${label}: left edge hit test missed the focused control`)
  assert.equal(focused.rightHit, true, `${label}: right edge hit test missed the focused control`)
}
async function nativeMarketScan(direction) {
  const reverse = direction === 'reverse'
  const expected = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const controls = [...region.querySelectorAll('tbody tr select, tbody tr input')].filter((control) => !control.disabled); region.scrollIntoView({ block: 'start' }); region.scrollLeft = 0; return controls.map((control) => ({ type: control.getAttribute('type') || control.tagName.toLowerCase(), label: control.getAttribute('aria-label') || '' })); })()`)
  assert.equal(expected.length, 11, `Market ${direction} scan has an unexpected control count`)
  if (direction === 'forward') {
    await evaluate(`document.querySelector('[role="region"][aria-label*="Market"]')?.focus(); true`)
    for (let index = 0; index < expected.length; index += 1) {
      await keyPress('Tab')
      const state = await marketState()
      assertMarketState(state, `Market native forward ${index}`)
      assertFocusedMarket(state, index, `Market native forward ${index}`)
    }
  } else {
    await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); const controls = [...region.querySelectorAll('tbody tr select, tbody tr input')].filter((control) => !control.disabled); document.activeElement?.blur(); controls.at(-1)?.focus(); return Boolean(document.activeElement); })()`)
    for (let index = expected.length - 1; index >= 0; index -= 1) {
      const state = await marketState()
      assertMarketState(state, `Market native reverse ${index}`)
      assertFocusedMarket(state, index, `Market native reverse ${index}`)
      if (index > 0) await keyPress('Tab', { shift: true })
    }
  }
  return expected
}
async function marketHorizontalSweep(label) {
  await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Market"]'); region.scrollIntoView({ block: 'start' }); region.scrollLeft = 0; region.focus(); return region.getBoundingClientRect().toJSON(); })()`)
  let state = await marketState()
  assertMarketState(state, `${label} horizontal start`)
  const maxScroll = state.scrollWidth ? state.scrollWidth - state.clientWidth : 0
  const samples = [state.scrollLeft || 0, maxScroll]
  if (maxScroll > 0) {
    let last = state.scrollLeft || 0
    for (let index = 0; index < 30; index += 1) {
      await horizontalWheel(state.region, 450)
      state = await marketState()
      assertMarketState(state, `${label} wheel forward ${index}`)
      if (!samples.includes(state.scrollLeft)) samples.push(state.scrollLeft)
      if (state.scrollLeft === last) break
      last = state.scrollLeft
    }
    assert.ok((state.scrollLeft || 0) >= maxScroll - 1, `${label}: native wheel did not reach actual max: ${JSON.stringify(state)}`)
    for (let index = 0; index < 30; index += 1) {
      await horizontalWheel(state.region, -450)
      state = await marketState()
      assertMarketState(state, `${label} wheel reverse ${index}`)
      if (!samples.includes(state.scrollLeft)) samples.push(state.scrollLeft)
      if (state.scrollLeft === 0) break
    }
    assert.equal(state.scrollLeft, 0, `${label}: native reverse wheel did not return to zero`)
    await evaluate(`document.querySelector('[role="region"][aria-label*="Market"]')?.focus(); true`)
    for (let index = 0; index < 4; index += 1) {
      await keyPress('ArrowRight')
      state = await marketState()
      assertMarketState(state, `${label} keyboard horizontal ${index}`)
    }
  }
  assert.ok(samples.length >= 1)
  return { ...state, samples }
}

await command('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await settle()
await activateTab('Market Data')
await waitFor('Boolean(document.querySelector("[role=region][aria-label*=Market] tbody tr"))')
await waitFor('document.querySelectorAll("[role=region][aria-label*=Market] input[type=radio]").length === 1')
const marketDesktop = await marketHorizontalSweep('desktop Market')
await nativeMarketScan('forward')
await nativeMarketScan('reverse')
await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 800, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await settle()
await activateTab('Market Data')
const market320 = await marketHorizontalSweep('320px Market')
await nativeMarketScan('forward')
await nativeMarketScan('reverse')
const marketResultAfterWheel = await evaluate(`(async () => {
  const region = document.querySelector('[role="region"][aria-label*="Market"]')
  const rows = [...region.querySelectorAll('tbody tr')]
  region.scrollLeft = region.scrollWidth
  const farRightControl = rows.at(-1)?.querySelector('td:last-child input[type="checkbox"]')
  farRightControl?.focus()
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  const identity = rows[0]?.querySelector('.mapping-identity-cell')
  return { region: region.getBoundingClientRect().toJSON(), identity: identity?.getBoundingClientRect().toJSON(), farRight: farRightControl?.getBoundingClientRect().toJSON(), scrollLeft: region.scrollLeft, scrollWidth: region.scrollWidth, rootScrollWidth: document.documentElement.scrollWidth, innerWidth }
})()`)
assert.ok(marketResultAfterWheel.farRight, `Market far-right confirmation control is missing: ${JSON.stringify(marketResultAfterWheel)}`)
assert.ok(marketResultAfterWheel.identity.left >= marketResultAfterWheel.region.left - 1 && marketResultAfterWheel.identity.right <= marketResultAfterWheel.region.right + 1, `Market identity left the region at max scroll: ${JSON.stringify(marketResultAfterWheel)}`)
assert.ok(marketResultAfterWheel.farRight.left >= marketResultAfterWheel.identity.right + 4, `Market far-right control is under sticky identity: ${JSON.stringify(marketResultAfterWheel)}`)
assert.ok(marketResultAfterWheel.farRight.left >= marketResultAfterWheel.region.left && marketResultAfterWheel.farRight.right <= marketResultAfterWheel.region.right, `Market far-right control is clipped at max scroll: ${JSON.stringify(marketResultAfterWheel)}`)

async function holdingsState() {
  return evaluate(`(() => {
    const region = document.querySelector('[role="region"][aria-label*="Holdings"]')
    const controls = [...region.querySelectorAll('button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])')].filter((control) => !control.disabled)
    const identity = region.querySelector('.holding-identity-cell')
    const focused = document.activeElement
    const focusedRect = focused && focused !== document.body ? focused.getBoundingClientRect() : null
    return { innerWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, region: region.getBoundingClientRect().toJSON(), scrollWidth: region.scrollWidth, controls: controls.length, focused: focused && focused !== document.body ? { index: controls.indexOf(focused), rect: focusedRect?.toJSON(), inIdentity: Boolean(focused.closest('.holding-identity-cell')), identityRight: identity?.getBoundingClientRect().right, leftHit: focusedRect ? document.elementFromPoint(focusedRect.left + 1, focusedRect.top + focusedRect.height / 2) === focused : false, rightHit: focusedRect ? document.elementFromPoint(focusedRect.right - 1, focusedRect.top + focusedRect.height / 2) === focused : false } : null, identity: identity?.getBoundingClientRect().toJSON(), action: region.querySelector('.row-action-trigger')?.getBoundingClientRect().toJSON(), lotLabels: [...region.querySelectorAll('tbody tr .holding-identity-cell')].map((node) => node.textContent) }
  })()`)
}
async function nativeHoldingsScan(direction) {
  const expected = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Holdings"]'); return [...region.querySelectorAll('button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])')].filter((control) => !control.disabled).length })()`)
  assert.ok(expected > 0, `Holdings ${direction} scan has no controls`)
  await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Holdings"]'); region.scrollIntoView({ block: 'start' }); region.scrollLeft = ${direction === 'reverse' ? 'region.scrollWidth' : '0'}; const controls = [...region.querySelectorAll('button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])')].filter((control) => !control.disabled); if (${JSON.stringify(direction)} === 'reverse') controls.at(-1)?.focus(); else region.focus(); return controls.length; })()`)
  const sequence = []
  if (direction === 'forward') {
    for (let index = 0; index < expected; index += 1) {
      await keyPress('Tab')
      const state = await holdingsState()
      assert.equal(state.focused?.index, index, `Holdings forward native focus mismatch: ${JSON.stringify(state)}`)
      assert.ok(state.focused.rect.left >= state.region.left - 1 && state.focused.rect.right <= state.region.right + 1, `Holdings forward focus clipped: ${JSON.stringify(state)}`)
      if (!state.focused.inIdentity) assert.ok(state.focused.rect.left >= state.focused.identityRight + 4, `Holdings forward focus hidden under identity: ${JSON.stringify(state)}`)
      assert.equal(state.focused.leftHit, true)
      assert.equal(state.focused.rightHit, true)
      sequence.push(state.focused.index)
    }
  } else {
    for (let index = expected - 1; index >= 0; index -= 1) {
      const state = await holdingsState()
      assert.equal(state.focused?.index, index, `Holdings reverse native focus mismatch: ${JSON.stringify(state)}`)
      assert.ok(state.focused.rect.left >= state.region.left - 1 && state.focused.rect.right <= state.region.right + 1, `Holdings reverse focus clipped: ${JSON.stringify(state)}`)
      if (!state.focused.inIdentity) assert.ok(state.focused.rect.left >= state.focused.identityRight + 4, `Holdings reverse focus hidden under identity: ${JSON.stringify(state)}`)
      assert.equal(state.focused.leftHit, true)
      assert.equal(state.focused.rightHit, true)
      sequence.push(state.focused.index)
      if (index > 0) await keyPress('Tab', { shift: true })
    }
  }
  return sequence
}
async function holdingsGeometry(label) {
  await activateTab('Holdings')
  await waitFor('Boolean(document.querySelector("[role=region][aria-label*=Holdings] tbody tr"))')
  const state = await holdingsState()
  assertRootWithinViewport(state, label)
  assert.equal(state.lotLabels.length, 2, `${label}: expected two same-symbol lots`)
  assert.notEqual(state.lotLabels[0], state.lotLabels[1], `${label}: same-symbol lot identity collapsed`)
  await nativeHoldingsScan('forward')
  await nativeHoldingsScan('reverse')
  const result = await evaluate(`(async () => { const region=document.querySelector('[role="region"][aria-label*="Holdings"]'); const identity=region.querySelector('.holding-identity-cell'); region.scrollLeft=region.scrollWidth; const action=region.querySelector('.row-action-trigger'); action?.focus(); await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))); return { rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, innerWidth, region: region.getBoundingClientRect().toJSON(), scrollWidth: region.scrollWidth, identity: identity.getBoundingClientRect().toJSON(), action: action?.getBoundingClientRect().toJSON(), lotLabels: [...region.querySelectorAll('tbody tr .holding-identity-cell')].map((node) => node.textContent) } })()`)
  assertRootWithinViewport(result, label)
  assert.ok(result.scrollWidth > result.region.width, `${label}: table is not horizontally bounded`)
  assert.ok(result.action?.left >= result.identity.right + 4 && result.action.right <= result.region.right, `${label}: far-right action is not reachable: ${JSON.stringify(result)}`)
  assert.ok(result.identity.left >= result.region.left - 1 && result.identity.right <= result.region.right + 1, `${label}: sticky identity left the region`)
  return result
}

await command('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await settle()
const holdingsDesktop = await holdingsGeometry('desktop Holdings')
await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 520, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await settle()
const holdingsNarrow = await holdingsGeometry('resized 320px Holdings')
await evaluate(`document.querySelector('.row-action-trigger')?.click(); true`)
await waitFor('Boolean(document.querySelector("[role=menu]"))')
const menuAtNarrow = await evaluate(`(() => { const paper = document.querySelector('.row-action-menu'); const rect = paper.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewport: { width: innerWidth, height: innerHeight } } })()`)
for (const edge of ['left', 'top']) assert.ok(menuAtNarrow[edge] >= 0, JSON.stringify(menuAtNarrow))
assert.ok(menuAtNarrow.right <= menuAtNarrow.viewport.width, JSON.stringify(menuAtNarrow))
assert.ok(menuAtNarrow.bottom <= menuAtNarrow.viewport.height, JSON.stringify(menuAtNarrow))
await evaluate('window.scrollTo(0, 120); true')
await settle()
const menuAfterWindowScroll = await evaluate(`(() => { const paper = document.querySelector('.row-action-menu'); const rect = paper.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewport: { width: innerWidth, height: innerHeight }, rootScrollWidth: document.documentElement.scrollWidth } })()`)
assert.ok(menuAfterWindowScroll.right <= menuAfterWindowScroll.viewport.width, JSON.stringify(menuAfterWindowScroll))
assert.ok(menuAfterWindowScroll.bottom <= menuAfterWindowScroll.viewport.height, JSON.stringify(menuAfterWindowScroll))
assert.ok(menuAfterWindowScroll.rootScrollWidth <= menuAfterWindowScroll.viewport.width, JSON.stringify(menuAfterWindowScroll))
await evaluate('document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); true')
await command('Emulation.setPageScaleFactor', { pageScaleFactor: 2 })
await settle()
const reflow = await evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, signOut: [...document.querySelectorAll('button')].some((node) => node.textContent?.includes('Sign out')), scale: visualViewport?.scale }))()`)
assertRootWithinViewport(reflow, '200% page-scale reflow')
assert.equal(reflow.signOut, true)
console.log(JSON.stringify({ desktop: desktopShell, viewport: shell, market: { desktop: marketDesktop, narrow: market320, farRight: marketResultAfterWheel }, holdings: { desktop: holdingsDesktop, narrow: holdingsNarrow }, reflow, menus: { atNarrow: menuAtNarrow, afterWindowScroll: menuAfterWindowScroll } }, null, 2))
socket.close()

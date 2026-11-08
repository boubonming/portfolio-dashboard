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
async function activateTab(label) {
  await evaluate(`(() => { const tab = [...document.querySelectorAll('[role="tab"]')].find((node) => node.textContent?.includes(${JSON.stringify(label)})); tab?.click(); return Boolean(tab); })()`)
  await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.textContent?.includes(${JSON.stringify(label)}) && node.getAttribute('aria-selected') === 'true'))`)
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
assert.ok(shell.rootScrollWidth <= shell.innerWidth, JSON.stringify(shell))
assert.ok(shell.bodyScrollWidth <= shell.innerWidth, JSON.stringify(shell))
assert.equal(shell.signOut, true)

for (const view of ['Overview', 'Holdings', 'Activity', 'Reviews', 'Market Data', 'Settings']) {
  await activateTab(view)
  const viewCheck = await evaluate(`(() => ({ rootScrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth, heading: document.querySelector('h1')?.id, skip: Boolean(document.querySelector('.skip-link')), selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent }))()`)
  assert.ok(viewCheck.rootScrollWidth <= shell.innerWidth, `${view}: ${JSON.stringify(viewCheck)}`)
  assert.ok(viewCheck.heading, `${view} has no current heading`)
  assert.equal(viewCheck.skip, true, `${view} has no skip link`)
  assert.equal(viewCheck.selected?.includes(view), true, `${view} selected tab mismatch`)
  await evaluate(`(() => { document.querySelector('.skip-link')?.focus(); return document.activeElement?.classList.contains('skip-link') })()`)
  await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
  await waitFor('document.activeElement?.id === "portfolio-content"')
}

await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 800, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await new Promise((resolve) => setTimeout(resolve, 100))
shell = await evaluate(`(() => ({ innerWidth, clientWidth: document.documentElement.clientWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, tabs: [...document.querySelectorAll('[role="tab"]')].length, signOut: [...document.querySelectorAll('button')].some((node) => node.textContent?.includes('Sign out')) }))()`)
assert.equal(shell.tabs, 6)
assert.ok(shell.rootScrollWidth <= shell.innerWidth, JSON.stringify(shell))
assert.ok(shell.bodyScrollWidth <= shell.innerWidth, JSON.stringify(shell))
assert.equal(shell.signOut, true)
for (const view of ['Overview', 'Holdings', 'Activity', 'Reviews', 'Market Data', 'Settings']) {
  await activateTab(view)
  const viewCheck = await evaluate(`(() => ({ rootScrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth, heading: document.querySelector('h1')?.id, skip: Boolean(document.querySelector('.skip-link')), selected: [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true')?.textContent }))()`)
  assert.ok(viewCheck.rootScrollWidth <= shell.innerWidth, `${view}: ${JSON.stringify(viewCheck)}`)
  assert.ok(viewCheck.heading, `${view} has no current heading`)
  assert.equal(viewCheck.skip, true, `${view} has no skip link`)
  assert.equal(viewCheck.selected?.includes(view), true, `${view} selected tab mismatch`)
  await evaluate(`(() => { document.querySelector('.skip-link')?.focus(); return document.activeElement?.classList.contains('skip-link') })()`)
  await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 })
  await waitFor('document.activeElement?.id === "portfolio-content"')
}

await activateTab('Market Data')
await waitFor('Boolean(document.querySelector("[role=region][aria-label*=Market] tbody tr"))')
const marketResult = await evaluate(`(async () => {
  const region = document.querySelector('[role="region"][aria-label*="Market"]')
  region.scrollIntoView({ block: 'start' })
  await new Promise((resolve) => requestAnimationFrame(resolve))
  const rows = [...region.querySelectorAll('tbody tr')]
  const controls = rows.flatMap((row) => [...row.querySelectorAll('select, input')])
  const identity = rows[0].querySelector('.mapping-identity-cell')
  const regionRect = region.getBoundingClientRect()
  const identityWidth = identity.getBoundingClientRect().width
  const focusRects = []
  for (const control of controls) {
    control.focus()
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
    const rect = control.getBoundingClientRect()
    const inIdentity = Boolean(control.closest('.mapping-identity-cell'))
    focusRects.push({ left: rect.left, right: rect.right, regionLeft: regionRect.left, regionRight: regionRect.right, identityRight: regionRect.left + identityWidth, inIdentity })
  }
  region.scrollIntoView({ block: 'start' })
  await new Promise((resolve) => requestAnimationFrame(resolve))
  region.scrollLeft = 0
  const startIdentity = identity.getBoundingClientRect()
  const wheelTarget = region.getBoundingClientRect()
  return { region: regionRect.toJSON(), startIdentity: startIdentity.toJSON(), wheelTarget: wheelTarget.toJSON(), identity: identity.getBoundingClientRect().toJSON(), scrollWidth: region.scrollWidth, rootScrollWidth: document.documentElement.scrollWidth, focusRects, farRight: null }
})()`)
assert.ok(marketResult.rootScrollWidth <= shell.innerWidth, JSON.stringify(marketResult))
assert.ok(marketResult.scrollWidth > marketResult.region.width, JSON.stringify(marketResult))
await command('Input.dispatchMouseEvent', { type: 'mouseWheel', x: marketResult.wheelTarget.left + marketResult.wheelTarget.width / 2, y: Math.min(marketResult.wheelTarget.top + 20, 780), deltaX: 2000, deltaY: 0 })
await new Promise((resolve) => setTimeout(resolve, 100))
const wheelResult = await evaluate(`(() => { const region=document.querySelector('[role="region"][aria-label*="Market"]'); const identity=region.querySelector('.mapping-identity-cell'); const rr=region.getBoundingClientRect(); const ir=identity.getBoundingClientRect(); return {scrollLeft:region.scrollLeft, region:rr.toJSON(), identity:ir.toJSON()}; })()`)
assert.ok(wheelResult.scrollLeft > 0, JSON.stringify(wheelResult))
assert.ok(wheelResult.identity.left >= wheelResult.region.left, JSON.stringify(wheelResult))
assert.ok(wheelResult.identity.right <= wheelResult.region.right + 1, JSON.stringify(wheelResult))
const marketResultAfterWheel = await evaluate(`(async () => {
  const region = document.querySelector('[role="region"][aria-label*="Market"]')
  const rows = [...region.querySelectorAll('tbody tr')]
  const identity = rows[0].querySelector('.mapping-identity-cell')
  region.scrollLeft = region.scrollWidth
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  const endIdentity = identity.getBoundingClientRect()
  const farRightControl = rows[0].querySelector('td:last-child input[type="checkbox"]')
  farRightControl?.focus()
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))
  const farRight = farRightControl?.getBoundingClientRect()
  return { region: region.getBoundingClientRect().toJSON(), startIdentity: identity.getBoundingClientRect().toJSON(), identity: endIdentity.toJSON(), scrollWidth: region.scrollWidth, rootScrollWidth: document.documentElement.scrollWidth, focusRects: [], farRight: farRight?.toJSON() }
})()`)
assert.ok(marketResultAfterWheel.rootScrollWidth <= shell.innerWidth, JSON.stringify(marketResultAfterWheel))
assert.ok(marketResultAfterWheel.farRight.left >= marketResultAfterWheel.identity.right + 4, JSON.stringify(marketResultAfterWheel))
assert.ok(marketResultAfterWheel.farRight.right <= marketResultAfterWheel.region.right - 4, JSON.stringify(marketResultAfterWheel))
for (const rect of marketResult.focusRects) {
  if (rect.inIdentity) {
    assert.ok(rect.left >= rect.regionLeft, JSON.stringify(rect))
    assert.ok(rect.right <= rect.identityRight + 1, JSON.stringify(rect))
  } else {
    assert.ok(rect.left >= rect.identityRight + 4, JSON.stringify(rect))
    assert.ok(rect.right <= rect.regionRight - 4, JSON.stringify(rect))
  }
}
for (const identity of [marketResult.startIdentity, marketResultAfterWheel.identity]) {
  assert.ok(identity.left >= marketResult.region.left, JSON.stringify(marketResult))
  assert.ok(identity.right <= marketResult.region.right + 1, JSON.stringify(marketResult))
}

await activateTab('Holdings')
await waitFor('Boolean(document.querySelector("[role=region][aria-label*=Holdings] tbody tr"))')
const holdingsControlCount = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Holdings"]'); return [...region.querySelectorAll('button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])')].filter((control) => !control.disabled).length })()`)
await evaluate(`document.querySelector('[role="region"][aria-label*="Holdings"]')?.focus(); true`)
const holdingsFocus = []
for (let index = 0; index < holdingsControlCount; index += 1) {
  await command('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9, nativeVirtualKeyCode: 9 })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9, nativeVirtualKeyCode: 9 })
  await new Promise((resolve) => setTimeout(resolve, 30))
  const focused = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Holdings"]'); const control = document.activeElement; const rect = control?.getBoundingClientRect(); const regionRect = region.getBoundingClientRect(); return { inside: region.contains(control), inIdentity: Boolean(control?.closest('.holding-identity-cell')), label: control?.getAttribute('aria-label') || control?.textContent?.trim(), left: rect?.left, right: rect?.right, regionLeft: regionRect.left, regionRight: regionRect.right, identityRight: region.querySelector('.holding-identity-cell').getBoundingClientRect().right, scrollLeft: region.scrollLeft } })()`)
  assert.equal(focused.inside, true, JSON.stringify(focused))
  holdingsFocus.push(focused)
}
assert.equal(holdingsFocus.length, holdingsControlCount)
assert.ok((await evaluate('document.documentElement.scrollWidth')) <= shell.innerWidth, JSON.stringify(holdingsFocus))
for (const rect of holdingsFocus.filter((candidate) => !candidate.inIdentity)) {
  assert.ok(rect.left >= rect.identityRight + 4, JSON.stringify(rect))
  assert.ok(rect.right <= rect.regionRight - 4, JSON.stringify(rect))
}
const holdingsResult = await evaluate(`(() => { const region = document.querySelector('[role="region"][aria-label*="Holdings"]'); const identity = region.querySelector('.holding-identity-cell'); region.scrollLeft = region.scrollWidth; const action = region.querySelector('.row-action-trigger'); return { rootScrollWidth: document.documentElement.scrollWidth, region: region.getBoundingClientRect().toJSON(), scrollWidth: region.scrollWidth, identity: identity.getBoundingClientRect().toJSON(), action: action?.getBoundingClientRect().toJSON(), lotLabels: [...region.querySelectorAll('tbody tr .holding-identity-cell')].map((node) => node.textContent) } })()`)
assert.ok(holdingsResult.rootScrollWidth <= shell.innerWidth, JSON.stringify(holdingsResult))
assert.ok(holdingsResult.scrollWidth > holdingsResult.region.width, JSON.stringify(holdingsResult))
assert.ok(holdingsResult.action?.right <= holdingsResult.region.right + 1, JSON.stringify(holdingsResult))
assert.equal(holdingsResult.lotLabels.length, 2)
assert.notEqual(holdingsResult.lotLabels[0], holdingsResult.lotLabels[1])
await evaluate(`document.querySelector('.row-action-trigger')?.click(); true`)
await waitFor('Boolean(document.querySelector("[role=menu]"))')
const menuAtDesktopNarrow = await evaluate(`(() => { const paper = document.querySelector('.row-action-menu'); const rect = paper.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewport: { width: innerWidth, height: innerHeight } } })()`)
for (const edge of ['left', 'top']) assert.ok(menuAtDesktopNarrow[edge] >= 0, JSON.stringify(menuAtDesktopNarrow))
assert.ok(menuAtDesktopNarrow.right <= menuAtDesktopNarrow.viewport.width, JSON.stringify(menuAtDesktopNarrow))
assert.ok(menuAtDesktopNarrow.bottom <= menuAtDesktopNarrow.viewport.height, JSON.stringify(menuAtDesktopNarrow))
await command('Emulation.setDeviceMetricsOverride', { width: 320, height: 520, deviceScaleFactor: 1, mobile: false })
await evaluate('window.dispatchEvent(new Event("resize")); true')
await new Promise((resolve) => setTimeout(resolve, 75))
const menuAfterResize = await evaluate(`(() => { const paper = document.querySelector('.row-action-menu'); const rect = paper.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewport: { width: innerWidth, height: innerHeight } } })()`)
assert.ok(menuAfterResize.right <= menuAfterResize.viewport.width, JSON.stringify(menuAfterResize))
assert.ok(menuAfterResize.bottom <= menuAfterResize.viewport.height, JSON.stringify(menuAfterResize))
await evaluate('window.scrollTo(0, 120); true')
await new Promise((resolve) => setTimeout(resolve, 75))
const menuAfterWindowScroll = await evaluate(`(() => { const paper = document.querySelector('.row-action-menu'); const rect = paper.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, viewport: { width: innerWidth, height: innerHeight }, rootScrollWidth: document.documentElement.scrollWidth } })()`)
assert.ok(menuAfterWindowScroll.right <= menuAfterWindowScroll.viewport.width, JSON.stringify(menuAfterWindowScroll))
assert.ok(menuAfterWindowScroll.bottom <= menuAfterWindowScroll.viewport.height, JSON.stringify(menuAfterWindowScroll))
assert.ok(menuAfterWindowScroll.rootScrollWidth <= menuAfterWindowScroll.viewport.width, JSON.stringify(menuAfterWindowScroll))
await evaluate('document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); true')
await command('Emulation.setPageScaleFactor', { pageScaleFactor: 2 })
await new Promise((resolve) => setTimeout(resolve, 100))
const reflow = await evaluate(`(() => ({ innerWidth, rootScrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, signOut: [...document.querySelectorAll('button')].some((node) => node.textContent?.includes('Sign out')), scale: visualViewport?.scale }))()`)
assert.ok(reflow.rootScrollWidth <= reflow.innerWidth, JSON.stringify(reflow))
assert.ok(reflow.bodyScrollWidth <= reflow.innerWidth, JSON.stringify(reflow))
assert.equal(reflow.signOut, true)
console.log(JSON.stringify({ desktop: desktopShell, viewport: shell, market: marketResult, marketAfterWheel: wheelResult, marketFarRight: marketResultAfterWheel, holdings: holdingsResult, holdingsFocus, reflow, menus: { atDesktopNarrow: menuAtDesktopNarrow, afterResize: menuAfterResize, afterWindowScroll: menuAfterWindowScroll } }, null, 2))
socket.close()

#!/usr/bin/env node
// S4_APP_URL=http://127.0.0.1:4173/ S4_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s4-production-recovery-check.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const appURL = process.env.S4_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S4_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const fixture = await readFile(new URL('./s4-synthetic-fixture.js', import.meta.url), 'utf8')

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
  if (!message.id || !pending.has(message.id)) return
  const { resolve, reject } = pending.get(message.id)
  pending.delete(message.id)
  if (message.error) reject(new Error(`${message.error.code}: ${message.error.message}`))
  else resolve(message.result)
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
async function waitFor(expression, timeout = 6000) {
  const started = Date.now()
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return
    await new Promise((resolve) => setTimeout(resolve, 60))
  }
  throw new Error(`Timed out waiting for ${expression}`)
}
async function settle() { await new Promise((resolve) => setTimeout(resolve, 100)) }
async function keyPress(key, { ctrl = false, shift = false } = {}) {
  const modifiers = ctrl ? 2 : shift ? 8 : 0
  const virtualKeyCodes = { Enter: 13, Tab: 9, ArrowRight: 39, a: 65 }
  const params = { key, code: key === 'a' ? 'KeyA' : key, modifiers }
  if (virtualKeyCodes[key]) Object.assign(params, { windowsVirtualKeyCode: virtualKeyCodes[key], nativeVirtualKeyCode: virtualKeyCodes[key] })
  // Chromium needs the native character payload for Enter to synthesize the
  // browser's default button activation (keydown/up alone is not enough).
  const character = key === 'Enter' ? '\r' : undefined
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params, ...(character ? { text: character, unmodifiedText: character } : {}) })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  await settle()
}
async function activateButton(label) {
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent?.trim() === ${JSON.stringify(label)} || candidate.getAttribute('aria-label') === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Button not found: ${label}`)
  await keyPress('Enter')
}
async function activateTab(label, { expectSelected = true } = {}) {
  const found = await evaluate(`(() => { const node = [...document.querySelectorAll('[role="tab"]')].find((candidate) => (candidate.textContent || '').trim() === ${JSON.stringify(label)} || candidate.getAttribute('aria-label') === ${JSON.stringify(label)}); node?.focus(); return Boolean(node); })()`)
  assert.equal(found, true, `Tab not found: ${label}`)
  await keyPress('Enter')
  if (expectSelected) await waitFor(`Boolean([...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true' && ((node.textContent || '').trim() === ${JSON.stringify(label)} || node.getAttribute('aria-label') === ${JSON.stringify(label)})))`)
}
async function typeSummary(value) {
  await evaluate('document.querySelector("#thesis-summary")?.focus(); true')
  await keyPress('a', { ctrl: true })
  await command('Input.insertText', { text: value })
  await settle()
}
async function reload(width) {
  await command('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false })
  await command('Page.navigate', { url: appURL })
  await waitFor('Boolean(window.__s4 && document.querySelector("button.sign-out"))')
  await waitFor('document.querySelectorAll("[role=tab]").length === 6')
  await evaluate('window.__s4.reset(); true')
  await settle()
}
async function openThesis() {
  await activateTab('Holdings')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => /Open thesis for S4 lot lot-s4/.test(node.getAttribute("aria-label") || "")))')
  await activateButton('Open thesis')
  await waitFor('Boolean(document.querySelector("#thesis-title"))')
  await waitFor('Boolean(document.querySelector("#thesis-summary"))')
}
async function assertDecisionFocus() {
  await waitFor('Boolean(document.activeElement?.matches(".thesis-exit-prompt"))')
  const first = await evaluate('document.activeElement?.className')
  assert.equal(first, 'notice warning thesis-exit-prompt')
  await keyPress('Tab')
  assert.equal(await evaluate('document.activeElement?.textContent?.trim()'), 'Keep editing')
  await keyPress('Tab')
  assert.equal(await evaluate('document.activeElement?.textContent?.trim()'), 'Discard changes and leave')
}
async function leaveDirtyVia(label) {
  await activateButton(label)
  await waitFor('Boolean(document.querySelector(".thesis-exit-prompt"))')
  await assertDecisionFocus()
}
async function assertSelected(label) {
  const result = await evaluate(`(() => { const selected = [...document.querySelectorAll('[role="tab"]')].find((node) => node.getAttribute('aria-selected') === 'true'); return { selected: selected?.textContent?.trim(), heading: document.querySelector('h1')?.textContent?.trim() }; })()`)
  assert.equal(result.selected, label, `Selected tab mismatch: ${JSON.stringify(result)}`)
}
async function runWidth(width) {
  await reload(width)
  await openThesis()
  await activateTab('Activity')
  await waitFor('Boolean(document.querySelector("h2")?.textContent?.includes("Activity"))')
  await assertSelected('Activity')

  await reload(width)
  await openThesis()
  await typeSummary('Keyboard protected draft')
  await leaveDirtyVia('Close thesis')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Keep editing"))')
  const prompt = await evaluate('(() => { const node = document.querySelector(".thesis-exit-prompt"); const rect = node?.getBoundingClientRect(); return { visible: Boolean(node), top: rect?.top, bottom: rect?.bottom, focused: document.activeElement?.textContent?.trim() }; })()')
  assert.equal(prompt.visible, true)
  assert.ok(prompt.top >= 0 && prompt.bottom <= (await evaluate('window.innerHeight')), `Dirty prompt is not visible: ${JSON.stringify(prompt)}`)
  await activateButton('Keep editing')
  await waitFor('document.activeElement?.id === "thesis-title"')
  assert.equal(await evaluate('document.querySelector("#thesis-summary")?.value'), 'Keyboard protected draft')
  await typeSummary('Cancel protected draft')
  await leaveDirtyVia('Cancel')
  await activateButton('Keep editing')
  await waitFor('document.activeElement?.id === "thesis-title"')
  assert.equal(await evaluate('document.querySelector("#thesis-summary")?.value'), 'Cancel protected draft')
  await typeSummary('Tab protected draft')
  await activateTab('Activity', { expectSelected: false })
  await waitFor('Boolean(document.querySelector(".thesis-exit-prompt"))')
  await assertDecisionFocus()
  await activateButton('Discard changes and leave')
  await waitFor('Boolean(document.querySelector("h2")?.textContent?.includes("Activity"))')
  await assertSelected('Activity')

  await reload(width)
  await evaluate('window.__s4.configure({ deferHistory: true, deferSave: true }); true')
  await openThesis()
  await activateButton('View revision history')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Loading history…"))')
  await typeSummary('Pending save draft')
  await activateButton('Save thesis')
  await waitFor('window.__s4.state.pendingSaves.length === 1')
  await activateButton('Close thesis')
  await waitFor('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Keep editing"))')
  await activateButton('Keep editing')
  const aborted = await evaluate('({ history: Boolean(window.__s4.state.pendingHistory[0]?.signal?.aborted), save: Boolean(window.__s4.state.pendingSaves[0]?.signal?.aborted) })')
  assert.deepEqual(aborted, { history: true, save: true })
  await evaluate('window.__s4.resolveHistory(); window.__s4.resolveSave(); window.__s4.configure({ deferHistory: false }); true')
  await settle()
  assert.equal(await evaluate('Boolean([...document.querySelectorAll("button")].find((node) => node.textContent?.trim() === "Loading history…"))'), false)
  await activateButton('View revision history')
  await waitFor('Boolean(document.querySelector(".empty")?.textContent?.includes("No revision history"))')

  await reload(width)
  await evaluate('window.__s4.configure({ uncertainNextSave: true }); true')
  await openThesis()
  await typeSummary('Unconfirmed browser draft')
  await activateButton('Save thesis')
  await waitFor('Boolean(document.querySelector("[role=alert]")?.textContent?.includes("not confirmed"))')
  const retryState = await evaluate('(() => { const node = [...document.querySelectorAll("button")].find((candidate) => candidate.textContent?.trim() === "Retry unchanged save"); return { present: Boolean(node), disabled: Boolean(node?.disabled) }; })()')
  assert.deepEqual(retryState, { present: true, disabled: false })
  await activateButton('Retry unchanged save')
  await waitFor('Boolean(document.querySelector("[role=status]")?.textContent?.includes("Thesis saved as revision"))')
  const replay = await evaluate('({ identical: window.__s4.state.putBodies.length === 2 && window.__s4.state.putBodies[0] === window.__s4.state.putBodies[1], history: window.__s4.state.committedHistory.length })')
  assert.deepEqual(replay, { identical: true, history: 1 })

  await reload(width)
  await evaluate('window.__s4.configure({ failLogoutOnce: true }); true')
  await openThesis()
  await typeSummary('Draft before authority loss')
  await activateTab('Activity', { expectSelected: false })
  await waitFor('Boolean(document.querySelector(".thesis-exit-prompt"))')
  await assertDecisionFocus()
  await activateButton('Keep editing')
  await activateButton('Sign out')
  await waitFor('Boolean(document.querySelector("#username"))')
  await activateButton('Retry session check')
  await waitFor('Boolean(document.querySelector("button.sign-out"))')
  await assertSelected('Holdings')
  await openThesis()
  assert.equal(await evaluate('Boolean(document.querySelector("h2")?.textContent?.includes("Activity"))'), false)
  assert.equal(await evaluate('document.querySelector("#thesis-summary")?.value'), 'Canonical S4 thesis')
  return { width, dirtyPrompt: prompt, replay, aborted }
}

await command('Page.enable')
await command('Runtime.enable')
await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
const results = []
for (const width of [1280, 320]) results.push(await runWidth(width))
console.log(JSON.stringify({ widths: results, sessionRetry: true, keyboardOnly: true, noPrivateStorage: await evaluate('localStorage.length === 0 && sessionStorage.length === 0') }, null, 2))
socket.close()
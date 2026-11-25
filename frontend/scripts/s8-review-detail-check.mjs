#!/usr/bin/env node
// S8_APP_URL=http://127.0.0.1:4173/ S8_CDP_ENDPOINT=http://127.0.0.1:9222 node scripts/s8-review-detail-check.mjs
import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'

const appURL = process.env.S8_APP_URL || 'http://127.0.0.1:4173/'
const cdpEndpoint = process.env.S8_CDP_ENDPOINT || 'http://127.0.0.1:9222'
const evidenceDir = process.env.S8_BROWSER_EVIDENCE_DIR
const fixture = await readFile(new URL('./s8-review-synthetic-fixture.js', import.meta.url), 'utf8')

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
  const waiter = pending.get(message.id)
  pending.delete(message.id)
  if (message.error) waiter.reject(new Error(message.error.message))
  else waiter.resolve(message.result)
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
async function waitFor(expression, timeout = 8000) {
  const started = Date.now()
  while (Date.now() - started < timeout) {
    if (await evaluate(expression)) return
    await new Promise((resolve) => setTimeout(resolve, 75))
  }
  throw new Error(`Timed out waiting for ${expression}`)
}
async function keyPress(key) {
  const codes = { Tab: 9, Enter: 13, End: 35, ArrowRight: 39 }
  const params = { key, code: key, modifiers: 0, windowsVirtualKeyCode: codes[key], nativeVirtualKeyCode: codes[key] }
  await command('Input.dispatchKeyEvent', { type: 'keyDown', ...params, ...(key === 'Enter' ? { text: '\r', unmodifiedText: '\r' } : {}) })
  await command('Input.dispatchKeyEvent', { type: 'keyUp', ...params })
  await new Promise((resolve) => setTimeout(resolve, 100))
}
async function tabTo(predicate) {
  for (let index = 0; index < 160; index += 1) {
    if (await evaluate(`(() => { const a = document.activeElement; return ${predicate} })()`)) return
    await keyPress('Tab')
  }
  throw new Error(`Unable to tab to ${predicate}`)
}
async function activateButton(label) {
  await tabTo(`a.tagName === 'BUTTON' && a.textContent?.trim() === ${JSON.stringify(label)}`)
  await keyPress('Enter')
}
async function capture(name) {
  if (!evidenceDir) return
  const screenshot = await command('Page.captureScreenshot', { format: 'png' })
  await mkdir(evidenceDir, { recursive: true })
  await writeFile(`${evidenceDir}/${name}.png`, Buffer.from(screenshot.data, 'base64'))
}
async function assertContained(label) {
  const layout = await evaluate('({ inner: innerWidth, client: document.documentElement.clientWidth, root: document.documentElement.scrollWidth, body: document.body.scrollWidth })')
  assert.ok(layout.root <= layout.inner + 1, `${label} document overflow: ${JSON.stringify(layout)}`)
  assert.ok(layout.body <= layout.inner + 1, `${label} body overflow: ${JSON.stringify(layout)}`)
  return layout
}

const results = []
try {
  await command('Page.enable')
  await command('Runtime.enable')
  await command('Page.addScriptToEvaluateOnNewDocument', { source: fixture })
  for (const width of [1280, 320]) {
    await command('Emulation.setDeviceMetricsOverride', { width, height: 800, deviceScaleFactor: 1, mobile: false })
    await command('Page.navigate', { url: appURL })
    await waitFor('Boolean(document.querySelector("#holdings-title"))')
    await tabTo('a.getAttribute("role") === "tab"')
    for (let index = 0; index < 6 && !(await evaluate('document.activeElement?.textContent?.trim() === "Reviews"')); index += 1) await keyPress('ArrowRight')
    await keyPress('Enter')
    await waitFor('document.querySelectorAll("[data-review-id]").length === 20')
    assert.equal(await evaluate('document.querySelector("#review-status-filter")?.value'), 'all')
    assert.equal(await evaluate('document.querySelector("#review-lifecycle-filter")?.value'), 'all')

    await tabTo('a.id === "review-status-filter"')
    await keyPress('End')
    await waitFor('document.querySelector("#review-status-filter")?.value === "incomplete"')
    await tabTo('a.id === "review-lifecycle-filter"')
    await keyPress('End')
    await waitFor('document.querySelector("#review-lifecycle-filter")?.value === "true"')
    await waitFor('document.querySelectorAll("[data-review-id]").length === 20')
    assert.equal(await evaluate('document.querySelector("[data-review-id=review-target-lot-b]") !== null'), false)

    await activateButton('Load more reviews')
    await waitFor('Boolean(document.querySelector("[role=alert]"))')
    assert.equal(await evaluate('document.querySelectorAll("[data-review-id]").length'), 20)
    await activateButton('Retry loading reviews')
    await waitFor('Boolean(document.querySelector("[data-review-id=review-target-lot-b]"))')

    await tabTo('a.dataset.reviewId === "review-target-lot-b"')
    await keyPress('Enter')
    await waitFor('Boolean(document.querySelector(".review-detail"))')
    const detailText = await evaluate('document.querySelector(".review-detail")?.textContent || ""')
    assert.ok(detailText.includes('qty 2 · MYR · cost basis 10 MYR · lot lot-b'))
    const detailLayout = await evaluate('(() => { const badge = document.querySelector(".review-detail .state-pill"); const rect = badge?.getBoundingClientRect(); return { inner: innerWidth, client: document.documentElement.clientWidth, root: document.documentElement.scrollWidth, badge: { left: rect?.left, right: rect?.right, text: badge?.textContent } } })()')
    assert.ok(detailLayout.root <= detailLayout.inner + 1, `open detail overflow at ${width}px: ${JSON.stringify(detailLayout)}`)
    assert.ok(detailLayout.badge.right <= detailLayout.inner + 1, `status badge clipped at ${width}px: ${JSON.stringify(detailLayout)}`)
    await capture(`detail-${width}`)

    await tabTo('a.getAttribute("href") === "#review-review-target-lot-b-evidence-evidence-shared"')
    await keyPress('Enter')
    await waitFor('location.hash === "#review-review-target-lot-b-evidence-evidence-shared"')
    const evidence = await evaluate('(() => { const node = document.querySelector("#review-review-target-lot-b-evidence-evidence-shared"); const rect = node?.getBoundingClientRect(); return { text: node?.textContent || "", top: rect?.top, bottom: rect?.bottom, hash: location.hash } })()')
    assert.ok(evidence.text.includes('Publication date: Not supplied'))
    assert.equal(evidence.hash, '#review-review-target-lot-b-evidence-evidence-shared')
    assert.ok(evidence.top < 800 && evidence.bottom > 0)

    await activateButton('Record feedback')
    await waitFor('Boolean(document.querySelector("[role=alert]"))')
    assert.ok((await evaluate('document.querySelector("[role=alert]")?.textContent || ""')).includes('append-only event'))
    assert.equal(await evaluate('Boolean(document.querySelector(".review-detail"))'), true)
    await capture(`evidence-${width}`)
    await activateButton('Close review detail')
    await waitFor('!document.querySelector(".review-detail")')
    assert.equal(await evaluate('document.querySelector("[data-review-id=review-target-lot-b]") !== null'), true)
    const layout = await assertContained(`${width}px review journey`)
    const privateStorage = await evaluate('localStorage.length + sessionStorage.length')
    assert.equal(privateStorage, 0)
    results.push({ width, detailLayout, evidence, layout, privateStorage, nativeKeyboard: true, exactLot: true, feedbackErrorRecovery: true })
  }
  console.log(JSON.stringify(results, null, 2))
} finally {
  socket.close()
}

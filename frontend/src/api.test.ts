import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiFetch } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('typed API helper', () => {
  it('keeps CSRF state out of storage and attaches it only to unsafe requests', async () => {
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(() => Promise.resolve({ ok: true, status: 204 } as Response))
    vi.stubGlobal('fetch', fetchMock)
    await apiFetch('/api/v1/auth/session')
    await apiFetch('/api/v1/test', { method: 'POST', csrfToken: 'memory-only-token', body: '{}' })
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/auth/session', expect.objectContaining({ credentials: 'same-origin', headers: expect.any(Headers) }))
    expect((fetchMock.mock.calls[0][1]?.headers as Headers).get('X-CSRF-Token')).toBeNull()
    expect((fetchMock.mock.calls[1][1]?.headers as Headers).get('X-CSRF-Token')).toBe('memory-only-token')
    expect((globalThis.localStorage?.length ?? 0)).toBe(0)
    expect((globalThis.sessionStorage?.length ?? 0)).toBe(0)
  })

  it('surfaces 401 as an expired-session error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 401 } as Response)))
    await expect(apiFetch('/api/v1/private')).rejects.toMatchObject({ status: 401 })
  })
})

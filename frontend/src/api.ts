export class APIError extends Error {
  status: number

  constructor(status: number, message = `Request failed (${status})`) {
    super(message)
    this.name = 'APIError'
    this.status = status
  }
}

export type APIRequestInit = RequestInit & { csrfToken?: string }

export async function apiFetch(input: RequestInfo | URL, init: APIRequestInit = {}): Promise<Response> {
  const { csrfToken, headers: suppliedHeaders, ...requestInit } = init
  const headers = new Headers(suppliedHeaders)
  if (!headers.has('Accept')) headers.set('Accept', 'application/json')
  const method = (requestInit.method || 'GET').toUpperCase()
  if (csrfToken && method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(input, { ...requestInit, headers, credentials: 'same-origin' })
  if (response.status === 401) throw new APIError(401, 'Your session has expired. Please sign in again.')
  return response
}

export async function jsonResponse<T>(response: Response): Promise<T> {
  if (!response.ok) throw new APIError(response.status)
  return response.json() as Promise<T>
}

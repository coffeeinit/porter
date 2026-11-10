// Shared client for the root Go `/api/v1` operator API.
const BASE = '/api/v1'

export const auth = {
  get token() { return sessionStorage.getItem('porter.token') || '' },
  set token(value) {
    if (value) sessionStorage.setItem('porter.token', value)
    else sessionStorage.removeItem('porter.token')
  },
}

export class ApiError extends Error {
  constructor(message, status, code = '', requestId = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

async function responseData(response) {
  const text = await response.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return { message: text } }
}

function apiError(data, status) {
  const detail = data?.error
  return new ApiError(
    detail?.message || data?.message || `Request failed (${status})`,
    status,
    detail?.code || '',
    detail?.request_id || '',
  )
}

// Verify the operator token against the protected API before saving it for this tab.
export async function login(operatorToken) {
  const token = String(operatorToken || '').trim()
  if (!token) throw new Error('Enter the operator token.')
  const response = await fetch(`${BASE}/projects?page_size=1`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  const data = await responseData(response)
  if (!response.ok) throw apiError(data, response.status)
  auth.token = token
  return true
}

export function logout() {
  auth.token = ''
  window.location.assign('/')
}

export async function api(method, path, body) {
  if (!auth.token) {
    window.location.assign('/')
    throw new Error('Enter the operator token to continue.')
  }
  const headers = { Authorization: `Bearer ${auth.token}` }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const response = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const data = await responseData(response)
  if (response.status === 401) {
    auth.token = ''
    window.location.assign('/')
    throw apiError(data, response.status)
  }
  if (!response.ok) throw apiError(data, response.status)
  return data
}

export const get = (path) => api('GET', path)
export const post = (path, body) => api('POST', path, body ?? {})
export const patch = (path, body) => api('PATCH', path, body)
export const del = (path) => api('DELETE', path)

// Internal agent administration is kept outside the product `/api/v1` routes.
// Enrollment responses contain a one-time secret, so the fetch is never cached.
export async function internalPost(path, body) {
  if (!auth.token) {
    window.location.assign('/')
    throw new Error('Enter the operator token to continue.')
  }
  const response = await fetch('/internal' + path, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${auth.token}`,
      'Content-Type': 'application/json',
      'Cache-Control': 'no-store',
    },
    cache: 'no-store',
    body: JSON.stringify(body ?? {}),
  })
  const data = await responseData(response)
  if (response.status === 401) {
    auth.token = ''
    window.location.assign('/')
    throw apiError(data, response.status)
  }
  if (!response.ok) throw apiError(data, response.status)
  return data
}

// Fetch every bounded keyset page; resource endpoints remain individually capped.
export async function listAll(path, pageSize = 100) {
  const items = []
  const seen = new Set()
  let cursor = ''
  for (let pageNumber = 0; pageNumber < 1000; pageNumber++) {
    const separator = path.includes('?') ? '&' : '?'
    let pagePath = `${path}${separator}page_size=${encodeURIComponent(pageSize)}`
    if (cursor) pagePath += `&cursor=${encodeURIComponent(cursor)}`
    const page = await get(pagePath)
    if (!page || !Array.isArray(page.items)) throw new Error('The API returned an invalid page.')
    items.push(...page.items)
    cursor = page.next_cursor || ''
    if (!cursor) return items
    if (seen.has(cursor)) throw new Error('The API returned a repeated page cursor.')
    seen.add(cursor)
  }
  throw new Error('Too many pages to load safely.')
}

export async function upload(path, formData) {
  if (!auth.token) {
    window.location.assign('/')
    throw new Error('Enter the operator token to continue.')
  }
  const response = await fetch(BASE + path, {
    method: 'POST',
    headers: { Authorization: `Bearer ${auth.token}` },
    body: formData,
  })
  const data = await responseData(response)
  if (response.status === 401) {
    auth.token = ''
    window.location.assign('/')
    throw apiError(data, response.status)
  }
  if (!response.ok) throw apiError(data, response.status)
  return data
}

export function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[character]))
}

export function ago(iso) {
  const timestamp = Date.parse(iso)
  if (!timestamp) return ''
  const seconds = (Date.now() - timestamp) / 1000
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${seconds / 60 | 0}m ago`
  if (seconds < 86400) return `${seconds / 3600 | 0}h ago`
  return `${seconds / 86400 | 0}d ago`
}

const PILL = { running: 'ok', ready: 'ok', healthy: 'ok', active: 'ok', succeeded: 'ok', pending: 'warn', provisioning: 'warn', building: 'warn', degraded: 'warn', checking: 'warn', failed: 'bad', unhealthy: 'bad' }
export function pillClass(state) { return PILL[String(state).toLowerCase()] ?? 'info' }

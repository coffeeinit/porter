// Porter API client — one shared instance for all views.
const BASE = '/api/v1'

export const auth = {
  get token() { return localStorage.getItem('porter.token') || '' },
  set token(v) { v ? localStorage.setItem('porter.token', v) : localStorage.removeItem('porter.token') },
}

export const org = {
  get id() { return localStorage.getItem('porter.org') || '' },
  set id(v) { localStorage.setItem('porter.org', v) },
}

let csrf = ''

export async function login(username, password) {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!r.ok) {
    const d = await r.json().catch(() => ({}))
    throw new Error(d.error || d.message || 'login failed')
  }
  const d = await r.json()
  auth.token = d.token
  try {
    const o = await fetch(`${BASE}/orgs/default`, { headers: { Authorization: `Bearer ${auth.token}` } })
    if (o.ok) org.id = (await o.json()).id
  } catch (_) { /* org optional until first use */ }
  return d.user
}

export function logout() { auth.token = ''; location.href = '/' }

async function ensureCsrf() {
  if (csrf) return csrf
  const r = await fetch(`${BASE}/csrf`, { headers: { Authorization: `Bearer ${auth.token}` } })
  if (r.ok) csrf = (await r.json()).csrf_token || ''
  return csrf
}

export async function api(method, path, body) {
  if (!auth.token) { location.href = '/'; throw new Error('unauthenticated') }
  const headers = { Authorization: `Bearer ${auth.token}`, 'X-Porter-Org-Id': org.id }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    await ensureCsrf()
    headers['X-CSRF-Token'] = csrf
  }
  const r = await fetch(BASE + path, { method, headers, body: body !== undefined ? JSON.stringify(body) : undefined })
  if (r.status === 401) { logout(); throw new Error('session expired') }
  const text = await r.text()
  const data = text ? JSON.parse(text) : null
  if (!r.ok) {
    const msg = data && (data.error || data.message) ? `${data.error || ''} ${data.message || ''}`.trim() : `HTTP ${r.status}`
    throw new Error(msg)
  }
  return data
}

export const get = (p) => api('GET', p)
export const post = (p, b) => api('POST', p, b ?? {})
export const patch = (p, b) => api('PATCH', p, b)
export const del = (p) => api('DELETE', p)

export async function upload(path, formData) {
  if (!auth.token) { location.href = '/'; throw new Error('unauthenticated') }
  const headers = { Authorization: `Bearer ${auth.token}`, 'X-Porter-Org-Id': org.id }
  await ensureCsrf()
  headers['X-CSRF-Token'] = csrf
  const r = await fetch(BASE + path, { method: 'POST', headers, body: formData })
  if (r.status === 401) { logout(); throw new Error('session expired') }
  const text = await r.text()
  const data = text ? JSON.parse(text) : null
  if (!r.ok) throw new Error(data && (data.error || data.message) ? `${data.error || ''} ${data.message || ''}`.trim() : `HTTP ${r.status}`)
  return data
}

export function esc(s) { return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])) }
export function ago(iso) {
  const t = Date.parse(iso); if (!t) return ''
  const s = (Date.now() - t) / 1000
  if (s < 60) return 'just now'
  if (s < 3600) return `${s / 60 | 0}m ago`
  if (s < 86400) return `${s / 3600 | 0}h ago`
  return `${s / 86400 | 0}d ago`
}
const PILL = { running: 'ok', ready: 'ok', healthy: 'ok', active: 'ok', succeeded: 'ok', pending: 'warn', provisioning: 'warn', building: 'warn', degraded: 'warn', checking: 'warn', failed: 'bad', unhealthy: 'bad' }
export function pillClass(state) { return PILL[String(state).toLowerCase()] ?? 'info' }

const API = '/api'
const ADMIN_TOKEN_KEY = 'http-proxy-admin-token'
const ADMIN_TOKEN_HEADER = 'X-Admin-Token'

let adminToken = ''

try {
  adminToken = sessionStorage.getItem(ADMIN_TOKEN_KEY) || ''
} catch {
  adminToken = ''
}

export function getAdminToken() {
  return adminToken
}

export function setAdminToken(token) {
  adminToken = (token || '').trim()
  try {
    if (adminToken) {
      sessionStorage.setItem(ADMIN_TOKEN_KEY, adminToken)
    } else {
      sessionStorage.removeItem(ADMIN_TOKEN_KEY)
    }
  } catch {
    // Ignore unavailable session storage.
  }
}

async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) }
  if (options.body && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json'
  }
  if (adminToken) {
    headers[ADMIN_TOKEN_HEADER] = adminToken
  }

  const response = await fetch(`${API}${path}`, { ...options, headers })
  const text = await response.text()
  const data = text ? JSON.parse(text) : null

  if (!response.ok) {
    const error = new Error(data?.error || data?.message || `HTTP ${response.status}`)
    error.status = response.status
    error.data = data
    throw error
  }
  return data
}

export function listSubscriptions() {
  return request('/subscriptions')
}

export function addSubscription(url) {
  return request('/subscriptions', {
    method: 'POST',
    body: JSON.stringify({ url })
  })
}

export function deleteSubscription(id) {
  return request(`/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function refreshSubscription(id) {
  return request(`/subscriptions/${encodeURIComponent(id)}/refresh`, { method: 'POST' })
}

export function listProxies() {
  return request('/proxies')
}

export function testProxy(tag) {
  return request(`/proxies/${encodeURIComponent(tag)}/test`, { method: 'POST' })
}

export function getAvailableProxies(count, token) {
  const params = new URLSearchParams({ count: String(count) })
  if (token) {
    params.set('token', token)
  }
  return request(`/proxies/available?${params.toString()}`)
}

export function getPoolStatus() {
  return request('/pool/status')
}

export function getAvailableStatus() {
  return request('/pool/available/status')
}

export function stopPool() {
  return request('/pool/stop', { method: 'POST' })
}

export function getConfig() {
  return request('/config')
}

export function saveConfig(config) {
  return request('/config', {
    method: 'PUT',
    body: JSON.stringify(config)
  })
}

export function testUpstream(upstreamProxy, testTarget) {
  return request('/config/upstream/test', {
    method: 'POST',
    body: JSON.stringify({
      upstream_proxy: upstreamProxy,
      test_target: testTarget
    })
  })
}

export function listLogs(limit = 200) {
  return request(`/logs?limit=${encodeURIComponent(limit)}`)
}

export function clearLogs() {
  return request('/logs/clear', { method: 'POST' })
}

export function listRequestLogDates() {
  return request('/request-logs/dates')
}

export function listRequestLogs(date = '', limit = 200) {
  const params = new URLSearchParams({ limit: String(limit) })
  if (date) {
    params.set('date', date)
  }
  return request(`/request-logs?${params.toString()}`)
}

export function clearRequestLogs(date = '') {
  const params = new URLSearchParams()
  if (date) {
    params.set('date', date)
  }
  const suffix = params.toString()
  return request(`/request-logs${suffix ? `?${suffix}` : ''}`, { method: 'DELETE' })
}

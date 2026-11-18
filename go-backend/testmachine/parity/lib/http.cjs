'use strict'

async function requestJSON(baseUrl, route, {
  pathPrefix = '', method = 'GET', token, body, headers = {}, timeoutMs, throwOnError = false,
  errorLabel = route, signal, redirect = 'follow', bodyIsRaw = false, jsonContentType = true
} = {}) {
  const response = await fetch(`${baseUrl}${pathPrefix}${route}`, {
    method,
    redirect,
    headers: {
      ...(jsonContentType && body !== undefined && !(body instanceof FormData) && !bodyIsRaw ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers
    },
    body: body === undefined ? undefined : body instanceof FormData || bodyIsRaw ? body : JSON.stringify(body),
    signal: signal || (timeoutMs ? AbortSignal.timeout(timeoutMs) : undefined)
  })
  const bytes = Buffer.from(await response.arrayBuffer())
  const text = bytes.toString('utf8')
  let value
  try { value = JSON.parse(text) } catch { value = text || null }
  const result = { status: response.status, ok: response.ok, value, bytes, headers: response.headers }
  if (!response.ok && throwOnError) throw new Error(`${method} ${errorLabel} -> ${response.status}: ${JSON.stringify(value)}`)
  return result
}

function createRequester({
  pathPrefix = '', throwOnError = false, resultKey = 'value', valueOnly = false,
  includeHeaders = true, omitFalsyBody = false
} = {}) {
  return async (baseUrl, route, options = {}) => {
    const response = await requestJSON(baseUrl, route, {
      pathPrefix,
      method: options.method || 'GET',
      token: options.token,
      ...(includeHeaders ? { headers: options.headers || {} } : {}),
      body: omitFalsyBody ? options.body || undefined : options.body,
      throwOnError
    })
    return valueOnly ? response.value : { status: response.status, [resultKey]: response.value }
  }
}

async function login(baseUrl, credentials, {
  pathPrefix = '', headers = {}, timeoutMs, tokenRequired = true,
  returnTokensHeader = true, tokenSelector = body => body?.user?.accessToken,
  errorMessage = (status, value) => `login failed ${status}: ${JSON.stringify(value)}`
} = {}) {
  const result = await requestJSON(baseUrl, '/login', {
    pathPrefix, method: 'POST', body: credentials,
    headers: { ...(returnTokensHeader ? { 'X-Return-Tokens': 'true' } : {}), ...headers }, timeoutMs
  })
  const token = tokenSelector(result.value)
  if ((!result.ok || (tokenRequired && !token))) {
    throw new Error(errorMessage(result.status, result.value))
  }
  return { ...result, token }
}

module.exports = { requestJSON, createRequester, login }

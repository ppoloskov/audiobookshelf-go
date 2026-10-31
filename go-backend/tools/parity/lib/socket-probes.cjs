'use strict'

const path = require('node:path')

function loadSocketIOClient() {
  try {
    const socketIoClient = require('socket.io-client')
    return socketIoClient.io || socketIoClient
  } catch (error) {
    const candidatePaths = [
      process.env.ABS_PARITY_SOCKETIO_PATH,
      path.resolve(__dirname, '../../../../client/node_modules/socket.io-client'),
      '/usr/lib/node_modules/socket.io-client'
    ].filter(Boolean)
    for (const candidate of candidatePaths) {
      try {
        const resolved = require(candidate)
        return resolved.io || resolved
      } catch { /* try next */ }
    }
    if (process.env.ABS_PARITY_REQUIRE_SOCKETIO === '1') throw new Error(`Socket.IO JS client is required but unavailable: ${error.message}`)
    return null
  }
}

function socketNotificationProbe(io, server, socketPath, token, route, call) {
  return new Promise((resolve, reject) => {
    const socket = io(server.url, { path: socketPath, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
    let settled = false
    const received = []
    const finish = (fn, value) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      socket.disconnect()
      fn(value)
    }
    const timer = setTimeout(() => finish(reject, new Error('notification socket probe timed out')), 12000)
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('auth_failed', payload => finish(reject, new Error(`notification socket auth failed: ${JSON.stringify(payload)}`)))
    socket.once('connect_error', error => finish(reject, error))
    socket.once('init', async () => {
      socket.on('notifications_updated', payload => received.push(payload))
      try {
        const response = await call(server, route, { token })
        const deadline = Date.now() + 2500
        while (received.length === 0 && Date.now() < deadline) await new Promise(resolveWait => setTimeout(resolveWait, 25))
        finish(resolve, { response, events: received })
      } catch (error) {
        finish(reject, error)
      }
    })
  })
}

function socketAuthentication(io, url, socketPath, token) {
  return new Promise((resolve) => {
    const socket = io(url, {
      path: socketPath,
      timeout: 8000,
      reconnection: false,
      transports: ['polling', 'websocket']
    })
    let finished = false
    let connected = false
    const events = []
    const finish = (status, body) => {
      if (finished) return
      finished = true
      clearTimeout(timer)
      socket.disconnect()
      resolve({ status, headers: {}, body })
    }
    const timer = setTimeout(() => finish(599, {
      error: connected ? 'Socket.IO authentication timed out' : 'Socket.IO connection timed out',
      connected,
      events
    }), 10000)
    socket.once('connect', () => {
      connected = true
      events.push('connect')
      socket.emit('auth', token)
    })
    socket.once('init', (payload) => finish(200, payload))
    socket.once('auth_failed', (payload) => finish(401, payload))
    socket.once('disconnect', reason => events.push(`disconnect:${reason}`))
    socket.once('connect_error', (error) => finish(599, { error: error.message, connected, events }))
  })
}

function socketUserUpdatedProbe(io, url, socketPath, { readerToken, rootToken, progressRoute, expectedUsername }) {
  const openSocket = (token) => new Promise((resolve, reject) => {
    const socket = io(url, { path: socketPath, timeout: 8000, reconnection: false, transports: ['polling', 'websocket'] })
    let settled = false
    const settle = (fn, value) => { if (!settled) { settled = true; fn(value) } }
    socket.once('connect', () => socket.emit('auth', token))
    socket.once('init', (payload) => settle(resolve, { socket, payload }))
    socket.once('auth_failed', (payload) => settle(reject, new Error(`auth_failed ${JSON.stringify(payload).slice(0, 100)}`)))
    socket.once('connect_error', (error) => settle(reject, new Error(`connect_error ${error.message}`)))
    setTimeout(() => settle(reject, new Error('socket auth timeout')), 10000)
  })
  return (async () => {
    const reader = await openSocket(readerToken)
    const root = await openSocket(rootToken)
    const received = { reader: null, root: null }
    reader.socket.once('user_updated', (payload) => { received.reader = payload })
    root.socket.once('user_updated', (payload) => { received.root = payload })
    const patch = await fetch(`${url}${progressRoute}`, {
      method: 'PATCH',
      signal: AbortSignal.timeout(15000),
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${readerToken}` },
      body: JSON.stringify({ currentTime: 1.23, duration: 2, progress: 0.6 })
    })
    if (patch.status !== 200) throw new Error(`progress patch returned ${patch.status}`)
    const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const deadline = Date.now() + 8000
    while (received.reader === null && Date.now() < deadline) await wait(100)
    await wait(2000) // grace window to detect a forbidden root delivery
    const result = {
      status: 200,
      body: {
        username: received.reader?.username ?? null,
        mediaProgressCount: Array.isArray(received.reader?.mediaProgress) ? received.reader.mediaProgress.length : null,
        rootReceived: received.root !== null
      }
    }
    reader.socket.disconnect()
    root.socket.disconnect()
    return result
  })().catch((error) => ({ status: 599, body: { error: error.message } }))
}

module.exports = { loadSocketIOClient, socketNotificationProbe, socketAuthentication, socketUserUpdatedProbe }

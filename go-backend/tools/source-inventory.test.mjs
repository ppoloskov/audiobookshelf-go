import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { buildInventory, extractRoutes, extractSocketEvents, extractModels } from './source-inventory.mjs'

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-source-inventory-'))
  t.after(() => fs.rmSync(root, { recursive: true, force: true }))
  const write = (relative, source) => {
    const file = path.join(root, relative)
    fs.mkdirSync(path.dirname(file), { recursive: true })
    fs.writeFileSync(file, source)
  }
  return { root, write }
}

test('routes retain literal, multiline, regex and unresolved syntax', t => {
  const { root, write } = fixture(t)
  write('server/routers/ApiRouter.js', "const express = require('express')\nconst router = express()\nrouter.get('/books/:id', auth, controller.show)\nthis.router.patch(\n  `/users/${userType}`,\n  middleware.one,\n  middleware.two,\n  handler\n)\nrouter.get(/^\\/internal/, handler)\nrouter.post(pathFromConfig, handler)\n")
  const [routes, sources] = extractRoutes(root)
  assert.deepEqual(routes.map(route => route.method), ['GET', 'PATCH', 'GET', 'POST'])
  assert.equal(routes[0].route, '/books/:id')
  assert.equal(routes[0].route_kind, 'string_literal')
  assert.equal(routes[0].line, 3)
  assert.deepEqual(routes.slice(1).map(route => route.route_kind), ['template_expression', 'regex_literal', 'dynamic_expression'])
  assert.equal(routes[0].middleware_behavior_resolved, false)
  assert.equal(routes[0].prefix_context.known_mount, '/api')
  assert.ok(sources['server/routers/ApiRouter.js'])
})

test('socket registrations and emissions retain dynamic names', t => {
  const { root, write } = fixture(t)
  write('server/SocketAuthority.js', "socket.on('auth', handler)\nsocket.on(eventName, handler)\nclient.socket.emit('init', payload)\nio.emit(`room_${roomId}`, payload)\n")
  const [events] = extractSocketEvents(root)
  assert.deepEqual(events.map(event => event.direction), ['incoming_registration', 'incoming_registration', 'emission', 'emission'])
  assert.equal(events[0].event, 'auth')
  assert.deepEqual(events.map(event => event.event_kind), ['string_literal', 'dynamic_expression', 'string_literal', 'template_expression'])
  assert.equal(events[0].scope_resolved, false)
})

test('known wrapper signatures select the correct event argument', t => {
  const { root, write } = fixture(t)
  write('server/managers/SocketCalls.js', "SocketAuthority.emitter('global_update', makePayload(a, b))\nSocketAuthority.clientEmitter(userId, 'private_update', { nested: [1, 2] })\nSocketAuthority.adminEmitter(eventName, payload)\nSocketAuthority.libraryItemEmitter(`item_${kind}`, item)\nSocketAuthority.libraryItemsEmitter('items_updated', items)\n")
  write('server/SocketAuthority.js', "this.clientEmitter(user.id, 'nested_private_update', payload)\n")
  const [events] = extractSocketEvents(root)
  assert.deepEqual(events.map(event => [event.wrapper, event.event]), [
    ['clientEmitter', 'nested_private_update'], ['emitter', 'global_update'], ['clientEmitter', 'private_update'],
    ['adminEmitter', 'eventName'], ['libraryItemEmitter', '`item_${kind}`'], ['libraryItemsEmitter', 'items_updated']
  ])
  assert.equal(events[0].wrapper_event_argument_index, 1)
  assert.equal(events[0].scope_classification, 'specific_user_clients')
  assert.equal(events[3].event_kind, 'dynamic_expression')
  assert.equal(events[4].event_kind, 'template_expression')
  assert.equal(events[0].scope_resolved, false)
})

test('models identify source files and content hashes', t => {
  const { root, write } = fixture(t)
  write('server/models/Book.js', 'module.exports = Book\n')
  write('server/models/User.js', 'module.exports = User\n')
  const [models, sources] = extractModels(root)
  assert.deepEqual(models.map(model => model.filename), ['Book.js', 'User.js'])
  assert.equal(models[0].source, 'server/models/Book.js')
  assert.match(sources['server/models/Book.js'].sha256, /^[0-9a-f]{64}$/)
})

test('machine-readable inventory explicitly disclaims behavioral completeness', t => {
  const { root, write } = fixture(t)
  write('server/routers/PublicRouter.js', "router.get('/share/:slug', handler)\n")
  write('server/models/Book.js', '// model\n')
  const inventory = buildInventory(root)
  assert.equal(inventory.counts.routes, 1)
  assert.equal(inventory.counts.model_files, 1)
  assert.equal(inventory.inventory_kind, 'conservative_source_syntax_inventory')
  assert.ok(inventory.limitations.some(limitation => limitation.includes('not a complete API')))
})

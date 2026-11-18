import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { ROOT, loadCapabilities, reconcile, render, validateRefs, writeOrCheck } from './replacement-contract-map.mjs'
import { buildInventory } from './source-inventory.mjs'

const capabilities = loadCapabilities()
const inventory = buildInventory()

test('current inventory reconciles all source sites and expected counts', () => {
  const [routes, events] = reconcile(capabilities, inventory)
  assert.equal(routes.length, inventory.routes.length)
  assert.equal(events.length, inventory.socket_events.length)
  assert.deepEqual(render().data.counts, { requirements: capabilities.length, invariants: capabilities.flatMap(capability => capability.invariants).length, route_sites: inventory.routes.length, socket_sites: inventory.socket_events.length, socket_names: capabilities.filter(capability => capability.id.startsWith('socket-')).length, models: inventory.model_files.length })
})

test('a new route fails until it has a canonical requirement', () => {
  const changed = structuredClone(inventory)
  changed.routes.push({ ...changed.routes[0], line: 999999 })
  assert.throws(() => reconcile(capabilities, changed), /expected one capability/)
})

test('duplicate inventory route sites fail', () => {
  const changed = structuredClone(inventory)
  changed.routes.push({ ...changed.routes[0] })
  assert.throws(() => reconcile(capabilities, changed), /duplicate route site/)
})

test('corroborating route references do not claim another operation', () => {
  const changed = structuredClone(capabilities)
  changed.find(capability => capability.id === 'route-r001').source.push('server/Auth.js:322')
  const [routes] = reconcile(changed, { ...inventory, socket_events: [] })
  assert.equal(routes.find(route => route.source === 'server/Auth.js' && route.line === 322).capability, 'route-r000')
  assert.equal(routes.find(route => route.source === 'server/routers/ApiRouter.js' && route.line === 75).capability, 'route-r022')
})

test('duplicate operation claims still fail', () => {
  const changed = structuredClone(capabilities)
  const duplicate = structuredClone(changed.find(capability => capability.id === 'route-r000'))
  duplicate.id = 'route-duplicate'
  changed.push(duplicate)
  assert.throws(() => reconcile(changed, inventory), /expected one capability/)
})

test('a lone corroborating citation cannot claim an unrelated route', () => {
  const changed = structuredClone(capabilities).filter(capability => capability.id !== 'route-r000')
  for (const capability of changed.filter(capability => capability.id.startsWith('route-') && capability.id !== 'route-r022')) {
    capability.source = capability.source.filter(ref => ref !== 'server/Auth.js:322')
  }
  assert.throws(() => reconcile(changed, inventory), /route site server\/Auth.js:322: expected one capability/)
})

test('route path matching rejects arbitrary suffixes', () => {
  const changed = structuredClone(capabilities)
  const login = changed.find(capability => capability.id === 'route-r000')
  login.contract = login.contract.replace('POST /login', 'POST /other/login')
  assert.throws(() => reconcile(changed, inventory), /route site server\/Auth.js:322: expected one capability/)
})

test('namespace aliases and the final catch-all resolve explicitly', () => {
  const [routes] = reconcile(capabilities, inventory)
  for (const [source, line, id] of [
    ['server/routers/ApiRouter.js', 75, 'route-r022'],
    ['server/routers/ApiRouter.js', 200, 'route-r122'],
    ['server/routers/HlsRouter.js', 21, 'route-r219'],
    ['server/routers/PublicRouter.js', 16, 'route-r220'],
    ['server/Server.js', 452, 'route-r015']
  ]) assert.equal(routes.find(route => route.source === source && route.line === line).capability, id)
  const changed = structuredClone(capabilities)
  changed.find(capability => capability.id === 'route-r015').contract = 'Final GET * catch-all'
  assert.throws(() => reconcile(changed, inventory), /route site server\/Server.js:452: expected one capability/)
})

test('one requirement can own multiple matching registration sites', () => {
  const changed = structuredClone(capabilities)
  const added = structuredClone(inventory)
  added.socket_events = []
  const original = added.routes.find(route => route.source === 'server/Auth.js' && route.line === 322)
  added.routes.push({ ...original, line: 323 })
  changed.find(capability => capability.id === 'route-r000').source.push('server/Auth.js:323')
  const [routes] = reconcile(changed, added)
  assert.equal(routes.find(route => route.source === 'server/Auth.js' && route.line === 323).capability, 'route-r000')
})

test('a new event fails until it has a canonical requirement', () => {
  const changed = structuredClone(inventory)
  changed.socket_events.push({ ...changed.socket_events[0], event: 'new_contract_event', event_kind: 'string_literal' })
  assert.throws(() => reconcile(capabilities, changed), /unregistered event/)
})

test('event evidence must cover the emission site in the right file', () => {
  const changed = structuredClone(capabilities)
  const event = inventory.socket_events.find(event => event.source === 'server/models/Book.js' && event.line === 471)
  const record = changed.find(capability => capability.id === 'socket-outgoing-author-added')
  record.source = record.source.filter(ref => !ref.startsWith('server/models/Book.js:'))
  record.source.push('server/models/Book.js:466-472')
  assert.doesNotThrow(() => reconcile(changed, inventory))
  record.source[record.source.length - 1] = 'server/models/Book.js:466-470'
  assert.throws(() => reconcile(changed, inventory), /unmapped event site server\/models\/Book.js:471/)
  record.source[record.source.length - 1] = 'server/models/Author.js:466-472'
  assert.throws(() => reconcile(changed, inventory), /unmapped event site server\/models\/Book.js:471/)
  assert.equal(event.event, 'author_added')
})

test('missing test symbols fail the mapping gate', () => {
  const changed = structuredClone(capabilities[0])
  changed.tests = ['go-backend/internal/db/open_test.go#TestDoesNotExist']
  assert.throws(() => validateRefs(changed), /missing test symbol/)
})

test('a production file cannot masquerade as a test', () => {
  const changed = structuredClone(capabilities[0])
  changed.tests = ['go-backend/internal/db/open.go']
  assert.throws(() => validateRefs(changed), /production file used as test/)
})

test('duplicate requirement IDs fail the gate', () => {
  assert.throws(() => reconcile([...capabilities, capabilities[0]], inventory), /duplicate capability/)
})

test('the renderer preserves canonical proof status without granting a product claim', () => {
  const { outputs, data } = render()
  assert.equal(data.claim_permitted, false)
  assert.equal(Object.keys(outputs).length, 4)
  assert.deepEqual(data.requirements.map(row => [row.requirement, row.invariant, row.status]),
    capabilities.flatMap(capability => capability.invariants.map(invariant => [capability.id, invariant.id, invariant.status])))
})

test('a synthetic verified invariant survives rendering without granting global proof', () => {
  const changed = structuredClone(capabilities)
  changed[0].invariants[0].status = 'verified'
  const { data } = render(ROOT, changed)
  assert.equal(data.requirements[0].status, 'verified')
  assert.equal(data.claim_permitted, false)
})

test('check mode rejects a stale output without overwriting it', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'abs-map-stale-'))
  t.after(() => fs.rmSync(root, { recursive: true, force: true }))
  const file = path.join(root, 'view.md')
  fs.writeFileSync(file, 'old\n')
  assert.throws(() => writeOrCheck({ 'view.md': 'new\n' }, root, true), /stale generated view/)
  assert.equal(fs.readFileSync(file, 'utf8'), 'old\n')
})

test('the public CLI check validates checked-in generated views', () => {
  const result = spawnSync(process.execPath, ["go-backend/testmachine/scripts/replacement-contract-map.mjs", "--check"], { cwd: ROOT, encoding: 'utf8' })
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /Mapping structure reconciled/)
})

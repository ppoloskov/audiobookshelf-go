#!/usr/bin/env node
// Render the canonical register; mapping structure is not product verification.
import fs from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawnSync, execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { buildInventory, ROOT, sha256 } from './source-inventory.mjs'
export { ROOT }
export const PIN = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'
const OUT = 'go-backend/docs/verification'
// Authored dispatch boundaries, not completion claims.
export const PACKETS = [
  [
    "tools",
    "Audio task progress and queue lifecycle",
    "internal/tools/",
    "Prove track_started/progress/finished and task_progress with incremental values, exact task data and audiences; immediate work must not fake queue transitions; test waiting/repeat/cancel/failure/shutdown and real output. Reuse media process runner; any media API change needs a separately reviewed ownership exception."
  ],
  [
    "harness",
    "Contract-aware evidence harness",
    "tools/parity/",
    "Reproduce and triage recorded core/replacement/scan/OIDC/boot differences; distinguish client/persistence defects from free representation. Correct fixture and comparator assumptions with rejection self-tests; never normalize away protected fields, statuses, IDs, time units or missing events."
  ],
  [
    "db",
    "Pinned database and row semantics",
    "internal/db/",
    "Open fresh/existing/refused installs; prove schema/row/time/JSON TEXT/null/trigger/link-order semantics and transactional rollback with TS reopening Go mutations. Compare refused-install trees and meaningful row state; explain harmless boot updates rather than demanding byte identity."
  ],
  [
    "settings",
    "Atomic settings and secrets",
    "internal/settings/",
    "Prove partial/null/default handling, concurrent disjoint patches, failed update rollback, deep snapshots, reload callbacks, secure defaults, secret redaction and nonpersisted environment overrides against pinned rows."
  ],
  [
    "auth",
    "Local authentication and permissions",
    "internal/auth/",
    "Characterize login/init/logout/refresh/API authorization, token modes, rotation/replay/grace, disabled/inactive users and immediate revocation; check denied reads/events expose no data and existing client fallback statuses."
  ],
  [
    "oidc",
    "OIDC web and mobile PKCE",
    "internal/auth/oidc*.go",
    "Prove web state/cookie and native PKCE flows, expiry, duplicate/empty/wrong verifier, one-time replay, redirect validation, linking/unlinking and safe errors; triage known 400-vs-302 candidate with actual client decisions."
  ],
  [
    "realtime",
    "Socket transport, auth and dispatch",
    "internal/realtime/",
    "Use a real Socket.IO v4 client with legacy/prefixed websocket paths; prove auth/init/action errors, per-user sockets, reconnect/heartbeat/overflow/shutdown, revocation and filtered audiences. Polling/binary requirements require client evidence and explicit disposition, never a hidden skip."
  ],
  [
    "playback",
    "Session sync, offline replay and media access",
    "internal/playback/",
    "Prove local/local-all with iOS/Android payloads, duplicate and out-of-order replay, seconds/milliseconds, stable IDs, progress conflict rules, session ownership/history/delete, public track ranges and post-close access. Link accepted stream packet scenarios instead of reimplementing its accepted differences."
  ],
  [
    "user",
    "User, progress, bookmarks and API keys",
    "internal/user/",
    "Prove admin/root/owner CRUD, progress and bookmark list/detail/null/delete, stats/session projections, API-key permissions and ereader edits including durable rows and recipient scopes; include GET all-progress/all-bookmarks success."
  ],
  [
    "scan",
    "Scan precedence, identity and lifecycle",
    "internal/scan/",
    "Prove configured metadata precedence including absent/empty/locked, full multi-file chapter offsets/order, repeat/force/move/collision/removal/partial failure/cancel, watchers/cron, derived entities and cache invalidation. TS rereads written metadata.json for books and podcasts."
  ],
  [
    "podcast",
    "Podcast feed, download and schedule",
    "internal/podcast/",
    "Reach successful feed/create/search/check/download paths with local RSS/audio, not denial-only shapes. Prove stable episode IDs, queued/running/clear/failure/stop, retry/auto-disable/scheduling, OPML/match and file/row/event effects."
  ],
  [
    "item",
    "Item metadata, files and deletion",
    "internal/item/",
    "Prove projections and PATCH absent/null/empty/locked fields, batch atomicity, chapters/tracks, cover/file/ebook serving and mutation, quickmatch and destructive cascades; preserve TS-readable metadata/rows and no effects/events on rollback."
  ],
  [
    "library",
    "Library management and aggregates",
    "internal/library/",
    "Prove library CRUD/folder removal/order/settings, access-filtered aggregates/filterdata invalidation, downloads, scan/match dispatch and metadata removal with rollback and watcher effects after commit."
  ],
  [
    "browse",
    "Browse, search and series",
    "internal/browse/",
    "Prove pagination/filter/sort/search/collapse/personalized/series projections across restricted/tag/explicit users, renamed series, empty/invalid queries and stored ordering."
  ],
  [
    "author",
    "Authors and image lifecycle",
    "internal/author/",
    "Prove author update/match/delete and image upload/fetch/cache/remove, linked item updates, permissions and durable metadata; assert affected audiences and rollback rather than just response shape."
  ],
  [
    "list",
    "Collections and playlists",
    "internal/list/",
    "Prove owner/library/permission gates, empty/null values, ordered add/remove/reorder/batch and collection-to-playlist conversion; check empty-list deletion, shared-item cascades, feed closing and rollback silence."
  ],
  [
    "share",
    "Feed and public share consumers",
    "internal/share/",
    "Prove feed item/collection/series open/refresh/XML/cover/enclosure, timezone/pubDate meaning, closing/deletion, share expiry/session/progress/download and capability access; byte ranges and cache headers must support real clients."
  ],
  [
    "backup",
    "Backup archive, retention and apply",
    "internal/backup/",
    "TS reads Go archive layout and database; prove upload/path/settings/schedule/retention, malformed/traversal/symlink/wrong-version rejection before install, and no corruption on failure. Runtime swap itself belongs to server packet."
  ],
  [
    "metadata",
    "Metadata providers and cover search",
    "internal/metadata/",
    "Prove built-in/custom provider query/result/type/order/fallback, CRUD secrets and SSRF protection, websocket cover search result/error/cancel/provider error and no late result after cancellation; use deterministic local providers."
  ],
  [
    "mail",
    "SMTP, MIME and ereader delivery",
    "internal/mail/",
    "Prove settings/defaults/redaction/admin gates, actual local SMTP/TLS/auth/envelope/MIME attachments/filenames, reader device persistence, timeout/cancel and safe error paths; TS/configured clients interpret the message."
  ],
  [
    "notify",
    "Notification delivery and configuration",
    "internal/notify/",
    "Prove subscription/settings CRUD, admitted triggers and no-subscriber edges, local Apprise payload, failure/retry/disable state and notifications_updated full settings; characterize the historical string-enabled fixture without retaining unrelated JS coercion."
  ],
  [
    "admin",
    "Admin operations and operational settings",
    "internal/admin/",
    "Prove upload/filesystem/path/cache/tag/genre/cron/stats/settings routes including authorization, validation, traversal, atomic affected-row updates and downstream invalidation; no silent coercion or protected details."
  ],
  [
    "httpx",
    "Ranges, MIME and conditional caching",
    "internal/httpx/",
    "Characterize shared file/JSON behavior plus actual public/item/playback/download entrypoints: suffix/open/multiple/unsatisfiable 206/416, correct bytes/Content-Range/MIME/disposition, ETag/304 after writes and denied/revoked access. Do not freeze representation algorithms."
  ],
  [
    "text",
    "Observable text and ordering",
    "internal/text/",
    "Check Node-derived natural/locale/name/prefix fixtures and representative real caller ordering; retain only client-visible/persisted semantics, include Unicode/numeric edge cases and safe rich-text handling."
  ],
  [
    "logging",
    "Persisted logs and subscribers",
    "internal/logging/",
    "TS reads Go daily/crash logs; verify line shape/timezone, retention/midnight/reopen, level filtering and per-socket subscribe/unsubscribe without secret leakage."
  ],
  [
    "config",
    "Runtime configuration and launch",
    "internal/config/",
    "Prove flag/environment precedence, supported config/metadata/tool/client paths and invalid input; operate a CGO=0 build without Node, retaining external FFmpeg and assets."
  ],
  [
    "server",
    "Runtime graph, restore and HTTP pipeline",
    "internal/server/",
    "Prove cold boot/shutdown/restart/restore success and rollback under requests/jobs, same-socket backup_applied argument semantics, CORS/basepath/public exceptions/cache isolation/static SPA; verify healthy media has no short blanket deadline."
  ],
  [
    "client",
    "Configured web and native acceptance",
    "internal/contracttest/",
    "Run unchanged bundled web login/catalog/detail/playback with valid root/prefixed builds; exercise every source-audited native protocol workflow and retain device evidence for iOS/Android plus documented client profiles. Matching shared failures or source-audit confirmation are not successful workflows."
  ],
  [
    "closure",
    "Independent contract closure audit",
    "docs/verification/",
    "Re-scan source, client consumers, DB/models/config/files/jobs and requirement register; all cases/invariants/state variants must have current positive and failure-seeking evidence. Run all machine/rollback/client gates and strict ledger. An unresolved required skip, missing assertion or stale evidence blocks the final claim."
  ]
]
const queueHead = "# Ordered replacement-contract gap queue\n\nGenerated with the requirement mapping; status lives in capability/task records.\nOne accountable owner and one bounded packet per invocation. Q00 is this mapping\npacket. Architect acceptance is required after Q00 and between every later packet;\nno worker may dispatch its successor. This queue authorizes preparation within the\nexisting contract, not deployment, dependencies or compatibility exceptions.\n\nAt dispatch the architect creates a canonical task, pins base+approved overlay,\nnames exact files (the directory below is only a maximum ownership boundary), and\nselects a finishable subset of assigned invariant IDs. Budgets: 45 minutes for\ncharacterization, then a 90-minute implementation/verification checkpoint; at most\ntwo unsuccessful corrections of one failure. These are planning limits, not model\nor token limits. If the owner's family cannot fit, split it into sequential packets\nbefore dispatch, retaining every ID and dependency. No unreviewed partial pass.\n\nAll packets depend on acceptance of the previous packet in this order; Q03 onward\nalso depend on Q02's usable contract-aware harness. Multi-producer event records are accountable integration obligations: owners may\nadd contracttest assertions, but later producer requirements remain open until their\nown packets are accepted. Cross-owner defects discovered\nin a test-only integration packet get a new bounded owner task before resuming;\nnever silently expand production write scope. Architect alone updates register,\nREADME, AGENTS, ROADMAP and handover. Workers may write only dispatched production\nfiles, their tests and a named deterministic harness/evidence path. Shared media,\nsettings, server or event helpers require explicit ownership in the brief.\n\nEvery packet must: read its whole package + source/client references; derive\nnormal/error/edge and initial/repeat/changed/force/failure or justify N/A; add\nfailure-seeking source/client-derived tests first; demonstrate red where a bug is\nidentified; implement the smallest fix; run affected tests/race plus required repo\nchecks; provide current source/binary/corpus hashes and N compared/M mismatched/S\nskipped; obtain separate contract and simplicity architect reviews. No production\nbehavior change is authorized solely because a generic map row suggests it.\n\n"
const queueFoot = "## Final claim gate\n\nOnly after the independent final inventory audit and all packets are accepted:\n\n1. Every in-scope operation, event, model/format and lifecycle obligation maps to\n   passing source/client-derived tests for each required field/status/audience,\n   error/edge and repeat-state invariant. No generic sampled invariant substitutes\n   for missing branches. All exclusions/differences have explicit owner authority.\n2. Evidence names the shipping commit and approved overlay, source pin, corpus,\n   binary/input hashes, exact commands/exit statuses and durable observations;\n   zero unresolved mismatches or required skips. Different representations may be\n   compared by meaning only with the actual contract rationale recorded.\n3. Go formatting, vet, complete tests, configured lint, affected race suites,\n   CGO=0 native/Linux amd64/arm64 builds, real media, fresh/existing/refused boot,\n   TS reopens Go-written database/files/backup, restore failure, runtime lifecycle\n   and required web/native protocol/device acceptance all pass on that source.\n4. Renderer `--check`, pinned ledger normal validation, then\n   `go run /Users/paul/.agents/skills/go-scope-ledger/scripts/check-ledger.go\n   -tasks docs/agent-work/tasks -capabilities docs/agent-work/capabilities\n   -require-verified -require-invariants` pass. Structural success alone proves\n   neither inventory completeness nor test behavior; architect checks both.\n\nThen, and only then, publish: **“All requirements of the defined v2.37.2 replacement\ncontract are verified.”** Until then report individual accepted packet evidence\nand the remaining queue. This is not authorization for live deployment.\n"

function eventOwner(name, direction) {
  if (direction === 'incoming' || ['admin_message', 'auth_failed', 'init', 'pong', 'user_online', 'user_offline'].includes(name)) return 'realtime'
  for (const [prefix, owner] of [['stream_', 'playback'], ['track_', 'tools'], ['task_', 'tools'], ['metadata_embed_', 'tools'], ['episode_', 'podcast'], ['author', 'author'], ['series_', 'browse'], ['item', 'item'], ['library_', 'library'], ['collection_', 'list'], ['playlist_', 'list'], ['rss_', 'share'], ['share_', 'share'], ['custom_metadata_', 'metadata'], ['cover_search_', 'metadata']]) {
    if (name.startsWith(prefix)) return owner
  }
  const owners = { backup_applied: 'server', notifications_updated: 'notify', log: 'logging', 'ereader-devices-updated': 'user', batch_quickmatch_complete: 'item', user_item_progress_updated: 'user', user_session_closed: 'playback', user_stream_update: 'playback', user_added: 'user', user_removed: 'user', user_updated: 'user' }
  if (!(name in owners)) throw new Error(`unowned event: ${name}`)
  return owners[name]
}

export function owner(capability) {
  const id = capability.id
  if (id.startsWith('socket-')) {
    const match = /^socket-(incoming|outgoing)-(.*)$/.exec(id)
    if (!match) throw new Error(`invalid socket requirement: ${id}`)
    const [, direction, name] = match
    return eventOwner(name === 'ereader-devices-updated' ? name : name.replaceAll('-', '_'), direction)
  }
  if (id.includes('oidc') || ['route-r002', 'route-r003', 'route-r004', 'route-r005'].includes(id)) return 'oidc'
  const exact = { 'sub-downloads': 'item', 'sub-authorization-ownership-w5': 'auth', 'sub-destructive-cascades-w6': 'item', 'sub-static-client': 'client', 'runtime-binary': 'config', 'client-device-acceptance': 'client', 'cap-flow-backup-restore-swap': 'server', 'cap-event-backup-applied': 'server', 'cap-flow-settings-atomicity': 'settings', 'flow-client-mobile-protocol': 'httpx' }
  if (id in exact) return exact[id]
  if (capability.parent === 'http/user' && capability.implementation.some(ref => ref.includes('/internal/list/'))) return 'list'
  const parent = capability.parent.replace(/^http\//, '')
  return ({ database: 'db', runtime: 'server', adminops: 'item' })[parent] || parent
}

const basePath = ref => ref.split(/[:# (]/, 1)[0].replace(/\/$/, '')
const escapeRegex = text => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
const coversSite = (refs, file, line) => refs.some(ref => {
  const match = ref.match(/^(.*):(\d+)(?:-(\d+))?$/)
  return match && match[1] === file && Number(match[2]) <= line && line <= Number(match[3] || match[2])
})

// Route requirements name either the router-local path or its known mount path.
const routeMounts = {
  'server/routers/ApiRouter.js': '/api',
  'server/routers/HlsRouter.js': '/hls',
  'server/routers/PublicRouter.js': '/public'
}
const namesRoute = (capability, route) => {
  if (route.method === 'ALL' && route.route === '*') return /^Final ALL \* catch-all\b/.test(capability.contract)
  const match = capability.contract.match(/^(\S+)\s+(\S+)/)
  if (!match || match[1] !== route.method) return false
  const declared = match[2].replace(/\.$/, '')
  const mount = routeMounts[route.source]
  return declared === route.route || (mount && declared === mount + route.route)
}

export function validateRefs(capability, root = ROOT) {
  for (const obj of [capability, ...capability.invariants]) {
    for (const kind of ['source', 'tests', ...(obj === capability ? ['implementation'] : [])]) {
      for (const ref of obj[kind] || []) {
        const file = path.join(root, basePath(ref))
        if (!fs.existsSync(file)) throw new Error(`${capability.id}: missing ${kind} reference ${ref}`)
        if (kind === 'tests' && file.endsWith('.go') && !file.endsWith('_test.go')) throw new Error(`${capability.id}: production file used as test ${ref}`)
        if (kind === 'tests' && ref.includes('#') && file.endsWith('.go')) {
          const name = ref.split('#')[1].split('/')[0]
          if (!new RegExp(`^func ${escapeRegex(name)}\\(`, 'm').test(fs.readFileSync(file, 'utf8'))) throw new Error(`${capability.id}: missing test symbol ${ref}`)
        }
      }
    }
  }
}

export function reconcile(capabilities, inventory, root = ROOT) {
  const byID = new Map(capabilities.map(capability => [capability.id, capability]))
  if (byID.size !== capabilities.length) throw new Error('duplicate capability IDs')
  const bySite = new Map()
  const owners = new Set(PACKETS.map(packet => packet[0]))
  for (const capability of capabilities) {
    validateRefs(capability, root)
    if (!capability.invariants?.length || !('stateful' in capability)) throw new Error(`${capability.id}: missing invariants/state classification`)
    if (capability.stateful && Object.keys(capability.state_scenarios || {}).sort().join(',') !== 'changed,failure,force,initial,repeat') throw new Error(`${capability.id}: incomplete state scenarios`)
    if (!owners.has(owner(capability))) throw new Error(`${capability.id}: unowned`)
    if (capability.id.startsWith('route-')) {
      for (const ref of capability.source) {
        const site = ref.replace(/:(\d+)-\d+$/, ':$1')
        if (!bySite.has(site)) bySite.set(site, new Set())
        bySite.get(site).add(capability.id)
      }
    }
  }
  const seenSites = new Set()
  const routes = inventory.routes.map(route => {
    const ref = `${route.source}:${route.line}`
    if (seenSites.has(ref)) throw new Error(`duplicate route site ${ref}`)
    seenSites.add(ref)
    let matches = [...(bySite.get(ref) || [])]
    // A source reference can be corroboration, even when it is the only one.
    if (route.route_kind === 'string_literal') matches = matches.filter(id => namesRoute(byID.get(id), route))
    if (matches.length !== 1) throw new Error(`route site ${ref}: expected one capability, got ${JSON.stringify(matches)}`)
    return { ...route, capability: matches[0] }
  })
  const sites = inventory.socket_events.map(event => {
    const ref = `${event.source}:${event.line}`
    if (!['string_literal', 'template_literal'].includes(event.event_kind)) {
      if (!['server/SocketAuthority.js', 'server/objects/Stream.js'].includes(event.source)) throw new Error(`unresolved dynamic event ${ref}`)
      return { ...event, resolution: 'forwarding helper; literal callers registered separately' }
    }
    const direction = event.direction === 'incoming_registration' ? 'incoming' : 'outgoing'
    const id = `socket-${direction}-${event.event.replaceAll('_', '-')}`
    const capability = byID.get(id)
    if (!capability) throw new Error(`unregistered event ${ref} ${id}`)
    if (!coversSite(capability.source, event.source, event.line)) throw new Error(`unmapped event site ${ref}`)
    return { ...event, capability: id }
  })
  const source = fs.readFileSync(path.join(root, 'server/objects/Stream.js'), 'utf8')
  for (const match of source.matchAll(/this\.clientEmit\('([^']+)'/g)) {
    const id = `socket-outgoing-${match[1].replaceAll('_', '-')}`
    const ref = `server/objects/Stream.js:${source.slice(0, match.index).split('\n').length}`
    const line = source.slice(0, match.index).split('\n').length
    if (!coversSite(byID.get(id)?.source || [], 'server/objects/Stream.js', line)) throw new Error(`unmapped stream caller ${ref}`)
  }
  return [routes, sites]
}

export function capabilityPaths(root = ROOT) {
  const dir = path.join(root, 'docs/agent-work/capabilities')
  return fs.readdirSync(dir).filter(file => file.endsWith('.json')).sort().map(file => path.join(dir, file))
}

export function loadCapabilities(root = ROOT) {
  return capabilityPaths(root).map(file => JSON.parse(fs.readFileSync(file, 'utf8')))
}

// Match the existing checked-in JSON/CSV representation without retaining a
// Python runtime. This preserves evidence diffs while changing the tool stack.
function jsonText(value) {
  return JSON.stringify(value, null, 2).replace(/[\u007f-\uffff]/g, ch => `\\u${ch.charCodeAt(0).toString(16).padStart(4, '0')}`) + '\n'
}
function csvCell(value) {
  const text = typeof value === 'boolean' ? (value ? 'True' : 'False') : String(value)
  return /[",\r\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text
}
const csvRow = values => values.map(csvCell).join(',') + '\n'
const compactObject = object => '{' + Object.keys(object).sort().map(key => `${JSON.stringify(key)}: ${JSON.stringify(object[key])}`).join(', ') + '}'

export function render(root = ROOT, capabilities = loadCapabilities(root)) {
  const paths = capabilityPaths(root)
  const cs = capabilities
  const files = Object.fromEntries(paths.map(file => [JSON.parse(fs.readFileSync(file, 'utf8')).id, path.relative(root, file).split(path.sep).join('/')]))
  const drift = spawnSync('git', ['diff', '--exit-code', PIN, '--', 'server', 'client', 'index.js'], { cwd: root })
  if (drift.error || drift.status !== 0) throw new Error('pinned source/client tree differs or source pin is unavailable')
  const inventory = buildInventory(root)
  const [routes, events] = reconcile(cs, inventory, root)
  const packetIDs = Object.fromEntries(PACKETS.map((packet, index) => [packet[0], `Q${String(index + 1).padStart(2, '0')}`]))
  const inputs = Object.fromEntries(paths.map(file => [path.relative(root, file).split(path.sep).join('/'), sha256(fs.readFileSync(file))]))
  const sourcePaths = execFileSync('git', ['ls-files', '--', 'server', 'client', 'index.js'], { cwd: root, encoding: 'utf8' }).trim().split('\n')
  const digest = createHash('sha256')
  for (const relative of sourcePaths) digest.update(relative).update('\0').update(fs.readFileSync(path.join(root, relative))).update('\0')
  const rows = cs.flatMap(capability => capability.invariants.map(invariant => ({
    requirement: capability.id, invariant: invariant.id, owner: owner(capability), packet: packetIDs[owner(capability)], contract: invariant.contract,
    status: invariant.status, source: invariant.source, tests: invariant.tests, evidence: invariant.evidence, cases: capability.cases, stateful: capability.stateful,
    state_scenarios: capability.state_scenarios || {}, exceptions: [...capability.exceptions, ...(invariant.exceptions || [])]
  })))
  const data = {
    source_pin: PIN, scope: 'docs/agent-work/CONTRACT.md + go-backend/ARCHITECTURE.md', claim: 'All requirements of the defined v2.37.2 replacement contract are verified.', claim_permitted: false,
    proof: 'Mapping only. No behavior tests executed by this renderer. Test references are assertion/discovery leads, not inferred full coverage.',
    counts: { requirements: cs.length, invariants: rows.length, route_sites: routes.length, socket_sites: events.length, socket_names: cs.filter(capability => capability.id.startsWith('socket-')).length, models: inventory.model_files.length },
    pinned_source_and_client_sha256: digest.digest('hex'), capability_sha256: inputs, source_syntax_sha256: Object.fromEntries(Object.entries(inventory.sources).map(([file, record]) => [file, record.sha256])),
    routes, socket_sites: events, models: inventory.model_files.map(model => ({ ...model, requirements: ['sub-database-schema', 'persisted-column-semantics'] })), requirements: rows
  }
  const outputs = { [`${OUT}/replacement-requirement-map.json`]: jsonText(data) }
  let csv = csvRow(['requirement', 'invariant', 'packet', 'owner', 'contract', 'status', 'source', 'tests (leads only)', 'evidence', 'normal', 'error', 'edge', 'stateful', 'state scenarios', 'limitations'])
  for (const row of rows) csv += csvRow([row.requirement, row.invariant, row.packet, row.owner, row.contract, row.status, row.source.join('\n'), row.tests.join('\n'), row.evidence.join('\n'), row.cases.normal, row.cases.error, row.cases.edge, row.stateful, compactObject(row.state_scenarios), row.exceptions.join('\n')])
  outputs[`${OUT}/replacement-requirement-map.csv`] = csv
  let summary = `# v2.37.2 replacement requirement-to-test mapping

Generated by \`node go-backend/tools/replacement-contract-map.mjs\` from the canonical
[capability register](../../../docs/agent-work/capabilities) (repository root path:
\`docs/agent-work/capabilities\`). Do not edit the generated views. Check drift with \`--check\`.

Scope: [CONTRACT](../../../docs/agent-work/CONTRACT.md) and
[ARCHITECTURE](../../ARCHITECTURE.md), source \`${PIN}\`.

**${cs.length} requirements, ${rows.length} recorded invariants; 226 route sites
(223 endpoints + SPA fallback, ALL fallback and response-cache middleware),
${data.counts.socket_names} socket name/direction records, 165 syntax event sites,
and 25 persisted models.** Stream's literal clientEmit calls are additionally
resolved through its dynamic forwarding helper. Models map to schema/column
requirements; formats, config, jobs, lifecycle and clients have separate records.

This is an exhaustive register of the inspected entrypoint families and explicit
crosscutting obligations, with a gap wherever proof is missing. It is NOT an
assertion that all behavioral branches have been characterized. Per-owner packets
must refine sampled invariants into source/client-derived field, error and state
assertions and add discovered obligations before acceptance. There is no fixed
final denominator that permits newly discovered requirements to be ignored.

[Full CSV mapping](replacement-requirement-map.csv) contains every recorded
invariant's complete requirement text, source, actual current test anchors, historical
evidence, case/state status, limitations and assigned owner packet.
[Machine mapping](replacement-requirement-map.json) additionally contains all route,
event and model source-site reconciliations and input hashes.
[Ordered gap queue](replacement-gap-queue.md) owns dispatch order; ROADMAP links it.

${rows.filter(row => !row.tests.length).length} recorded invariants currently have no test anchor; ${cs.filter(capability => !capability.tests.length).length} requirement records have
no test anchor. Other anchors range from direct assertions to literal-discovery leads.
No new product execution is claimed. Historical pre-rewrite passes were reopened;
current named tests are assertion leads. A test name or literal is not proof of every
facet. Accepted playback lifecycle evidence is retained and must be linked at its
actual scenario/assertion granularity, without reopening approved differences.

Gap classes: **implementation** (source/client-backed mismatch), **proof** (missing
or partial assertion/run), **harness** (obsolete fixtures/comparison/reference),
**acceptance** (real web/native workflow evidence), and **approved exclusion**.
Ebook engine and older-schema migration remain the only approved exclusions;
missing source producers for client-only task_updated/invalid_token listeners
are discrepancies to reconcile, not invented replacement features.

Known implementation candidate: tools emits queue true/false for immediate work
where TS only emits waiting/dequeue transitions. track_started/track_progress/task_progress
producers need source-backed tests. Notification whole-settings and backup no-argument
requirements were corrected during mapping. Historical feed-timezone, derived-title,
OIDC status and boot/settings differences remain triage leads in Q02 and owning
packets, not approved exclusions or proven current bugs.

## Requirement index

| Requirement | Owner packet | Assertion leads | Invariants | Proof status |
|---|---|---:|---:|---|
`
  for (const capability of cs) summary += `| [${capability.id}](../../../${files[capability.id]}) | ${packetIDs[owner(capability)]} ${owner(capability)} | ${capability.tests.length} | ${capability.invariants.length} | ${capability.id.startsWith('socket-outgoing-stream-') ? 'scoped playback evidence; full record open' : 'open'} |\n`
  outputs[`${OUT}/replacement-requirement-map.md`] = summary
  let queue = queueHead
  for (const [key, title, scope, acceptance] of PACKETS) {
    const members = cs.filter(capability => owner(capability) === key).map(capability => capability.id)
    queue += `## ${packetIDs[key]} — ${title}\n\nOwner: **${key}**. Maximum production write boundary: \`go-backend/${scope}\`.\n\nAcceptance: ${acceptance}\n\nAssigned requirement IDs (${members.length}): `
    queue += (members.length ? members.map(id => `\`${id}\``).join(', ') : 'All preceding requirements; audit-only rollup, no production ownership.') + '\n\n'
  }
  outputs[`${OUT}/replacement-gap-queue.md`] = queue + queueFoot
  return { outputs, data }
}

export function writeOrCheck(outputs, root = ROOT, check = false) {
  for (const [relative, text] of Object.entries(outputs)) {
    const file = path.join(root, relative)
    if (check) {
      if (!fs.existsSync(file) || fs.readFileSync(file, 'utf8') !== text) throw new Error(`stale generated view: ${file}`)
    } else fs.writeFileSync(file, text)
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2)
    if (args.some(arg => arg !== '--check') || args.length > 1) throw new Error('Usage: replacement-contract-map.mjs [--check]')
    const { outputs, data } = render()
    writeOrCheck(outputs, ROOT, args.includes('--check'))
    console.log(JSON.stringify(data.counts))
    console.log('Mapping structure reconciled; product verification is still open.')
  } catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}

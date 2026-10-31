#!/usr/bin/env node
'use strict'

// Export DDL and selected defaults from an isolated database initialized by
// the actual pinned upstream Node server. No tables or model definitions are
// recreated by this script.
const crypto = require('node:crypto')
const fs = require('node:fs')
const path = require('node:path')
const { writeJSON } = require('./lib/artifacts.cjs')
const { parseArgs } = require('./lib/args.cjs')
const sqlite3 = require('sqlite3')

const ROOT = path.resolve(__dirname, '../../..')
const BASELINE = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'
const SOURCES = [
  'server/Database.js',
  'server/managers/MigrationManager.js',
  'server/objects/settings/ServerSettings.js',
  'server/objects/settings/EmailSettings.js',
  'server/objects/settings/NotificationSettings.js'
]

function args() {
  const values = parseArgs()
  if (!values.db || !values.out) throw new Error('Usage: export-schema.cjs --db <absdatabase.sqlite> --out <parity directory>')
  return values
}

function openDb(filename) {
  return new Promise((resolve, reject) => {
    const db = new sqlite3.Database(filename, sqlite3.OPEN_READONLY, (error) => error ? reject(error) : resolve(db))
  })
}
function all(db, sql, params = []) {
  return new Promise((resolve, reject) => db.all(sql, params, (error, rows) => error ? reject(error) : resolve(rows)))
}
function get(db, sql) {
  return new Promise((resolve, reject) => db.get(sql, (error, row) => error ? reject(error) : resolve(row)))
}
function hash(bytes) { return crypto.createHash('sha256').update(bytes).digest('hex') }

const SECRET_KEY = /(secret|password|token|cookie|credential|private.?key|smtp|openid.*(client|secret))/i
const ENVIRONMENT_KEY = /(path|directory|dir)$/i
function safeValue(value) {
  try { return JSON.parse(value) } catch { return value }
}
function sanitize(value) {
  if (Array.isArray(value)) return value.map(sanitize)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !SECRET_KEY.test(key) && !ENVIRONMENT_KEY.test(key))
    .map(([key, nested]) => [key, sanitize(nested)]))
}
function safeSettings(rows) {
  return rows
    .filter(({ key }) => !SECRET_KEY.test(key))
    .map(({ key, value }) => ({ key, value: sanitize(safeValue(value)) }))
}

async function main() {
  const options = args()
  const outDir = path.resolve(options.out)
  fs.mkdirSync(outDir, { recursive: true })
  const db = await openDb(path.resolve(options.db))
  try {
    const version = await get(db, 'PRAGMA user_version')
    const objects = await all(db, `SELECT type, name, sql FROM sqlite_master
      WHERE type IN ('table','index','trigger','view') AND name NOT LIKE 'sqlite_%'
      ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'trigger' THEN 2 ELSE 3 END, name`)
    const ddl = objects.filter((row) => row.sql).map((row) => `${row.sql.trim().replace(/;?$/, ';')}`).join('\n\n')
    const migrationTables = objects.filter((row) => row.type === 'table' && /migration|version/i.test(row.name))
    const migrationState = []
    for (const table of migrationTables) {
      const quoted = `"${table.name.replaceAll('"', '""')}"`
      migrationState.push({ table: table.name, rows: await all(db, `SELECT * FROM ${quoted} ORDER BY rowid`) })
    }
    const tableNames = new Set(objects.filter((row) => row.type === 'table').map((row) => row.name))
    const settings = tableNames.has('settings') ? await all(db, 'SELECT key, value FROM settings ORDER BY key') : []
    const defaultSettings = safeSettings(settings).map((row) => ({ ...row, persistedAtFreshInit: true }))
    const EmailSettings = require(path.join(ROOT, 'server/objects/settings/EmailSettings'))
    const NotificationSettings = require(path.join(ROOT, 'server/objects/settings/NotificationSettings'))
    for (const [key, row] of [
      ['email-settings', new EmailSettings().toJSON()],
      ['notification-settings', new NotificationSettings().toJSON()]
    ]) {
      if (!defaultSettings.some((setting) => setting.key === key)) {
        defaultSettings.push({ key, value: sanitize(row), persistedAtFreshInit: false })
      }
    }
    const sourceHashes = Object.fromEntries(SOURCES.map((source) => [source, hash(fs.readFileSync(path.join(ROOT, source)))]))
    const header = [
      '-- Audiobookshelf upstream fresh-init schema; source-executed, do not hand edit.',
      `-- Baseline: ${BASELINE}`,
      `-- Source SHA-256: ${JSON.stringify(sourceHashes)}`,
      `-- PRAGMA user_version: ${version.user_version}`,
      `-- Migration tables/row counts: ${migrationState.map((m) => `${m.table}=${m.rows.length}`).join(', ') || '(none)'}`,
      '-- Generated from an isolated fresh upstream initialization; fixture data and secrets are excluded.',
      ''
    ].join('\n')
    const schema = `${header}${ddl}\n`
    fs.writeFileSync(path.join(outDir, 'schema.sql'), schema)
    const schemaHash = hash(schema)
    writeJSON(path.join(outDir, 'default-settings.json'), {
      baseline: BASELINE,
      note: 'Rows selected from settings immediately after upstream Database.init, before root creation. Secret-bearing keys are omitted.',
      rows: defaultSettings
    })
    writeJSON(path.join(outDir, 'migration-state.json'), {
      baseline: BASELINE,
      sqliteUserVersion: version.user_version,
      tables: migrationState
    })
    writeJSON(path.join(outDir, 'schema-provenance.json'), {
      baseline: BASELINE,
      generator: 'go-backend/tools/parity/export-schema.cjs',
      upstreamSources: sourceHashes,
      schemaSha256: schemaHash,
      tableCount: objects.filter((row) => row.type === 'table').length,
      ddlObjectCount: objects.length,
      migrationTableCount: migrationState.length,
      migrationStateFile: 'migration-state.json',
      defaultSettingsFile: 'default-settings.json',
      migrationStateSha256: hash(fs.readFileSync(path.join(outDir, 'migration-state.json'))),
      defaultSettingsSha256: hash(fs.readFileSync(path.join(outDir, 'default-settings.json')))
    })
    console.log(JSON.stringify({ schemaSha256: schemaHash, tableCount: objects.filter((row) => row.type === 'table').length, migrationTables: migrationState.map((m) => m.table) }))
  } finally {
    await new Promise((resolve, reject) => db.close((error) => error ? reject(error) : resolve()))
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`)
  process.exitCode = 1
})

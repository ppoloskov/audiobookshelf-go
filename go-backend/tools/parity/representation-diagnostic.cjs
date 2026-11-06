#!/usr/bin/env node
'use strict'

const fs = require('node:fs')
const crypto = require('node:crypto')

function validateExpressJSONRepresentation(response) {
  if (!response || !response.body || typeof response.body !== 'object') return false
  const bytes = Buffer.from(JSON.stringify(response.body))
  const tag = `W/"${bytes.length.toString(16)}-${crypto.createHash('sha1').update(bytes).digest('base64').replace(/=+$/, '')}"`
  return response.headers?.['content-type'] === 'application/json; charset=utf-8' &&
    response.headers?.['content-length'] === String(bytes.length) &&
    response.headers?.etag === tag
}

function main(argv) {
  const input = argv[2]
  if (!input) throw new Error('Usage: node representation-diagnostic.cjs <sidecar-from---representation-diagnostics-out.json>')
  const payload = JSON.parse(fs.readFileSync(input, 'utf8'))
  const cases = payload.cases || { 'cover-patch': payload }
  if (Object.keys(cases).length === 0) throw new Error('no representation diagnostic cases supplied')
  const result = Object.fromEntries(Object.entries(cases).map(([id, pair]) => [id, {
    referenceExpressRepresentation: validateExpressJSONRepresentation(pair.reference),
    goExpressRepresentation: validateExpressJSONRepresentation(pair.go)
  }]))
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
  if (Object.values(result).some(pair => !pair.referenceExpressRepresentation || !pair.goExpressRepresentation)) process.exitCode = 1
}

if (require.main === module) {
  try {
    main(process.argv)
  } catch (error) {
    process.stderr.write(`${error.message || error}\n`)
    process.exitCode = 1
  }
}

module.exports = { validateExpressJSONRepresentation }

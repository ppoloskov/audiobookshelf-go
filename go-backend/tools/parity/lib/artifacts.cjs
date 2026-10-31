'use strict'

const fs = require('node:fs')
const path = require('node:path')

function writeJSON(file, value, { ensureDirectory = false, newline = true, space = 2 } = {}) {
  if (ensureDirectory) fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, `${JSON.stringify(value, null, space)}${newline ? '\n' : ''}`)
}

module.exports = { writeJSON }

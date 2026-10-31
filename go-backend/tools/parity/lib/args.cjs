'use strict'

function parseArgs(argv = process.argv, startAt = 2) {
  const out = {}
  for (let i = startAt; i < argv.length; i++) {
    const arg = argv[i]
    if (arg.startsWith('--')) out[arg.slice(2)] = argv[++i]
  }
  return out
}

module.exports = { parseArgs }

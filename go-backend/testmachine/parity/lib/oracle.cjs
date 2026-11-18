'use strict'
const path = require('node:path')
const { execFileSync } = require('node:child_process')
const ROOT = path.resolve(__dirname, '../../../..')
const PINNED = 'edb1f7efc92ecacdef51aafb4d9d5054ce10604f'

function prepareOracle() {
  const inputs = ['server', 'index.js', 'tsconfig.server.json', 'package.json', 'package-lock.json']
  execFileSync('git', ['diff', '--quiet', PINNED, '--', ...inputs], { cwd: ROOT })
  const untracked = execFileSync('git', ['ls-files', '--others', '--exclude-standard', '--', ...inputs], { cwd: ROOT, encoding: 'utf8' }).trim()
  if (untracked) throw new Error(`untracked oracle inputs are not pinned: ${untracked}`)
  // Never execute an older bundle while claiming the pinned source as oracle.
  execFileSync(process.execPath, [path.join(ROOT, 'node_modules/typescript/bin/tsc'), '-p', 'tsconfig.server.json'], { cwd: ROOT, stdio: 'inherit' })
}
module.exports = { prepareOracle }

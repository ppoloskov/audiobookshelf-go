#!/usr/bin/env node
// Fetches and installs pinned upstream Audiobookshelf TS source into
// ORACLE_UPSTREAM_DIR (default: <repoRoot>/.oracle-upstream) for the oracle.
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

function findRepoRoot(startDir) {
  let dir = startDir
  while (true) {
    if (
      fs.existsSync(path.join(dir, 'go.mod')) &&
      fs.existsSync(path.join(dir, 'testmachine', 'oracle'))
    ) {
      return dir
    }
    const parent = path.dirname(dir)
    if (parent === dir) {
      throw new Error('repository root (go.mod and testmachine/oracle/) not found above ' + startDir)
    }
    dir = parent
  }
}

function run(cmd, args, cwd) {
  const res = spawnSync(cmd, args, { cwd, stdio: 'inherit' })
  if (res.error) {
    throw new Error(`${cmd} ${args.join(' ')} failed: ${res.error.message}`)
  }
  if (res.status !== 0) {
    throw new Error(`${cmd} ${args.join(' ')} exited with status ${res.status}`)
  }
}

function hasOriginRemote(targetDir) {
  const res = spawnSync('git', ['remote', 'get-url', 'origin'], {
    cwd: targetDir,
    encoding: 'utf8',
  })
  return res.status === 0
}

function main() {
  const repoRoot = findRepoRoot(__dirname)
  const upstreamPath = path.join(__dirname, 'upstream.json')
  const upstream = JSON.parse(fs.readFileSync(upstreamPath, 'utf8'))
  if (!upstream.repo || !upstream.commit) {
    throw new Error(`invalid ${upstreamPath}: repo and commit are required`)
  }

  const targetDir = process.env.ORACLE_UPSTREAM_DIR
    ? path.resolve(process.env.ORACLE_UPSTREAM_DIR)
    : path.join(repoRoot, '.oracle-upstream')

  const markerPath = path.join(targetDir, '.upstream-commit')
  const indexPath = path.join(targetDir, 'index.js')
  const nodeModulesPath = path.join(targetDir, 'node_modules')

  if (
    fs.existsSync(markerPath) &&
    fs.existsSync(indexPath) &&
    fs.existsSync(nodeModulesPath)
  ) {
    const currentCommit = fs.readFileSync(markerPath, 'utf8').trim()
    if (currentCommit === upstream.commit) {
      console.log(`[upstream] already seeded at ${targetDir} (${upstream.commit.slice(0, 12)})`)
      return
    }
  }

  console.log(`[upstream] fetching ${upstream.repo} @ ${upstream.commit} -> ${targetDir}`)
  fs.mkdirSync(targetDir, { recursive: true })

  if (!fs.existsSync(path.join(targetDir, '.git'))) {
    run('git', ['init'], targetDir)
  }
  if (hasOriginRemote(targetDir)) {
    run('git', ['remote', 'set-url', 'origin', upstream.repo], targetDir)
  } else {
    run('git', ['remote', 'add', 'origin', upstream.repo], targetDir)
  }
  run('git', ['fetch', '--depth', '1', 'origin', upstream.commit], targetDir)
  run('git', ['checkout', '-f', 'FETCH_HEAD'], targetDir)
  fs.writeFileSync(markerPath, `${upstream.commit}\n`, 'utf8')

  const npmCmd = process.platform === 'win32' ? 'npm.cmd' : 'npm'
  run(npmCmd, ['ci', '--no-audit', '--no-fund'], targetDir)
  console.log(`[upstream] ready at ${targetDir} (${upstream.version || upstream.commit.slice(0, 12)})`)
}

try {
  main()
} catch (err) {
  console.error(`[upstream] error: ${err.message}`)
  process.exit(1)
}

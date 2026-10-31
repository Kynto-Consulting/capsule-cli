#!/usr/bin/env node
'use strict'

const path  = require('path')
const fs    = require('fs')
const os    = require('os')
const zlib  = require('zlib')
const childProcess = require('child_process')
const { TextEncoder, TextDecoder } = require('util')

// Polyfill globals that Go's wasm_exec.js expects in Node.js context
globalThis.require      = require
globalThis.fs           = fs
globalThis.path         = path
globalThis.TextEncoder  = TextEncoder
globalThis.TextDecoder  = TextDecoder
if (!globalThis.performance) globalThis.performance = require('perf_hooks').performance
// crypto is read-only on Node 22+ — already available natively, skip polyfill

const root     = path.join(__dirname, '..')
const wasmPath = path.join(root, 'capsule.wasm')

if (!fs.existsSync(wasmPath)) {
  process.stderr.write(
    'capsule: WASM binary not found.\n' +
    'Re-install or rebuild: cd cli && GOOS=js GOARCH=wasm go build -o ../npm/capsule.wasm ./cmd/capsule\n'
  )
  process.exit(1)
}

// ── Node.js source packager ──────────────────────────────────────────────────
// Go's WASM bridge on Windows does not support O_DIRECTORY (needed by
// os.ReadDir). We create the archive here in Node.js instead, then pass it
// to the Go binary via CAPSULE_SOURCE_ARCHIVE (a temp file path).

const SKIP_DIRS = new Set(['.git', '.svn', 'node_modules', 'vendor'])

function loadGitignorePatterns(dir) {
  try {
    return fs.readFileSync(path.join(dir, '.gitignore'), 'utf8')
      .split(/\r?\n/)
      .map(l => l.trim())
      .filter(l => l && !l.startsWith('#'))
  } catch { return [] }
}

function matchesGitignore(patterns, relPath) {
  const base = path.basename(relPath)
  for (const pat of patterns) {
    const trimmed = pat.replace(/\/$/, '')
    if (relPath === trimmed || base === trimmed) return true
    if (relPath.startsWith(trimmed + '/')) return true
    if (pat.includes('*')) {
      const re = new RegExp('^' + pat.replace(/\./g, '\\.').replace(/\*/g, '[^/]*') + '$')
      if (re.test(relPath) || re.test(base)) return true
    }
  }
  return false
}

function writeTarHeader(buf, offset, name, size, mtime) {
  buf.fill(0, offset, offset + 512)
  const nameBytes = Buffer.from(name)
  nameBytes.copy(buf, offset, 0, Math.min(100, nameBytes.length))
  buf.write('0000644\0', offset + 100, 'ascii')
  buf.write('0000000\0', offset + 108, 'ascii')
  buf.write('0000000\0', offset + 116, 'ascii')
  buf.write(size.toString(8).padStart(11, '0') + '\0', offset + 124, 'ascii')
  buf.write(Math.floor(mtime / 1000).toString(8).padStart(11, '0') + '\0', offset + 136, 'ascii')
  buf.write('        ', offset + 148, 'ascii')
  buf[offset + 156] = 0x30
  buf.write('ustar\0', offset + 257, 'ascii')
  buf.write('00', offset + 263, 'ascii')
  let sum = 0
  for (let i = 0; i < 512; i++) sum += buf[offset + i]
  buf.write(sum.toString(8).padStart(6, '0') + '\0 ', offset + 148, 'ascii')
}

function createSourceArchiveNode(dir) {
  const patterns = loadGitignorePatterns(dir)
  const entries  = []

  function walk(absDir, relPrefix) {
    let items
    try { items = fs.readdirSync(absDir, { withFileTypes: true }) }
    catch { return }
    for (const item of items) {
      const name    = item.name
      const relPath = relPrefix ? relPrefix + '/' + name : name
      if (item.isDirectory()) {
        if (SKIP_DIRS.has(name) || name.startsWith('.')) continue
        if (matchesGitignore(patterns, relPath)) continue
        walk(path.join(absDir, name), relPath)
      } else {
        if (matchesGitignore(patterns, relPath)) continue
        try {
          const stat    = fs.statSync(path.join(absDir, name))
          const content = fs.readFileSync(path.join(absDir, name))
          entries.push({ name: relPath, content, mtime: stat.mtimeMs })
        } catch { /* skip unreadable */ }
      }
    }
  }

  walk(dir, '')
  if (entries.length === 0) return null

  const blocks = []
  for (const entry of entries) {
    const hdr = Buffer.alloc(512)
    writeTarHeader(hdr, 0, entry.name, entry.content.length, entry.mtime)
    blocks.push(hdr)
    const padded = Math.ceil(entry.content.length / 512) * 512
    const padBuf = Buffer.alloc(padded)
    entry.content.copy(padBuf)
    blocks.push(padBuf)
  }
  blocks.push(Buffer.alloc(1024))

  const gzBuf  = zlib.gzipSync(Buffer.concat(blocks))
  const tmpFile = path.join(os.tmpdir(), 'capsule-src-' + Date.now() + '.tar.gz')
  fs.writeFileSync(tmpFile, gzBuf)
  return tmpFile
}

// Load Go WASM shim — defines globalThis.Go
require(path.join(root, 'wasm_exec.js'))

// Go's js/wasm runtime refuses to start when argv + environ exceed ~8 KB
// ("total length of command line and environment variables exceeds limit").
// A full Windows environment easily passes that, so only forward the
// variables the CLI actually reads: its own CAPSULE_* settings, the home and
// temp locations used for ~/.capsule/config.yaml, terminal hints and proxies.
const ENV_ALLOW = new Set([
  'HOME', 'USERPROFILE', 'HOMEDRIVE', 'HOMEPATH', 'APPDATA', 'LOCALAPPDATA',
  'TMPDIR', 'TEMP', 'TMP', 'TERM', 'NO_COLOR', 'FORCE_COLOR', 'COLUMNS', 'LINES',
  'HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'http_proxy', 'https_proxy', 'no_proxy',
])

// GitHub Actions context read by `capsule deploy` (metadata, outputs, summary).
const GITHUB_ALLOW = new Set([
  'CI', 'GITHUB_ACTIONS', 'GITHUB_SHA', 'GITHUB_REF', 'GITHUB_REF_NAME', 'GITHUB_HEAD_REF',
  'GITHUB_ACTOR', 'GITHUB_REPOSITORY', 'GITHUB_RUN_ID', 'GITHUB_SERVER_URL', 'GITHUB_EVENT_NAME',
  'GITHUB_EVENT_PATH', 'GITHUB_OUTPUT', 'GITHUB_STEP_SUMMARY',
])

function wasmEnv(env) {
  const out = {}
  for (const [k, v] of Object.entries(env)) {
    if (ENV_ALLOW.has(k) || k.startsWith('CAPSULE_') || GITHUB_ALLOW.has(k)) out[k] = v
  }
  return out
}

function git(gitArgs, cwd) {
  const r = childProcess.spawnSync('git', gitArgs, { cwd, encoding: 'utf8' })
  return r.status === 0 ? r.stdout.trim() : null
}

// Executes the gh calls queued by the CLI (`ci init --set-secrets`). Only
// `gh secret set` and `gh variable set` are allowed; secret values travel on
// stdin, never on the command line, and the queue file is removed afterwards.
function runPostActions(file, cwd) {
  let list = []
  try {
    const raw = fs.readFileSync(file, 'utf8')
    if (raw) list = JSON.parse(raw)
  } catch { /* nothing queued */ }
  try { fs.unlinkSync(file) } catch {}
  for (const a of list) {
    const ok = a && a.cmd === 'gh' && Array.isArray(a.args) &&
      (a.args[0] === 'secret' || a.args[0] === 'variable') && a.args[1] === 'set'
    if (!ok) { process.stderr.write('capsule: refused to run queued command\n'); continue }
    const r = childProcess.spawnSync('gh', a.args, {
      cwd, input: a.stdin || undefined, stdio: [a.stdin ? 'pipe' : 'ignore', 'inherit', 'inherit'],
    })
    if (r.status !== 0) process.stderr.write('capsule: gh ' + a.args.slice(0, 3).join(' ') + ' failed\n')
  }
}

async function main() {
  const cwd  = process.cwd()
  const args = process.argv.slice(2)

  // Pre-create archive in Node.js — bypasses Go WASM O_DIRECTORY limitation on Windows
  let archivePath = null
  const NON_DEPLOY = new Set(['tokens', 'ci', 'vars', 'env', 'deployments'])
  const isDeployCmd = !NON_DEPLOY.has(args[0]) && args.includes('deploy')
  if (isDeployCmd) {
    archivePath = createSourceArchiveNode(cwd)
  }

  // os/exec does not exist under WASM, so anything needing a subprocess is
  // done here: git facts for `deploy`, and the gh calls queued by `ci init`.
  const extra = {}
  if (isDeployCmd && process.env.GITHUB_ACTIONS !== 'true') {
    const sha = git(['rev-parse', '--short', 'HEAD'], cwd)
    const branch = git(['rev-parse', '--abbrev-ref', 'HEAD'], cwd)
    const dirty = git(['status', '--porcelain'], cwd)
    if (sha) extra.CAPSULE_GIT_SHA = sha
    if (branch) extra.CAPSULE_GIT_BRANCH = branch
    if (dirty !== null) extra.CAPSULE_GIT_DIRTY = String(dirty.split('\n').filter(Boolean).length)
  }
  let postActionsPath = null
  if (args[0] === 'ci' && args.includes('--set-secrets')) {
    const probe = childProcess.spawnSync('gh', ['auth', 'status'], { stdio: 'ignore' })
    if (probe.status === 0) {
      extra.CAPSULE_HAS_GH = '1'
      postActionsPath = path.join(os.tmpdir(), 'capsule-post-' + process.pid + '-' + Date.now() + '.json')
      fs.writeFileSync(postActionsPath, '', { mode: 0o600 })
      extra.CAPSULE_POST_ACTIONS = postActionsPath
    }
  }

  const go    = new globalThis.Go()
  go.argv     = ['capsule', ...args]
  go.env      = Object.assign(wasmEnv(process.env), extra, {
    TMPDIR:                 os.tmpdir(),
    CAPSULE_CWD:            cwd,
    CAPSULE_SOURCE_ARCHIVE: archivePath || '',
  })
  go.exit     = process.exit

  const buf    = fs.readFileSync(wasmPath)
  const result = await WebAssembly.instantiate(buf, go.importObject)

  process.on('exit', code => {
    if (archivePath) { try { fs.unlinkSync(archivePath) } catch {} }
    if (postActionsPath) runPostActions(postActionsPath, cwd)
    if (code === 0 && !go.exited) {
      go._pendingEvent = { id: 0 }
      go._resume()
    }
  })

  await go.run(result.instance)
}

main().catch(err => {
  process.stderr.write('capsule: ' + String(err) + '\n')
  process.exit(1)
})

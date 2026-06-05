#!/usr/bin/env node
'use strict'

const path  = require('path')
const fs    = require('fs')
const os    = require('os')
const zlib  = require('zlib')
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

async function main() {
  const cwd  = process.cwd()
  const args = process.argv.slice(2)

  // Pre-create archive in Node.js — bypasses Go WASM O_DIRECTORY limitation on Windows
  let archivePath = null
  const isDeployCmd = args[0] === 'deploy' || args.includes('deploy')
  if (isDeployCmd) {
    archivePath = createSourceArchiveNode(cwd)
  }

  const go    = new globalThis.Go()
  go.argv     = ['capsule', ...args]
  go.env      = Object.assign({}, process.env, {
    TMPDIR:                 os.tmpdir(),
    CAPSULE_CWD:            cwd,
    CAPSULE_SOURCE_ARCHIVE: archivePath || '',
  })
  go.exit     = process.exit

  const buf    = fs.readFileSync(wasmPath)
  const result = await WebAssembly.instantiate(buf, go.importObject)

  process.on('exit', code => {
    if (archivePath) { try { fs.unlinkSync(archivePath) } catch {} }
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

#!/usr/bin/env node
'use strict'

const path = require('path')
const fs   = require('fs')
const os   = require('os')
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

// Load Go WASM shim — defines globalThis.Go
require(path.join(root, 'wasm_exec.js'))

async function main() {
  const go    = new globalThis.Go()
  go.argv     = ['capsule', ...process.argv.slice(2)]
  go.env      = Object.assign({ TMPDIR: os.tmpdir() }, process.env)
  go.exit     = process.exit

  const buf    = fs.readFileSync(wasmPath)
  const result = await WebAssembly.instantiate(buf, go.importObject)

  process.on('exit', code => {
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

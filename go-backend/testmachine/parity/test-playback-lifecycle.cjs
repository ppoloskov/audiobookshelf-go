'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { assertEvents } = require('./playback-lifecycle.cjs')

const reportPath = path.resolve(__dirname, '../contracts/parity/playback-lifecycle-evidence/final-live-observations.json')
const report = JSON.parse(fs.readFileSync(reportPath, 'utf8'))
const progress = report.scenarios.find(scenario => scenario.mode === 'progress')
function clone(value) { return structuredClone(value) }
function removeIncremental(evidence, recipient) {
  evidence.events = evidence.events.filter(event => event.recipient !== recipient || event.name !== 'stream_progress' || !(event.args[0].chunks?.length && event.args[0].numSegments > 0))
}

for (const side of ['node', 'go']) {
  test(`recorded ${side} progress trace passes`, () => assertEvents(side, 'progress', clone(progress[side])))
  for (const recipient of ['owner-1', 'owner-2']) {
    test(`${side} rejects removal of ${recipient} incremental updates`, () => {
      const evidence = clone(progress[side])
      removeIncremental(evidence, recipient)
      assert.throws(() => assertEvents(side, 'progress', evidence), /nonzero incremental progress/)
    })
  }
  test(`${side} rejects removal of both owners' incremental updates`, () => {
    const evidence = clone(progress[side])
    for (const recipient of ['owner-1', 'owner-2']) removeIncremental(evidence, recipient)
    assert.throws(() => assertEvents(side, 'progress', evidence), /nonzero incremental progress/)
  })
  test(`${side} rejects incremental progress after close`, () => {
    const evidence = clone(progress[side])
    const owner = evidence.events.find(event => event.recipient === 'owner-1' && event.name === 'stream_progress' && event.args[0].chunks?.length)
    const close = evidence.events.find(event => event.recipient === 'owner-1' && event.name === 'stream_closed')
    removeIncremental(evidence, 'owner-1')
    evidence.events.push(owner)
    owner.at = close.at + 1
    evidence.events.sort((a, b) => a.at - b.at)
    assert.throws(() => assertEvents(side, 'progress', evidence))
  })
}

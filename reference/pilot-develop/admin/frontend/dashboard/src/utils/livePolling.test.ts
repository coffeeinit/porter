import test from 'node:test'
import assert from 'node:assert/strict'

import { livePollDelayMs, LIVE_POLL_MS, LIVE_WARMUP_POLL_MS } from './livePolling.ts'

test('warms up while the chart cannot draw a line yet', () => {
  assert.equal(livePollDelayMs({ isLive: true, pointCount: 0 }), LIVE_WARMUP_POLL_MS)
  assert.equal(livePollDelayMs({ isLive: true, pointCount: 1 }), LIVE_WARMUP_POLL_MS)
})

test('settles to the steady cadence once two points exist', () => {
  assert.equal(livePollDelayMs({ isLive: true, pointCount: 2 }), LIVE_POLL_MS)
  assert.equal(livePollDelayMs({ isLive: true, pointCount: 240 }), LIVE_POLL_MS)
})

test('a seeded bench never warms up', () => {
  // Production seeds a full window from the monitor log before the first schedule.
  assert.equal(livePollDelayMs({ isLive: true, pointCount: 360 }), LIVE_POLL_MS)
})

test('non-live views keep the steady cadence regardless of point count', () => {
  assert.equal(livePollDelayMs({ isLive: false, pointCount: 0 }), LIVE_POLL_MS)
  assert.equal(livePollDelayMs({ isLive: false, pointCount: 1 }), LIVE_POLL_MS)
})

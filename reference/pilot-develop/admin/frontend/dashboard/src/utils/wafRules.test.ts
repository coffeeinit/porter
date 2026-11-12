import assert from 'node:assert/strict'
import test from 'node:test'

import { actionLabel, ruleSummary } from './wafRules.ts'

const condition = (over = {}) => ({
  field: 'uri_path',
  operator: 'contains',
  value: '/admin',
  ...over,
})

test('ruleSummary spells out a lone condition', () => {
  assert.equal(
    ruleSummary({ match: 'all', conditions: [condition()] }),
    'When URI Path contains "/admin"',
  )
})

test('ruleSummary names the request header instead of the field', () => {
  const header = condition({ field: 'header', header_name: 'X-Real-IP', value: '10.0.0.1' })
  assert.equal(ruleSummary({ match: 'all', conditions: [header] }), 'When Header X-Real-IP contains "10.0.0.1"')
})

test('ruleSummary counts conditions past the first, and reports the match mode', () => {
  const two = [condition(), condition({ field: 'method', value: 'POST' })]
  assert.equal(ruleSummary({ match: 'all', conditions: two }), 'When all of 2 conditions match')
  assert.equal(ruleSummary({ match: 'any', conditions: two }), 'When any of 2 conditions match')
})

test('ruleSummary survives a half-built rule', () => {
  assert.equal(ruleSummary({ match: 'all', conditions: [] }), 'When all of 0 conditions match')
  assert.equal(ruleSummary({ match: 'all' }), 'When all of 0 conditions match')
  assert.equal(
    ruleSummary({ match: 'all', conditions: [condition({ value: '' })] }),
    'When URI Path contains "…"',
  )
})

test('actionLabel falls back to the raw action', () => {
  assert.equal(actionLabel({ action: 'block' }), 'Block')
  assert.equal(actionLabel({ action: 'quarantine' }), 'quarantine')
})

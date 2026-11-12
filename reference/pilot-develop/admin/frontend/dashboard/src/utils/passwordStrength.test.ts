import assert from 'node:assert/strict'
import test from 'node:test'

import { PASSWORD_REQUIREMENTS, meetsPasswordRequirements } from './passwordStrength.ts'

// These rules must stay in step with validate_admin_password in pilot/internal/validators.py,
// which the POST /auth/password route enforces.
test('accepts a password meeting every requirement', () => {
  assert.equal(meetsPasswordRequirements('N3wSecret!'), true)
})

test('rejects a password missing any single requirement', () => {
  assert.equal(meetsPasswordRequirements('Sh0rt!'), false, 'too short')
  assert.equal(meetsPasswordRequirements('n3wsecret!'), false, 'no uppercase')
  assert.equal(meetsPasswordRequirements('N3WSECRET!'), false, 'no lowercase')
  assert.equal(meetsPasswordRequirements('NewSecret!'), false, 'no digit')
  assert.equal(meetsPasswordRequirements('N3wSecret'), false, 'no symbol')
  assert.equal(meetsPasswordRequirements(''), false, 'empty')
})

test('every requirement is individually enforced', () => {
  assert.equal(PASSWORD_REQUIREMENTS.length, 4)
  for (const requirement of PASSWORD_REQUIREMENTS) {
    assert.equal(requirement.test('N3wSecret!'), true, requirement.label)
  }
})

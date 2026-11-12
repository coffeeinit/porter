import assert from 'node:assert/strict'
import test from 'node:test'

import { API_V1_PREFIX, apiErrorMessage, apiUrl, isSessionExpired, unwrap } from '../api/client.ts'
import { useSignedOut } from '../composables/auth/useSignedOut.ts'

test('builds relative and cross-origin v1 API URLs', () => {
  assert.equal(API_V1_PREFIX, '/api/v1')
  assert.equal(apiUrl('tasks/task-id/events'), '/api/v1/tasks/task-id/events')
  assert.equal(
    apiUrl('/health', 'https://admin.example.com'),
    'https://admin.example.com/api/v1/health',
  )
})

test('reads canonical and transitional API error messages', () => {
  assert.equal(apiErrorMessage({ error: { message: 'Invalid value.' } }), 'Invalid value.')
  assert.equal(apiErrorMessage({ error: 'Legacy error.' }), 'Legacy error.')
  assert.equal(apiErrorMessage({}, 'Try again.'), 'Try again.')
})

test('unwrap rethrows a resolved error body as a rejection', async () => {
  await assert.rejects(
    unwrap(
      Promise.resolve({
        error: { message: 'System-managed and secret-like configuration keys cannot be changed.' },
      }),
    ),
    { message: 'System-managed and secret-like configuration keys cannot be changed.' },
  )
})

test('unwrap passes a successful payload through', async () => {
  assert.deepEqual(await unwrap(Promise.resolve({ ssl: true })), { ssl: true })
})

const jsonResponse = (status, body) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })

test('a revoked or expired session is recognised as signed out', async () => {
  const response = jsonResponse(401, {
    error: { code: 'authentication_required', message: 'Authentication is required.' },
  })
  assert.equal(await isSessionExpired(response), true)
  // The hook clones before reading, so the caller still gets an unconsumed body.
  assert.equal(response.bodyUsed, false)
})

test('a rejected credential is not treated as being signed out', async () => {
  // Wrong password on login, or on a password change: the session is still fine.
  const response = jsonResponse(401, {
    error: { code: 'invalid_credentials', message: 'Incorrect password.' },
  })
  assert.equal(await isSessionExpired(response), false)
})

test('non-401 and unparsable responses are not signed out', async () => {
  assert.equal(await isSessionExpired(jsonResponse(200, { authenticated: true })), false)
  assert.equal(
    await isSessionExpired(jsonResponse(403, { error: { code: 'forbidden' } })),
    false,
  )
  assert.equal(await isSessionExpired(new Response('<html>gateway</html>', { status: 401 })), false)
})

test('unwrap stops resolving once the signed-out modal has taken over', async () => {
  const { signedOut } = useSignedOut()
  signedOut.value = true
  try {
    const settled = await Promise.race([
      unwrap(Promise.resolve({ error: { message: 'Could not load settings.' } })).then(
        () => 'resolved',
        () => 'rejected',
      ),
      new Promise((resolve) => setTimeout(() => resolve('pending'), 20)),
    ])
    // Neither branch runs, so no component sets error text under the modal.
    assert.equal(settled, 'pending')
  } finally {
    signedOut.value = false
  }
})

test('unwrap still rejects normally when the session is intact', async () => {
  const { signedOut } = useSignedOut()
  assert.equal(signedOut.value, false)
  await assert.rejects(unwrap(Promise.resolve({ error: { message: 'Bad input.' } })), {
    message: 'Bad input.',
  })
})

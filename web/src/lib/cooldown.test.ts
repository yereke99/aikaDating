import test from 'node:test'
import assert from 'node:assert/strict'

import { deadlineFromError, formatRemaining, isActive, mergeCooldowns, pruneCooldowns, remainingMs, serverNow, syncServerTime } from './cooldown.ts'

test('remaining time counts down and stops at zero', () => {
  const now = Date.parse('2026-08-05T16:00:00Z')
  const deadline = '2026-08-05T16:30:00Z'
  assert.equal(remainingMs(deadline, now), 30 * 60 * 1000)
  assert.equal(remainingMs(deadline, now + 29 * 60 * 1000), 60 * 1000)
  assert.equal(remainingMs(deadline, Date.parse(deadline)), 0)
  assert.equal(remainingMs(deadline, Date.parse(deadline) + 5000), 0)
  assert.equal(isActive(deadline, Date.parse(deadline) - 1), true)
  assert.equal(isActive(deadline, Date.parse(deadline)), false)
})

test('a missing or invalid deadline is never treated as active', () => {
  assert.equal(remainingMs(undefined), 0)
  assert.equal(remainingMs(''), 0)
  assert.equal(remainingMs('not a date'), 0)
  assert.equal(isActive(undefined), false)
})

test('countdown labels round seconds up', () => {
  assert.equal(formatRemaining(24 * 60 * 1000 + 37 * 1000), '24:37')
  assert.equal(formatRemaining(1), '0:01')
  assert.equal(formatRemaining(0), '0:00')
  assert.equal(formatRemaining(-5000), '0:00')
  assert.equal(formatRemaining(59_999), '1:00')
  assert.equal(formatRemaining(3_600_000), '1:00:00')
})

test('the server clock wins over a wrong device clock', () => {
  const drift = 5 * 60 * 1000
  syncServerTime(new Date(Date.now() + drift).toISOString())
  assert.ok(Math.abs(serverNow() - (Date.now() + drift)) < 1000)
  // An unusable value leaves the previous offset untouched rather than resetting it to garbage.
  syncServerTime('nonsense')
  assert.ok(Math.abs(serverNow() - (Date.now() + drift)) < 1000)
  syncServerTime(new Date().toISOString())
})

test('merging keeps the later deadline so a stale poll cannot clear a fresh timer', () => {
  const early = '2026-08-05T16:10:00Z'
  const late = '2026-08-05T16:30:00Z'
  assert.deepEqual(mergeCooldowns({ like: late }, { like: early }), { like: late })
  assert.deepEqual(mergeCooldowns({ like: early }, { like: late }), { like: late })
  assert.deepEqual(mergeCooldowns({ like: late }, {}), { like: late })
  assert.deepEqual(mergeCooldowns(undefined, { message: late }), { message: late })
  // The two actions never interfere with one another.
  assert.deepEqual(mergeCooldowns({ like: late }, { message: early }), { like: late, message: early })
})

test('expired deadlines are pruned', () => {
  const now = Date.parse('2026-08-05T16:00:00Z')
  const pruned = pruneCooldowns({ like: '2026-08-05T15:59:00Z', message: '2026-08-05T16:30:00Z' }, now)
  assert.deepEqual(pruned, { message: '2026-08-05T16:30:00Z' })
})

test('a cooldown refusal is understood in either response shape', () => {
  assert.deepEqual(deadlineFromError({ code: 'like_cooldown_active', next_allowed_at: '2026-08-05T16:30:00Z' }), {
    action: 'like',
    nextAllowedAt: '2026-08-05T16:30:00Z',
  })
  assert.deepEqual(
    deadlineFromError({ error: { code: 'message_cooldown_active', next_allowed_at: '2026-08-05T16:45:00Z' } }),
    { action: 'message', nextAllowedAt: '2026-08-05T16:45:00Z' },
  )
  assert.equal(deadlineFromError({ error: { code: 'server_error' } }), null)
  assert.equal(deadlineFromError(null), null)
  assert.equal(deadlineFromError('nope'), null)
})

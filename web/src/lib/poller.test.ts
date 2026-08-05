import test from 'node:test'
import assert from 'node:assert/strict'

import { createPoller } from './poller.ts'

/** A hand-driven clock, so the loop can be stepped without waiting for real seconds to pass. */
function fakeTimers() {
  const pending = new Map<number, { callback: () => void; delay: number }>()
  let nextHandle = 1
  return {
    schedule(callback: () => void, delay: number) {
      const handle = nextHandle++
      pending.set(handle, { callback, delay })
      return handle
    },
    cancel(handle: number) {
      pending.delete(handle)
    },
    pendingCount: () => pending.size,
    lastDelay: () => [...pending.values()].at(-1)?.delay,
    async fire() {
      const entries = [...pending.entries()]
      pending.clear()
      for (const [, entry] of entries) entry.callback()
      await Promise.resolve()
      await Promise.resolve()
    },
  }
}

const settle = async () => {
  for (let index = 0; index < 5; index += 1) await Promise.resolve()
}

test('requests never overlap, even when one takes longer than the interval', async () => {
  const timers = fakeTimers()
  let started = 0
  let release: (() => void) | undefined
  const poller = createPoller({
    intervalMs: 2000,
    schedule: timers.schedule,
    cancel: timers.cancel,
    run: () =>
      new Promise<number>((resolve) => {
        started += 1
        release = () => resolve(started)
      }),
  })

  poller.start()
  await settle()
  assert.equal(started, 1)
  assert.equal(poller.isInFlight(), true)
  // Nothing is scheduled while a request is outstanding, so no second request can be queued.
  assert.equal(timers.pendingCount(), 0)

  release!()
  await settle()
  assert.equal(started, 1)
  assert.equal(timers.pendingCount(), 1)
  assert.equal(timers.lastDelay(), 2000)

  await timers.fire()
  await settle()
  assert.equal(started, 2)
  poller.stop()
})

test('stopping aborts the in-flight request and schedules nothing more', async () => {
  const timers = fakeTimers()
  let aborted = false
  let delivered = 0
  const poller = createPoller({
    intervalMs: 2000,
    schedule: timers.schedule,
    cancel: timers.cancel,
    onResult: () => {
      delivered += 1
    },
    run: (signal) =>
      new Promise<string>((resolve) => {
        signal.addEventListener('abort', () => {
          aborted = true
        })
        setTimeout(() => resolve('late'), 0)
      }),
  })

  poller.start()
  await settle()
  poller.stop()
  assert.equal(aborted, true)
  assert.equal(poller.isRunning(), false)

  // The response that lands after the stop is discarded instead of updating a gone component.
  await new Promise((resolve) => setTimeout(resolve, 5))
  await settle()
  assert.equal(delivered, 0)
  assert.equal(timers.pendingCount(), 0)
})

test('start is idempotent, so a rerun effect cannot create a second loop', async () => {
  const timers = fakeTimers()
  let started = 0
  const poller = createPoller({
    intervalMs: 2000,
    schedule: timers.schedule,
    cancel: timers.cancel,
    run: async () => {
      started += 1
      return started
    },
  })

  poller.start()
  poller.start()
  poller.start()
  await settle()
  assert.equal(started, 1)
  assert.equal(timers.pendingCount(), 1)
  poller.stop()
})

test('failures back off and recover to the normal interval', async () => {
  const timers = fakeTimers()
  let failing = true
  let errors = 0
  const poller = createPoller({
    intervalMs: 2000,
    maxBackoffMs: 10_000,
    schedule: timers.schedule,
    cancel: timers.cancel,
    onError: () => {
      errors += 1
    },
    run: async () => {
      if (failing) throw new Error('offline')
      return 'ok'
    },
  })

  poller.start()
  await settle()
  assert.equal(errors, 1)
  assert.equal(timers.lastDelay(), 4000)

  await timers.fire()
  await settle()
  assert.equal(errors, 2)
  assert.equal(timers.lastDelay(), 8000)

  await timers.fire()
  await settle()
  assert.equal(timers.lastDelay(), 10_000, 'backoff is capped')

  failing = false
  await timers.fire()
  await settle()
  assert.equal(timers.lastDelay(), 2000, 'recovers to the polling interval')
  poller.stop()
})

test('refresh replaces the pending wait with an immediate request', async () => {
  const timers = fakeTimers()
  let started = 0
  const poller = createPoller({
    intervalMs: 2000,
    schedule: timers.schedule,
    cancel: timers.cancel,
    run: async () => {
      started += 1
      return started
    },
  })

  poller.start()
  await settle()
  assert.equal(started, 1)
  poller.refresh()
  await settle()
  assert.equal(started, 2)
  assert.equal(timers.pendingCount(), 1)
  poller.stop()
  poller.refresh()
  await settle()
  assert.equal(started, 2, 'refresh does nothing once stopped')
})

/**
 * A polling loop that can never overlap itself.
 *
 * `setInterval` is wrong for this: on a slow connection it stacks requests until the device is
 * fighting itself. Here the next tick is scheduled only after the previous request settles, the
 * in-flight request is aborted when the loop stops, and a transient failure backs off instead of
 * hammering the server every two seconds.
 */
export interface PollerOptions<T> {
  /** Delay between the end of one request and the start of the next. */
  intervalMs: number
  run: (signal: AbortSignal) => Promise<T>
  onResult?: (value: T) => void
  onError?: (error: unknown) => void
  /** Upper bound for the exponential backoff applied after consecutive failures. */
  maxBackoffMs?: number
  /** Injected in tests so the loop can be driven without real time passing. */
  schedule?: (callback: () => void, delay: number) => number
  cancel?: (handle: number) => void
}

export interface Poller {
  start: () => void
  stop: () => void
  /** Runs immediately, cancelling any pending wait — used when a filter changes. */
  refresh: () => void
  isRunning: () => boolean
  isInFlight: () => boolean
}

export function createPoller<T>(options: PollerOptions<T>): Poller {
  const schedule = options.schedule ?? ((callback, delay) => window.setTimeout(callback, delay))
  const cancel = options.cancel ?? ((handle) => window.clearTimeout(handle))
  const maxBackoff = options.maxBackoffMs ?? 30_000

  let running = false
  let inFlight = false
  let timer: number | undefined
  let controller: AbortController | undefined
  let failures = 0
  // Guards against two loops existing at once if start() is called twice, which is easy to do from
  // a React effect that reruns.
  let generation = 0

  function clearTimer() {
    if (timer !== undefined) {
      cancel(timer)
      timer = undefined
    }
  }

  async function tick(current: number) {
    if (!running || current !== generation || inFlight) return
    inFlight = true
    controller = new AbortController()
    const signal = controller.signal
    try {
      const value = await options.run(signal)
      if (!running || current !== generation || signal.aborted) return
      failures = 0
      options.onResult?.(value)
    } catch (error) {
      if (!running || current !== generation || signal.aborted) return
      failures += 1
      options.onError?.(error)
    } finally {
      inFlight = false
      controller = undefined
      // A response that arrives after the loop was stopped — a navigation, an unmount — must not
      // resurrect it.
      if (running && current === generation) {
        const backoff = failures > 0 ? Math.min(options.intervalMs * 2 ** failures, maxBackoff) : options.intervalMs
        clearTimer()
        timer = schedule(() => tick(current), backoff)
      }
    }
  }

  return {
    start() {
      if (running) return
      running = true
      generation += 1
      failures = 0
      void tick(generation)
    },
    stop() {
      running = false
      generation += 1
      clearTimer()
      controller?.abort()
      controller = undefined
      inFlight = false
    },
    refresh() {
      if (!running) return
      clearTimer()
      controller?.abort()
      generation += 1
      inFlight = false
      void tick(generation)
    },
    isRunning: () => running,
    isInFlight: () => inFlight,
  }
}

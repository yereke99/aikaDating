import { useCallback, useEffect, useRef, useState } from 'react'
import { remainingMs, serverNow } from './lib/cooldown'
import { pushBackHandler } from './telegram'

/** Binds Telegram's native back button while `active`, stacking under any sheet opened later. */
export function useBackButton(active: boolean, onBack: () => void) {
  const latest = useRef(onBack)
  latest.current = onBack
  useEffect(() => {
    if (!active) return undefined
    return pushBackHandler(() => latest.current())
  }, [active])
}

/**
 * Milliseconds left on a server deadline, re-rendered once a second and stopped exactly at zero.
 * The interval only exists while a countdown is running, so an idle screen schedules no timers.
 */
export function useCountdown(deadline: string | undefined): number {
  const [left, setLeft] = useState(() => remainingMs(deadline))

  useEffect(() => {
    const current = remainingMs(deadline)
    setLeft(current)
    if (current <= 0) return undefined
    const timer = window.setInterval(() => {
      const next = remainingMs(deadline)
      setLeft(next)
      if (next <= 0) window.clearInterval(timer)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [deadline])

  return left
}

/**
 * True while the Mini App is actually on screen. Telegram fires its own activated/deactivated
 * events on top of the document's visibility change, and both matter: a backgrounded app should
 * not keep polling, and a resumed one should refresh immediately.
 */
export function useAppVisible(): boolean {
  const [visible, setVisible] = useState(() => !document.hidden)

  useEffect(() => {
    const sync = () => setVisible(!document.hidden)
    document.addEventListener('visibilitychange', sync)
    window.addEventListener('focus', sync)
    window.addEventListener('pageshow', sync)
    return () => {
      document.removeEventListener('visibilitychange', sync)
      window.removeEventListener('focus', sync)
      window.removeEventListener('pageshow', sync)
    }
  }, [])

  return visible
}

/** Guards a state update against a component that unmounted while a request was in flight. */
export function useMounted() {
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  return useCallback(() => mounted.current, [])
}

/** Latest server timestamp as a value, for components that need to recompute on demand. */
export function nowOnServer() {
  return serverNow()
}

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { APIError, NEARBY_MAX_LIMIT, NEARBY_PAGE_SIZE, api } from './api'
import { useAppVisible } from './hooks'
import { ActionType, Cooldowns, mergeCooldowns, pruneCooldowns } from './lib/cooldown'
import { appendPage, mergeWindow, sameList } from './lib/merge'
import { createPoller } from './lib/poller'
import type { PublicProfile } from './types'

export const POLL_INTERVAL_MS = 2000

interface FeedOptions {
  enabled: boolean
  radius: number
  gender: string
}

/**
 * The nearby feed and its two-second refresh.
 *
 * The loop only exists while the tab is on screen and the app is in the foreground, it never has
 * more than one request outstanding, and its results are merged rather than assigned, so the list
 * updates without the cards flickering, duplicating, or losing the reader's place.
 */
export function useNearbyFeed({ enabled, radius, gender }: FeedOptions) {
  const [profiles, setProfiles] = useState<PublicProfile[]>([])
  const [page, setPage] = useState(1)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [offline, setOffline] = useState(false)
  const [cooldowns, setCooldowns] = useState<Record<string, Cooldowns>>({})
  const visible = useAppVisible()

  // Refs the poll callback reads: keeping them out of the effect's dependencies is what stops the
  // loop being torn down and recreated on every refresh it performs.
  const state = useRef({ profiles, page, radius, gender })
  state.current = { profiles, page, radius, gender }
  const etag = useRef('')
  const requestKey = `${radius}|${gender}`
  const activeKey = useRef(requestKey)

  const applyCooldowns = useCallback((users: PublicProfile[]) => {
    setCooldowns((current) => {
      const next = { ...current }
      for (const user of users) {
        const incoming: Cooldowns = {}
        if (user.like_next_allowed_at) incoming.like = user.like_next_allowed_at
        if (user.message_next_allowed_at) incoming.message = user.message_next_allowed_at
        const merged = pruneCooldowns(mergeCooldowns(current[user.id], incoming))
        if (Object.keys(merged).length > 0) next[user.id] = merged
        else delete next[user.id]
      }
      return next
    })
  }, [])

  /** Records a deadline the user just earned, or one the server reported when refusing. */
  const noteCooldown = useCallback((userID: string, action: ActionType, nextAllowedAt?: string) => {
    if (!nextAllowedAt) return
    setCooldowns((current) => ({ ...current, [userID]: mergeCooldowns(current[userID], { [action]: nextAllowedAt }) }))
  }, [])

  // First load, and any reload caused by a filter change. Polling picks up from here.
  useEffect(() => {
    if (!enabled) return undefined
    let active = true
    activeKey.current = requestKey
    etag.current = ''
    setLoading(true)
    setError('')
    api
      .nearby(radius, 1, gender)
      .then((result) => {
        if (!active) return
        setProfiles(result.users)
        setPage(1)
        setHasMore(result.has_more)
        applyCooldowns(result.users)
        setOffline(false)
      })
      .catch((caught) => {
        if (active) setError(caught instanceof Error ? caught.message : 'network_error')
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [enabled, radius, gender, requestKey, applyCooldowns])

  const poller = useMemo(
    () =>
      createPoller({
        intervalMs: POLL_INTERVAL_MS,
        run: (signal) => {
          const current = state.current
          // The refresh covers everything already on screen, up to what one request may return.
          const limit = Math.min(NEARBY_MAX_LIMIT, Math.max(NEARBY_PAGE_SIZE, current.profiles.length))
          return api.nearbyRevalidated(current.radius, current.gender, limit, etag.current, signal).then((result) => ({ result, limit }))
        },
        onResult: ({ result, limit }) => {
          etag.current = result.etag
          setOffline(false)
          // 304: the server confirmed nothing changed, so no state is touched at all — no rerender,
          // no image reload, no interrupted swipe.
          if (!result.data) return
          setProfiles((current) => {
            const merged = mergeWindow(current, result.data!.users, limit)
            return sameList(current, merged) ? current : merged
          })
          setHasMore((current) => (result.data!.users.length >= limit ? result.data!.has_more : current))
          applyCooldowns(result.data.users)
        },
        onError: (caught) => {
          // The list stays on screen through a blip; only a persistent failure is surfaced, and the
          // poller's own backoff keeps it from retrying every two seconds.
          if (caught instanceof DOMException && caught.name === 'AbortError') return
          if (caught instanceof APIError && caught.status === 401) return
          setOffline(true)
        },
      }),
    [applyCooldowns],
  )

  useEffect(() => {
    if (!enabled || !visible) {
      poller.stop()
      return undefined
    }
    poller.start()
    return () => poller.stop()
  }, [enabled, visible, poller])

  // A filter change invalidates the entity tag and the in-flight request together.
  useEffect(() => {
    if (activeKey.current === requestKey) return
    etag.current = ''
    poller.refresh()
  }, [requestKey, poller])

  const loadMore = useCallback(async () => {
    setLoading(true)
    try {
      const result = await api.nearby(state.current.radius, state.current.page + 1, state.current.gender)
      setProfiles((current) => appendPage(current, result.users))
      setPage(result.page)
      setHasMore(result.has_more)
      applyCooldowns(result.users)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'network_error')
    } finally {
      setLoading(false)
    }
  }, [applyCooldowns])

  const reload = useCallback(() => {
    etag.current = ''
    poller.refresh()
    setError('')
  }, [poller])

  return { profiles, hasMore, loading, error, offline, cooldowns, noteCooldown, loadMore, reload }
}

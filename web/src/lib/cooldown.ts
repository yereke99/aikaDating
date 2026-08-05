/**
 * Countdown maths for the like and message timers.
 *
 * Every deadline comes from the server as an RFC3339 instant. The device clock may be wrong by
 * minutes, so the offset between the two clocks is measured once and applied to every reading:
 * a countdown then reflects the server's opinion of the remaining time, which is the only one that
 * decides whether the next attempt succeeds.
 */

let clockOffsetMs = 0

/** Records the difference between the server clock and this device's. */
export function syncServerTime(serverTime: string | undefined) {
  if (!serverTime) return
  const server = Date.parse(serverTime)
  if (Number.isNaN(server)) return
  clockOffsetMs = server - Date.now()
}

export function serverNow(): number {
  return Date.now() + clockOffsetMs
}

/** Milliseconds left until a deadline, never negative. Returns 0 for a missing or invalid value. */
export function remainingMs(deadline: string | undefined, now = serverNow()): number {
  if (!deadline) return 0
  const target = Date.parse(deadline)
  if (Number.isNaN(target)) return 0
  return Math.max(0, target - now)
}

export function isActive(deadline: string | undefined, now = serverNow()): boolean {
  return remainingMs(deadline, now) > 0
}

/** `mm:ss` for anything under an hour, `h:mm:ss` above it. Seconds are rounded up. */
export function formatRemaining(milliseconds: number): string {
  const total = Math.ceil(Math.max(0, milliseconds) / 1000)
  const seconds = total % 60
  const minutes = Math.floor(total / 60) % 60
  const hours = Math.floor(total / 3600)
  const pad = (value: number) => String(value).padStart(2, '0')
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`
}

export type ActionType = 'like' | 'message'

export interface Cooldowns {
  like?: string
  message?: string
}

/**
 * Merges a fresh set of deadlines into the known ones. A later deadline always wins, so a polling
 * response that was already in flight when the user acted cannot erase the timer they just started.
 */
export function mergeCooldowns(current: Cooldowns | undefined, incoming: Cooldowns | undefined): Cooldowns {
  const merged: Cooldowns = { ...current }
  for (const action of ['like', 'message'] as ActionType[]) {
    const next = incoming?.[action]
    if (!next) continue
    const existing = merged[action]
    if (!existing || Date.parse(next) > Date.parse(existing)) merged[action] = next
  }
  return merged
}

/** Drops deadlines that have already passed, so an idle map does not grow without bound. */
export function pruneCooldowns(cooldowns: Cooldowns, now = serverNow()): Cooldowns {
  const pruned: Cooldowns = {}
  if (isActive(cooldowns.like, now)) pruned.like = cooldowns.like
  if (isActive(cooldowns.message, now)) pruned.message = cooldowns.message
  return pruned
}

/** Reads the deadline out of a cooldown error body, whichever of the two shapes the server used. */
export function deadlineFromError(payload: unknown): { action: ActionType; nextAllowedAt: string } | null {
  if (!payload || typeof payload !== 'object') return null
  const body = payload as Record<string, unknown>
  const nested = (body.error ?? {}) as Record<string, unknown>
  const code = typeof body.code === 'string' ? body.code : typeof nested.code === 'string' ? nested.code : ''
  const nextAllowedAt =
    typeof body.next_allowed_at === 'string' ? body.next_allowed_at : typeof nested.next_allowed_at === 'string' ? nested.next_allowed_at : ''
  if (!nextAllowedAt) return null
  if (code === 'like_cooldown_active') return { action: 'like', nextAllowedAt }
  if (code === 'message_cooldown_active') return { action: 'message', nextAllowedAt }
  return null
}

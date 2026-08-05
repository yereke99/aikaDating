/**
 * Stable list merging for the two-second nearby refresh.
 *
 * A naive `setProfiles(response.users)` replaces every object on every poll, which remounts every
 * card, restarts every image request and makes the list flicker. Merging keeps the exact object
 * reference for a user whose data has not changed, so a memoised card does not even rerender.
 */

interface Identified {
  id: string
}

function unchanged<T>(current: T, incoming: T): boolean {
  return JSON.stringify(current) === JSON.stringify(incoming)
}

export function mergeById<T extends Identified>(current: T[], incoming: T[]): T[] {
  const incomingById = new Map(incoming.map((item) => [item.id, item]))

  // Users still present keep their current position, so a small change in distance does not
  // reshuffle the whole screen under the reader's thumb.
  const merged: T[] = []
  const keptIds = new Set<string>()
  for (const item of current) {
    const next = incomingById.get(item.id)
    if (!next) continue
    keptIds.add(item.id)
    merged.push(unchanged(item, next) ? item : next)
  }

  // Newcomers are inserted where the server ranked them rather than appended, so someone who is
  // genuinely closest still shows up near the top.
  incoming.forEach((item, index) => {
    if (keptIds.has(item.id)) return
    merged.splice(Math.min(index, merged.length), 0, item)
  })
  return merged
}

/**
 * Merges a refresh that only covers the first `windowSize` results. Anything the reader loaded
 * beyond that window is kept as it was: a poll of the first page is not evidence that page three
 * disappeared.
 */
export function mergeWindow<T extends Identified>(current: T[], incoming: T[], windowSize: number): T[] {
  const incomingIds = new Set(incoming.map((item) => item.id))
  const tail = current.filter((item, index) => index >= windowSize && !incomingIds.has(item.id))
  return [...mergeById(current, incoming), ...tail]
}

/** True when the merge produced exactly the previous list, so state does not need to be replaced. */
export function sameList<T extends Identified>(current: T[], merged: T[]): boolean {
  return current.length === merged.length && current.every((item, index) => item === merged[index])
}

/** Appends a page of results, ignoring anything already on screen. */
export function appendPage<T extends Identified>(current: T[], page: T[]): T[] {
  const known = new Set(current.map((item) => item.id))
  return [...current, ...page.filter((item) => !known.has(item.id))]
}

/** Moves an item within a list, used by the photo reordering controls. */
export function moveItem<T>(items: T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length) return items
  const next = [...items]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  return next
}

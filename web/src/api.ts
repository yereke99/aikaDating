import { syncServerTime } from './lib/cooldown'
import type { ActionResult, AdminStats, AdminUser, CooldownsResponse, Gallery, Me, NearbyPage, ProfileInput, PublicProfile } from './types'

let authorization = ''

export class APIError extends Error {
  code: string
  status: number
  /** Raw error body, so a caller can read cooldown details without re-parsing the response. */
  payload: unknown

  constructor(status: number, code: string, message: string, payload?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.payload = payload
  }
}

export function setAuthorization(value: string) {
  authorization = value
}

interface RequestOptions extends RequestInit {
  /** Set when the caller wants a 304 reported rather than treated as an error. */
  etag?: string
}

function send(path: string, options: RequestOptions = {}): Promise<Response> {
  const isFormData = options.body instanceof FormData
  return fetch(path, {
    ...options,
    headers: {
      Authorization: authorization,
      ...(options.body && !isFormData ? { 'Content-Type': 'application/json' } : {}),
      ...(options.etag ? { 'If-None-Match': options.etag } : {}),
      ...options.headers,
    },
  })
}

function failure(status: number, body: any): APIError {
  return new APIError(status, body?.error?.code ?? body?.code ?? 'network_error', body?.error?.message ?? 'Request failed', body)
}

/** Any response carrying the server clock keeps every countdown anchored to server time. */
function adopt(body: any) {
  if (body && typeof body === 'object' && typeof body.server_time === 'string') syncServerTime(body.server_time)
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const response = await send(path, options)
  const body = await response.json().catch(() => null)
  if (!response.ok) throw failure(response.status, body)
  adopt(body)
  return body as T
}

/** A polled list response: `null` data means the server confirmed nothing changed. */
export interface Revalidated<T> {
  data: T | null
  etag: string
}

async function revalidate<T>(path: string, etag: string, signal?: AbortSignal): Promise<Revalidated<T>> {
  const response = await send(path, { etag, signal })
  const nextTag = response.headers.get('ETag') ?? ''
  if (response.status === 304) return { data: null, etag: nextTag || etag }
  const body = await response.json().catch(() => null)
  if (!response.ok) throw failure(response.status, body)
  adopt(body)
  return { data: body as T, etag: nextTag }
}

export const NEARBY_PAGE_SIZE = 20
/** The server caps a page at 50, which is also the widest window a refresh may cover. */
export const NEARBY_MAX_LIMIT = 50

function nearbyPath(radius: number, page: number, gender: string, limit = NEARBY_PAGE_SIZE) {
  const params = new URLSearchParams({ radius_km: String(radius), page: String(page), limit: String(limit) })
  if (gender) params.set('gender', gender)
  return `/api/users/nearby?${params}`
}

export const api = {
  authenticate: () => request<Me>('/api/auth/telegram', { method: 'POST' }),
  me: () => request<Me>('/api/me'),
  updateProfile: (profile: ProfileInput) => request<Me>('/api/me', { method: 'PATCH', body: JSON.stringify(profile) }),
  updateLocation: (latitude: number, longitude: number) =>
    request<{ success: boolean }>('/api/me/location', { method: 'POST', body: JSON.stringify({ latitude, longitude }) }),

  /** Replaces the primary photo. Kept for the single-avatar flow that predates the gallery. */
  uploadPhoto: (photo: Blob) => {
    const body = new FormData()
    body.append('photo', photo, 'profile.jpg')
    return request<Me>('/api/me/photo', { method: 'POST', body })
  },
  photos: () => request<Gallery>('/api/me/photos'),
  addPhoto: (photo: Blob, signal?: AbortSignal) => {
    const body = new FormData()
    body.append('photo', photo, 'photo.jpg')
    return request<Gallery>('/api/me/photos', { method: 'POST', body, signal })
  },
  deletePhoto: (photoID: string) => request<Gallery>(`/api/me/photos/${encodeURIComponent(photoID)}`, { method: 'DELETE' }),
  setPrimaryPhoto: (photoID: string) => request<Gallery>(`/api/me/photos/${encodeURIComponent(photoID)}/primary`, { method: 'PATCH' }),
  reorderPhotos: (photoIDs: string[]) =>
    request<Gallery>('/api/me/photos/order', { method: 'PATCH', body: JSON.stringify({ photo_ids: photoIDs }) }),
  userPhotos: (id: string) => request<Gallery>(`/api/users/${encodeURIComponent(id)}/photos`),

  nearby: (radius: number, page: number, gender = '') => request<NearbyPage>(nearbyPath(radius, page, gender)),
  /** Polling variant: sends the previous entity tag, so an unchanged neighbourhood costs one 304. */
  nearbyRevalidated: (radius: number, gender: string, limit: number, etag: string, signal?: AbortSignal) =>
    revalidate<NearbyPage>(nearbyPath(radius, 1, gender, limit), etag, signal),

  publicProfile: (id: string) => request<PublicProfile>(`/api/users/${encodeURIComponent(id)}`),
  cooldowns: (id: string) => request<CooldownsResponse>(`/api/users/${encodeURIComponent(id)}/cooldowns`),
  like: (id: string) => request<ActionResult>(`/api/users/${encodeURIComponent(id)}/like`, { method: 'POST', body: JSON.stringify({}) }),
  message: (id: string, message: string) =>
    request<ActionResult>(`/api/users/${encodeURIComponent(id)}/message`, { method: 'POST', body: JSON.stringify({ message }) }),

  adminStats: () => request<AdminStats>('/api/admin/stats'),
  adminUsers: (search: string) =>
    request<{ users: AdminUser[]; has_more: boolean }>(`/api/admin/users?limit=50&search=${encodeURIComponent(search)}`),
}

import type { AdminStats, AdminUser, Me, NearbyPage, ProfileInput, PublicProfile } from './types'

let authorization = ''

export class APIError extends Error {
  code: string
  status: number

  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export function setAuthorization(value: string) {
  authorization = value
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const isFormData = options.body instanceof FormData
  const response = await fetch(path, {
    ...options,
    headers: {
      Authorization: authorization,
      ...(options.body && !isFormData ? { 'Content-Type': 'application/json' } : {}),
      ...options.headers,
    },
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    throw new APIError(response.status, body?.error?.code ?? 'network_error', body?.error?.message ?? 'Request failed')
  }
  return body as T
}

export const api = {
  authenticate: () => request<Me>('/api/auth/telegram', { method: 'POST' }),
  me: () => request<Me>('/api/me'),
  updateProfile: (profile: ProfileInput) => request<Me>('/api/me', { method: 'PATCH', body: JSON.stringify(profile) }),
  updateLocation: (latitude: number, longitude: number) =>
    request<{ success: boolean }>('/api/me/location', { method: 'POST', body: JSON.stringify({ latitude, longitude }) }),
  uploadPhoto: (photo: Blob) => {
    const body = new FormData()
    body.append('photo', photo, 'profile.jpg')
    return request<Me>('/api/me/photo', { method: 'POST', body })
  },
  nearby: (radius: number, page: number, gender = '') => {
    const params = new URLSearchParams({ radius_km: String(radius), page: String(page), limit: '20' })
    if (gender) params.set('gender', gender)
    return request<NearbyPage>(`/api/users/nearby?${params}`)
  },
  publicProfile: (id: string) => request<PublicProfile>(`/api/users/${encodeURIComponent(id)}`),
  like: (id: string, message?: string) =>
    request<{ success: boolean; message: string }>(`/api/users/${encodeURIComponent(id)}/like`, {
      method: 'POST',
      body: JSON.stringify(message === undefined ? {} : { message }),
    }),
  adminStats: () => request<AdminStats>('/api/admin/stats'),
  adminUsers: (search: string) => request<{ users: AdminUser[]; has_more: boolean }>(`/api/admin/users?limit=50&search=${encodeURIComponent(search)}`),
}

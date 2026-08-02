export type Language = 'ru' | 'kk' | 'en'
export type Gender = 'male' | 'female' | 'other'

export interface Me {
  id: string
  telegram_user_id: number
  username?: string
  first_name?: string
  last_name?: string
  display_name?: string
  photo_url?: string
  telegram_photo_url?: string
  custom_photo_url?: string
  app_language: Language
  gender?: Gender
  birth_date?: string
  purpose?: string
  bio?: string
  is_profile_completed: boolean
  is_active: boolean
  is_admin: boolean
  location_available: boolean
  location_updated_at?: string
}

export interface ProfileInput {
  display_name: string
  gender: Gender | ''
  birth_date: string
  purpose: string
  bio: string
  custom_photo_url: string
  app_language: Language
  is_active: boolean
}

export interface PublicProfile {
  id: string
  display_name: string
  username?: string
  photo_url?: string
  age?: number
  gender?: Gender
  purpose?: string
  bio?: string
  distance_km?: number
}

export interface NearbyPage {
  users: PublicProfile[]
  page: number
  has_more: boolean
}

export interface AdminStats {
  total: number
  completed: number
  incomplete: number
  active: number
  blocked: number
  by_gender: Record<string, number>
  by_language: Record<string, number>
}

export interface AdminUser {
  telegram_user_id: number
  username?: string
  display_name: string
  gender?: string
  age?: number
  purpose?: string
  registered_at: string
  last_seen_at: string
  location_available: boolean
  profile_completed: boolean
  is_active: boolean
  is_blocked: boolean
}

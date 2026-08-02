interface TelegramUser {
  id: number
  first_name?: string
  last_name?: string
  username?: string
  language_code?: string
  photo_url?: string
}

interface LocationData {
  latitude: number
  longitude: number
}

interface LocationManager {
  isInited: boolean
  isLocationAvailable: boolean
  isAccessRequested: boolean
  isAccessGranted: boolean
  init(callback?: () => void): LocationManager
  getLocation(callback: (location: LocationData | null) => void): LocationManager
  openSettings(): LocationManager
}

export interface TelegramWebApp {
  initData: string
  initDataUnsafe: { user?: TelegramUser; start_param?: string }
  colorScheme: 'light' | 'dark'
  version: string
  platform: string
  isFullscreen?: boolean
  safeAreaInset?: Record<'top' | 'bottom' | 'left' | 'right', number>
  contentSafeAreaInset?: Record<'top' | 'bottom' | 'left' | 'right', number>
  LocationManager?: LocationManager
  HapticFeedback?: { notificationOccurred(type: 'error' | 'success' | 'warning'): void }
  ready(): void
  expand(): void
  requestFullscreen?: () => void
  disableVerticalSwipes?: () => void
  setHeaderColor?: (color: string) => void
  setBackgroundColor?: (color: string) => void
  onEvent(event: string, callback: (...args: unknown[]) => void): void
  offEvent(event: string, callback: (...args: unknown[]) => void): void
  isVersionAtLeast(version: string): boolean
}

declare global {
  interface Window {
    Telegram?: { WebApp?: TelegramWebApp }
  }
}

export class LocationFailure extends Error {
  code: 'location_denied' | 'location_unsupported' | 'location_timeout' | 'location_unavailable'
  constructor(code: LocationFailure['code']) {
    super(code)
    this.code = code
  }
}

function setInsets(webApp: TelegramWebApp) {
  const root = document.documentElement
  const safe = webApp.safeAreaInset
  const content = webApp.contentSafeAreaInset
  for (const edge of ['top', 'right', 'bottom', 'left'] as const) {
    root.style.setProperty(`--tg-safe-${edge}`, `${safe?.[edge] ?? 0}px`)
    root.style.setProperty(`--tg-content-safe-${edge}`, `${content?.[edge] ?? 0}px`)
  }
}

export function initializeTelegram(): TelegramWebApp | undefined {
  const webApp = window.Telegram?.WebApp
  if (!webApp) return undefined
  webApp.ready()
  webApp.expand()
  const updateNativeColors = () => {
    try { webApp.setHeaderColor?.('bg_color') } catch { /* older client */ }
    try { webApp.setBackgroundColor?.('bg_color') } catch { /* older client */ }
  }
  updateNativeColors()
  try { webApp.disableVerticalSwipes?.() } catch { /* older client */ }
  const updateInsets = () => setInsets(webApp)
  webApp.onEvent('safeAreaChanged', updateInsets)
  webApp.onEvent('contentSafeAreaChanged', updateInsets)
  webApp.onEvent('themeChanged', updateNativeColors)
  webApp.onEvent('fullscreenChanged', () => {
    document.documentElement.dataset.fullscreen = webApp.isFullscreen ? 'active' : 'expanded'
    updateInsets()
  })
  webApp.onEvent('fullscreenFailed', () => {
    document.documentElement.dataset.fullscreen = 'unavailable'
    webApp.expand()
    updateInsets()
  })
  setInsets(webApp)
  if (webApp.isVersionAtLeast?.('8.0') && webApp.requestFullscreen) {
    try { webApp.requestFullscreen() } catch { /* usable expanded fallback */ }
  }
  return webApp
}

function locationWithTelegram(manager: LocationManager): Promise<LocationData> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new LocationFailure('location_timeout')), 15_000)
    const request = () => {
      if (!manager.isLocationAvailable) {
        window.clearTimeout(timer)
        reject(new LocationFailure('location_unsupported'))
        return
      }
      manager.getLocation((location) => {
        window.clearTimeout(timer)
        if (location) resolve(location)
        else reject(new LocationFailure(manager.isAccessRequested && !manager.isAccessGranted ? 'location_denied' : 'location_unavailable'))
      })
    }
    if (manager.isInited) request()
    else manager.init(request)
  })
}

function locationWithBrowser(): Promise<LocationData> {
  if (!navigator.geolocation) return Promise.reject(new LocationFailure('location_unsupported'))
  return new Promise((resolve, reject) => {
    navigator.geolocation.getCurrentPosition(
      ({ coords }) => resolve({ latitude: coords.latitude, longitude: coords.longitude }),
      (error) => {
        if (error.code === error.PERMISSION_DENIED) reject(new LocationFailure('location_denied'))
        else if (error.code === error.TIMEOUT) reject(new LocationFailure('location_timeout'))
        else reject(new LocationFailure('location_unavailable'))
      },
      { enableHighAccuracy: true, timeout: 15_000, maximumAge: 60_000 },
    )
  })
}

export function requestLocation(webApp?: TelegramWebApp): Promise<LocationData> {
  if (webApp?.LocationManager) return locationWithTelegram(webApp.LocationManager)
  return locationWithBrowser()
}

export function openLocationSettings(webApp?: TelegramWebApp) {
  try { webApp?.LocationManager?.openSettings() } catch { /* unavailable */ }
}

export function startParameter(webApp?: TelegramWebApp): string {
  return webApp?.initDataUnsafe.start_param ?? new URLSearchParams(window.location.search).get('tgWebAppStartParam') ?? ''
}

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

interface BackButton {
  isVisible: boolean
  show(): BackButton
  hide(): BackButton
  onClick(callback: () => void): BackButton
  offClick(callback: () => void): BackButton
}

interface HapticFeedback {
  impactOccurred(style: 'light' | 'medium' | 'heavy' | 'rigid' | 'soft'): void
  notificationOccurred(type: 'error' | 'success' | 'warning'): void
  selectionChanged(): void
}

type Edge = 'top' | 'right' | 'bottom' | 'left'
type Insets = Record<Edge, number>

export interface TelegramWebApp {
  initData: string
  initDataUnsafe: { user?: TelegramUser; start_param?: string }
  colorScheme: 'light' | 'dark'
  version: string
  platform: string
  isExpanded?: boolean
  isFullscreen?: boolean
  viewportHeight?: number
  viewportStableHeight?: number
  safeAreaInset?: Partial<Insets>
  contentSafeAreaInset?: Partial<Insets>
  LocationManager?: LocationManager
  HapticFeedback?: HapticFeedback
  BackButton?: BackButton
  ready(): void
  expand(): void
  disableVerticalSwipes?: () => void
  setHeaderColor?: (color: string) => void
  setBackgroundColor?: (color: string) => void
  setBottomBarColor?: (color: string) => void
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

const EDGES: Edge[] = ['top', 'right', 'bottom', 'left']

/** Telegram reports insets as numbers, but older clients can send null/NaN for a missing edge. */
function inset(source: Partial<Insets> | undefined, edge: Edge): number {
  const value = source?.[edge]
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.round(value) : 0
}

function measuredHeight(): number {
  const visual = window.visualViewport?.height
  const height = typeof visual === 'number' && visual > 0 ? visual : window.innerHeight
  return Math.round(height)
}

/**
 * Single source of truth for the viewport. Every layout offset in styles.css is derived from the
 * custom properties written here, so no rule has to re-guess a device dimension.
 *
 * `--app-safe-*` is the device safe area (notch, home indicator). `--app-content-*` is the area
 * Telegram's own floating controls occupy. Telegram documents the two as additive, so styles.css
 * sums them into `--app-inset-*` rather than taking whichever is larger.
 */
class Viewport {
  private app?: TelegramWebApp
  private frame = 0
  private readonly sync = () => this.schedule()

  attach(app?: TelegramWebApp) {
    this.app = app
    if (app) {
      for (const event of ['viewportChanged', 'safeAreaChanged', 'contentSafeAreaChanged', 'fullscreenChanged', 'activated']) {
        app.onEvent(event, this.sync)
      }
    }
    window.visualViewport?.addEventListener('resize', this.sync)
    window.visualViewport?.addEventListener('scroll', this.sync)
    window.addEventListener('resize', this.sync)
    window.addEventListener('orientationchange', this.sync)
    this.apply()
  }

  private schedule() {
    if (this.frame) return
    this.frame = window.requestAnimationFrame(() => {
      this.frame = 0
      this.apply()
    })
  }

  private apply() {
    const root = document.documentElement
    const app = this.app
    const fallback = measuredHeight()

    // Written under our own names so the client's --tg-*-inset-* variables stay intact as the
    // fallback layer in styles.css. An edge we cannot measure is removed rather than zeroed,
    // which lets that fallback take over instead of being overridden with a wrong 0px.
    write(root, '--app-safe', app?.safeAreaInset)
    write(root, '--app-content', app?.contentSafeAreaInset)

    // Stable height ignores the on-screen keyboard, so the shell never jumps while typing.
    // Live height tracks it, so bottom sheets can lift above the keyboard.
    root.style.setProperty('--app-vh-stable', `${usable(app?.viewportStableHeight, fallback)}px`)
    root.style.setProperty('--app-vh', `${usable(app?.viewportHeight, fallback)}px`)
  }
}

function write(root: HTMLElement, prefix: string, source: Partial<Insets> | undefined) {
  for (const edge of EDGES) {
    if (source) root.style.setProperty(`${prefix}-${edge}`, `${inset(source, edge)}px`)
    else root.style.removeProperty(`${prefix}-${edge}`)
  }
}

/**
 * Clients can report 0 or undefined for a frame during startup. Anything positive is trusted as-is:
 * a landscape viewport with the keyboard open really is only ~90px tall, and clamping that to an
 * invented minimum would push the sheet back under the keyboard.
 */
function usable(reported: number | undefined, fallback: number): number {
  return typeof reported === 'number' && Number.isFinite(reported) && reported > 0 ? Math.round(reported) : fallback
}

const viewport = new Viewport()

let webApp: TelegramWebApp | undefined

/**
 * Boots the Mini App runtime exactly once. Safe to call outside Telegram: the viewport controller
 * then falls back to the visual viewport so the same layout works in a plain browser.
 */
export function initializeTelegram(): TelegramWebApp | undefined {
  const app = window.Telegram?.WebApp
  webApp = app

  if (!app) {
    viewport.attach(undefined)
    return undefined
  }

  app.ready()
  app.expand()

  // Deliberately no requestFullscreen(): in fullscreen Telegram draws its close/collapse/menu
  // controls over the webview, which is what buried the app header. Expanded mode keeps the
  // native controls in their own bar above the content.
  call(() => app.disableVerticalSwipes?.())

  const paintNativeChrome = () => {
    call(() => app.setHeaderColor?.('bg_color'))
    call(() => app.setBackgroundColor?.('bg_color'))
    call(() => app.setBottomBarColor?.('bg_color'))
  }
  paintNativeChrome()
  app.onEvent('themeChanged', paintNativeChrome)

  viewport.attach(app)
  return app
}

function call(action: () => void) {
  try {
    action()
  } catch {
    /* older clients throw on unsupported methods; the layout works without them */
  }
}

export function haptic(kind: 'success' | 'error' | 'warning' | 'select' | 'tap') {
  const feedback = webApp?.HapticFeedback
  if (!feedback) return
  call(() => {
    if (kind === 'select') feedback.selectionChanged()
    else if (kind === 'tap') feedback.impactOccurred('light')
    else feedback.notificationOccurred(kind)
  })
}

/**
 * Telegram exposes a single BackButton, but sheets can stack on top of a wizard step. A stack keeps
 * the topmost consumer in control and restores the previous one when it unmounts.
 */
const backHandlers: Array<() => void> = []

function runTopBackHandler() {
  backHandlers[backHandlers.length - 1]?.()
}

function syncBackButton() {
  const button = webApp?.BackButton
  if (!button) return
  call(() => (backHandlers.length ? button.show() : button.hide()))
}

export function pushBackHandler(handler: () => void): () => void {
  const button = webApp?.BackButton
  if (backHandlers.length === 0) call(() => button?.onClick(runTopBackHandler))
  backHandlers.push(handler)
  syncBackButton()
  return () => {
    const index = backHandlers.lastIndexOf(handler)
    if (index >= 0) backHandlers.splice(index, 1)
    if (backHandlers.length === 0) call(() => button?.offClick(runTopBackHandler))
    syncBackButton()
  }
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

export function requestLocation(app?: TelegramWebApp): Promise<LocationData> {
  if (app?.LocationManager) return locationWithTelegram(app.LocationManager)
  return locationWithBrowser()
}

export function openLocationSettings(app?: TelegramWebApp) {
  call(() => app?.LocationManager?.openSettings())
}

export function startParameter(app?: TelegramWebApp): string {
  return app?.initDataUnsafe.start_param ?? new URLSearchParams(window.location.search).get('tgWebAppStartParam') ?? ''
}

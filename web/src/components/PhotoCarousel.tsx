import { PointerEvent as ReactPointerEvent, useEffect, useRef, useState } from 'react'
import { translator } from '../i18n'
import { haptic } from '../telegram'
import type { Language, Photo } from '../types'

/**
 * Swipeable photo viewer.
 *
 * Only the track's `transform` changes while a finger is down — no width, no left, no re-layout —
 * so dragging stays on the compositor and remains smooth on a low-end Android. `touch-action:
 * pan-y` in the stylesheet lets a vertical scroll through while claiming horizontal movement, which
 * is also what stops the swipe turning into the browser's back gesture.
 */
export function PhotoCarousel({
  photos,
  language,
  fallback,
  alt,
  fullscreen,
  onOpen,
}: {
  photos: Photo[]
  language: Language
  fallback?: string
  alt: string
  fullscreen?: boolean
  onOpen?: (index: number) => void
}) {
  const t = translator(language)
  const slides = photos.length > 0 ? photos : fallback ? [{ id: 'fallback', url: fallback } as Photo] : []
  const [index, setIndex] = useState(0)
  const [drag, setDrag] = useState(0)
  const track = useRef<HTMLDivElement>(null)
  const gesture = useRef({ startX: 0, startY: 0, pointer: -1, horizontal: false })

  // A gallery that shrinks — a photo deleted on another device — must not leave the view on a slide
  // that no longer exists.
  useEffect(() => {
    setIndex((current) => Math.min(current, Math.max(0, slides.length - 1)))
  }, [slides.length])

  if (slides.length === 0) return null

  const go = (next: number) => {
    const clamped = Math.max(0, Math.min(slides.length - 1, next))
    if (clamped !== index) haptic('select')
    setIndex(clamped)
  }

  function onPointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (slides.length < 2) return
    gesture.current = { startX: event.clientX, startY: event.clientY, pointer: event.pointerId, horizontal: false }
  }

  function onPointerMove(event: ReactPointerEvent<HTMLDivElement>) {
    const state = gesture.current
    if (state.pointer !== event.pointerId) return
    const deltaX = event.clientX - state.startX
    const deltaY = event.clientY - state.startY
    if (!state.horizontal) {
      // Wait until the direction is unambiguous, so a vertical scroll is never hijacked.
      if (Math.abs(deltaX) < 8 || Math.abs(deltaX) <= Math.abs(deltaY)) return
      state.horizontal = true
      event.currentTarget.setPointerCapture(event.pointerId)
    }
    const width = track.current?.clientWidth || 1
    // Resistance at both ends, so the first and last photo feel like edges instead of dead weight.
    const overscroll = (index === 0 && deltaX > 0) || (index === slides.length - 1 && deltaX < 0)
    setDrag(overscroll ? deltaX / 3 : Math.max(-width, Math.min(width, deltaX)))
  }

  function endGesture(event: ReactPointerEvent<HTMLDivElement>) {
    const state = gesture.current
    if (state.pointer !== event.pointerId) return
    const width = track.current?.clientWidth || 1
    const travelled = drag
    gesture.current = { startX: 0, startY: 0, pointer: -1, horizontal: false }
    setDrag(0)
    if (Math.abs(travelled) > Math.max(48, width * 0.18)) go(index + (travelled < 0 ? 1 : -1))
  }

  const offset = `calc(${-index * 100}% + ${drag}px)`

  return (
    <div className={`carousel${fullscreen ? ' carousel-full' : ''}`}>
      <div
        className="carousel-track"
        ref={track}
        style={{ transform: `translate3d(${offset}, 0, 0)`, transition: drag === 0 ? undefined : 'none' }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endGesture}
        onPointerCancel={endGesture}
      >
        {slides.map((photo, position) => (
          <Slide
            key={photo.id}
            photo={photo}
            alt={`${alt} — ${t('photo')} ${position + 1}`}
            // Only the current slide and its immediate neighbours are fetched; the rest stay empty
            // until they are one swipe away.
            active={Math.abs(position - index) <= 1}
            onOpen={onOpen ? () => onOpen(position) : undefined}
          />
        ))}
      </div>

      {slides.length > 1 && (
        <>
          <div className="carousel-dots" role="tablist" aria-label={alt}>
            {slides.map((photo, position) => (
              <button
                key={photo.id}
                type="button"
                role="tab"
                aria-selected={position === index}
                aria-label={`${t('photo')} ${position + 1}`}
                className={position === index ? 'active' : ''}
                onClick={() => go(position)}
              />
            ))}
          </div>
          <button type="button" className="carousel-arrow prev" aria-label={t('previousPhoto')} disabled={index === 0} onClick={() => go(index - 1)}>
            ‹
          </button>
          <button
            type="button"
            className="carousel-arrow next"
            aria-label={t('nextPhoto')}
            disabled={index === slides.length - 1}
            onClick={() => go(index + 1)}
          >
            ›
          </button>
        </>
      )}
    </div>
  )
}

function Slide({ photo, alt, active, onOpen }: { photo: Photo; alt: string; active: boolean; onOpen?: () => void }) {
  const [state, setState] = useState<'loading' | 'ready' | 'failed'>('loading')
  const image = active ? (
    <img
      src={photo.url}
      alt={alt}
      // The slot keeps its size before the bytes arrive, so nothing jumps as photos load in.
      className={state === 'ready' ? 'ready' : ''}
      loading="eager"
      decoding="async"
      draggable={false}
      onLoad={() => setState('ready')}
      onError={() => setState('failed')}
    />
  ) : null

  return (
    <div className="carousel-slide">
      {state !== 'failed' && image}
      {state !== 'ready' && <div className={`carousel-placeholder${state === 'failed' ? ' failed' : ''}`} aria-hidden="true" />}
      {onOpen && (
        <button type="button" className="carousel-open" aria-label={alt} onClick={onOpen}>
          <span className="visually-hidden">{alt}</span>
        </button>
      )}
    </div>
  )
}

/** Full-screen viewer layered above every other surface, closed with the button or Telegram's back. */
export function PhotoViewer({
  photos,
  language,
  alt,
  onClose,
}: {
  photos: Photo[]
  language: Language
  alt: string
  onClose: () => void
}) {
  const t = translator(language)
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="photo-viewer" role="dialog" aria-modal="true" aria-label={alt}>
      <PhotoCarousel photos={photos} language={language} alt={alt} fullscreen />
      <button type="button" className="viewer-close" aria-label={t('close')} onClick={onClose}>
        ✕
      </button>
    </div>
  )
}

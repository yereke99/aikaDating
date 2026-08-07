import { useRef, useState } from 'react'
import { APIError, api } from '../api'
import { MessageKey, translator } from '../i18n'
import { haptic } from '../telegram'
import type { Gallery, Language, Me, Photo } from '../types'

/** Only a fallback: the server sends the real cap with every gallery response. */
const DEFAULT_MAX_PHOTOS = 4

interface DecodedImage {
  source: CanvasImageSource
  width: number
  height: number
  release: () => void
}

/**
 * createImageBitmap decodes the File directly, with no blob: URL and therefore no dependency on
 * the page's img-src policy. The <img> path stays as the fallback for clients without it.
 */
async function decodeImage(file: File): Promise<DecodedImage> {
  if (typeof createImageBitmap === 'function') {
    try {
      const bitmap = await createImageBitmap(file)
      return { source: bitmap, width: bitmap.width, height: bitmap.height, release: () => bitmap.close() }
    } catch {
      /* unsupported source format; retry through the element decoder below */
    }
  }
  const objectURL = URL.createObjectURL(file)
  try {
    const image = new Image()
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve()
      image.onerror = () => reject(new Error('invalid_photo'))
      image.src = objectURL
    })
    return { source: image, width: image.naturalWidth, height: image.naturalHeight, release: () => URL.revokeObjectURL(objectURL) }
  } catch (error) {
    URL.revokeObjectURL(objectURL)
    throw error
  }
}

/**
 * Normalises whatever the picker returned — HEIC from an iPhone, WebP from a gallery app — into the
 * JPEG the server accepts, at a size worth storing. Doing it here also keeps the upload small on a
 * mobile connection.
 */
export async function normalizeProfilePhoto(file: File): Promise<Blob> {
  // Some Android gallery pickers hand over a file with an empty type; let the decoder judge those
  // rather than rejecting a perfectly valid image on a missing MIME string.
  if (file.type && !file.type.startsWith('image/')) throw new Error('invalid_photo')
  const decoded = await decodeImage(file)
  try {
    const maxSide = 1600
    const scale = Math.min(1, maxSide / Math.max(decoded.width, decoded.height))
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(decoded.width * scale))
    canvas.height = Math.max(1, Math.round(decoded.height * scale))
    const context = canvas.getContext('2d')
    if (!context) throw new Error('invalid_photo')
    context.drawImage(decoded.source, 0, 0, canvas.width, canvas.height)
    // The server only accepts JPEG or PNG, so the canvas re-encode is required, not cosmetic.
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.86))
    if (!blob) throw new Error('invalid_photo')
    return blob
  } finally {
    decoded.release()
  }
}

/** Maps a server error code to a localized line, falling back to a generic upload failure. */
function uploadMessage(error: unknown, t: (key: MessageKey) => string): string {
  if (error instanceof APIError) {
    const known: Partial<Record<string, MessageKey>> = {
      photo_limit_reached: 'maxPhotosReached',
      photo_too_large: 'photoTooLarge',
      invalid_photo: 'unsupportedImage',
      photo_required: 'photoRequiredLast',
      photo_order_mismatch: 'serverError',
    }
    const key = known[error.code]
    return key ? t(key) : error.message || t('uploadFailed')
  }
  return t('uploadFailed')
}

export function PhotoManager({
  me,
  language,
  onGallery,
  notify,
}: {
  me: Me
  language: Language
  onGallery: (gallery: Gallery) => void
  notify: (text: string) => void
}) {
  const t = translator(language)
  const photos = me.photos ?? []
  const maxPhotos = me.max_photos ?? DEFAULT_MAX_PHOTOS
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Upload feedback is per slot, not only a line of text: the grid shows which square is filling
  // and which one refused.
  const [uploading, setUploading] = useState(false)
  const [failed, setFailed] = useState(false)
  const galleryInput = useRef<HTMLInputElement>(null)
  const cameraInput = useRef<HTMLInputElement>(null)
  // One in-flight mutation at a time: a double tap on "add" or two quick reorders cannot interleave
  // and leave the gallery in an order nobody asked for.
  const pending = useRef(false)

  const slots = maxPhotos - photos.length
  const fallbackPhoto = photos.length === 0 ? me.photo_url : undefined

  async function run(action: () => Promise<Gallery>, success?: MessageKey) {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    setError('')
    setFailed(false)
    try {
      const gallery = await action()
      onGallery(gallery)
      haptic('success')
      if (success) notify(t(success))
    } catch (caught) {
      haptic('error')
      setError(uploadMessage(caught, t))
      setFailed(true)
    } finally {
      pending.current = false
      setBusy(false)
      setUploading(false)
    }
  }

  async function pick(input: HTMLInputElement) {
    const file = input.files?.[0]
    input.value = ''
    if (!file) return
    if (photos.length >= maxPhotos) {
      setError(t('maxPhotosReached'))
      haptic('error')
      return
    }
    // The slot switches to its uploading state before the file is even decoded, so a large photo
    // on a slow phone never looks like a tap that did nothing.
    setUploading(true)
    setFailed(false)
    let normalized: Blob
    try {
      normalized = await normalizeProfilePhoto(file)
    } catch {
      haptic('error')
      setError(t('unsupportedImage'))
      setUploading(false)
      setFailed(true)
      return
    }
    await run(() => api.addPhoto(normalized))
  }

  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= photos.length) return
    const order = photos.map((photo) => photo.id)
    ;[order[index], order[target]] = [order[target], order[index]]
    void run(() => api.reorderPhotos(order))
  }

  // The grid always renders exactly maxPhotos squares, so the block never changes height between
  // an empty profile and a full one and the rows can never end with an orphan tile.
  const occupied = photos.length + (uploading || failed ? 1 : 0) + (fallbackPhoto ? 1 : 0)
  const emptySlots = Math.max(0, maxPhotos - occupied)

  return (
    <div className="photo-manager">
      <div className="photo-grid">
        {photos.map((photo, index) => (
          <PhotoSlot
            key={photo.id}
            photo={photo}
            index={index}
            total={photos.length}
            language={language}
            busy={busy}
            onPrimary={() => void run(() => api.setPrimaryPhoto(photo.id), 'primaryChanged')}
            onDelete={() => void run(() => api.deletePhoto(photo.id), 'photoDeleted')}
            onMove={(direction) => move(index, direction)}
          />
        ))}
        {uploading && (
          <div className="photo-slot photo-slot-pending" role="status" aria-label={t('photoUploading')}>
            <i className="spinner" aria-hidden="true" />
            <small>{t('photoUploading')}</small>
          </div>
        )}
        {!uploading && failed && (
          <button
            type="button"
            className="photo-slot photo-slot-failed"
            disabled={busy}
            aria-label={t('photoFailed')}
            onClick={() => {
              setFailed(false)
              setError('')
              galleryInput.current?.click()
            }}
          >
            <span aria-hidden="true">!</span>
            <small>{t('photoFailed')}</small>
          </button>
        )}
        {fallbackPhoto && (
          // A Telegram avatar with no gallery photo yet: shown so the profile never looks empty,
          // and labelled so it is clear it is not one of the user's own photos.
          <figure className="photo-slot is-fallback">
            <img src={fallbackPhoto} alt="" loading="lazy" decoding="async" />
            <figcaption>{t('telegramPhoto')}</figcaption>
          </figure>
        )}
        {Array.from({ length: emptySlots }, (_, index) => (
          <button
            key={`empty-${index}`}
            type="button"
            className="photo-slot photo-slot-empty"
            disabled={busy || slots <= 0}
            aria-label={`${t('addPhoto')} — ${t('emptySlot')} ${photos.length + index + 1}`}
            onClick={() => galleryInput.current?.click()}
          >
            <span aria-hidden="true">+</span>
          </button>
        ))}
      </div>

      <div className="photo-actions">
        <button type="button" className="secondary-button" disabled={busy || slots <= 0} onClick={() => galleryInput.current?.click()}>
          {t('choosePhoto')}
        </button>
        <button
          type="button"
          className="secondary-button selfie-button"
          disabled={busy || slots <= 0}
          onClick={() => cameraInput.current?.click()}
        >
          ◎ {t('takeSelfie')}
        </button>
      </div>
      <span className="field-hint">
        {busy ? t('uploadingPhoto') : `${photos.length}/${maxPhotos} ${t('photoCount')} · ${t('photosHint')}`}
      </span>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}

      <input
        ref={galleryInput}
        className="visually-hidden"
        type="file"
        accept="image/jpeg,image/png,image/webp,image/*"
        onChange={(event) => void pick(event.currentTarget)}
      />
      <input
        ref={cameraInput}
        className="visually-hidden"
        type="file"
        accept="image/*"
        capture="user"
        onChange={(event) => void pick(event.currentTarget)}
      />
    </div>
  )
}

function PhotoSlot({
  photo,
  index,
  total,
  language,
  busy,
  onPrimary,
  onDelete,
  onMove,
}: {
  photo: Photo
  index: number
  total: number
  language: Language
  busy: boolean
  onPrimary: () => void
  onDelete: () => void
  onMove: (direction: -1 | 1) => void
}) {
  const t = translator(language)
  const [failed, setFailed] = useState(false)
  return (
    <figure className={`photo-slot${photo.is_primary ? ' is-primary' : ''}`}>
      {failed ? (
        <div className="photo-fallback" aria-hidden="true">
          ⌾
        </div>
      ) : (
        <img src={photo.thumb_url || photo.url} alt={`${t('photo')} ${index + 1}`} loading="lazy" decoding="async" onError={() => setFailed(true)} />
      )}
      {photo.is_primary && <span className="photo-badge">{t('primaryPhoto')}</span>}
      <figcaption className="photo-controls">
        <button type="button" disabled={busy || index === 0} aria-label={t('movePhotoLeft')} onClick={() => onMove(-1)}>
          ‹
        </button>
        {!photo.is_primary && (
          <button type="button" disabled={busy} aria-label={t('makePrimary')} onClick={onPrimary}>
            ★
          </button>
        )}
        <button type="button" className="danger" disabled={busy} aria-label={t('deletePhoto')} onClick={onDelete}>
          ✕
        </button>
        <button type="button" disabled={busy || index === total - 1} aria-label={t('movePhotoRight')} onClick={() => onMove(1)}>
          ›
        </button>
      </figcaption>
    </figure>
  )
}

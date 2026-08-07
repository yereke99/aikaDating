/**
 * Camera and microphone access.
 *
 * Nothing here opens a device speculatively: it is called only once a call has actually been
 * accepted by both sides, so the platform's permission prompt appears at the moment the user
 * expects it and never while they are only browsing profiles.
 */

export type MediaFailure = 'call_permission_denied' | 'call_no_device' | 'call_device_busy' | 'call_unsupported' | 'call_media_failed'

export class MediaAccessError extends Error {
  code: MediaFailure
  constructor(code: MediaFailure) {
    super(code)
    this.code = code
  }
}

export type Facing = 'user' | 'environment'

/** True when this WebView can do a peer-to-peer call at all. Checked before anything is opened. */
export function supportsCalls(): boolean {
  return (
    typeof RTCPeerConnection === 'function' &&
    typeof navigator !== 'undefined' &&
    typeof navigator.mediaDevices?.getUserMedia === 'function'
  )
}

/**
 * Constraints tuned for a phone on a mobile network. 720p is an upper bound, not a demand — every
 * dimension is `ideal`, so a device that cannot deliver it negotiates something smaller instead of
 * failing, and WebRTC keeps adapting the encoding to the link for the rest of the call.
 */
function constraints(facing: Facing): MediaStreamConstraints {
  return {
    video: {
      facingMode: facing,
      width: { ideal: 1280 },
      height: { ideal: 720 },
      frameRate: { ideal: 30, max: 30 },
    },
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  }
}

function classify(error: unknown): MediaFailure {
  const name = error instanceof Error ? error.name : ''
  switch (name) {
    case 'NotAllowedError':
    case 'SecurityError':
    case 'PermissionDeniedError':
      return 'call_permission_denied'
    case 'NotFoundError':
    case 'DevicesNotFoundError':
      return 'call_no_device'
    // The device exists but another app — or another tab — is holding it.
    case 'NotReadableError':
    case 'TrackStartError':
    case 'AbortError':
      return 'call_device_busy'
    case 'TypeError':
      return 'call_unsupported'
    default:
      return 'call_media_failed'
  }
}

/**
 * Opens the camera and microphone.
 *
 * A device that cannot satisfy the preferred constraints reports OverconstrainedError rather than
 * picking something else, so that one case retries with the plainest possible request instead of
 * failing a call that the hardware could have carried.
 */
export async function openCamera(facing: Facing): Promise<MediaStream> {
  if (!supportsCalls()) throw new MediaAccessError('call_unsupported')
  try {
    return await navigator.mediaDevices.getUserMedia(constraints(facing))
  } catch (error) {
    if (error instanceof Error && (error.name === 'OverconstrainedError' || error.name === 'ConstraintNotSatisfiedError')) {
      try {
        return await navigator.mediaDevices.getUserMedia({ video: true, audio: true })
      } catch (retry) {
        throw new MediaAccessError(classify(retry))
      }
    }
    throw new MediaAccessError(classify(error))
  }
}

/** Opens one replacement video track, used when the user flips between front and back cameras. */
export async function openCameraTrack(facing: Facing): Promise<MediaStreamTrack> {
  const stream = await openCamera(facing)
  const [track] = stream.getVideoTracks()
  if (!track) {
    stopStream(stream)
    throw new MediaAccessError('call_no_device')
  }
  // Only the video track is kept; the extra microphone track from this second request would
  // otherwise stay open and hold the device.
  for (const other of stream.getTracks()) {
    if (other !== track) other.stop()
  }
  return track
}

/**
 * Releases every device a stream holds. This is the step that turns the camera indicator off, so
 * it runs on every exit path from a call, not only the tidy one.
 */
export function stopStream(stream: MediaStream | null | undefined) {
  if (!stream) return
  for (const track of stream.getTracks()) {
    try {
      track.stop()
    } catch {
      /* a track already ended by the platform throws on some WebViews */
    }
  }
}

/** True when the device reports more than one camera, which is what the flip button needs. */
export async function hasMultipleCameras(): Promise<boolean> {
  if (typeof navigator.mediaDevices?.enumerateDevices !== 'function') return false
  try {
    const devices = await navigator.mediaDevices.enumerateDevices()
    return devices.filter((device) => device.kind === 'videoinput').length > 1
  } catch {
    return false
  }
}

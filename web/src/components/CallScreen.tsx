import { useEffect, useRef, useState } from 'react'
import type { CallCenter } from '../calls/useCallCenter'
import { useBackButton } from '../hooks'
import { MessageKey, translator } from '../i18n'
import type { Language } from '../types'
import { Avatar } from './Avatar'
import { CallEndIcon, FlipCameraIcon, MicIcon, MicOffIcon, VideoCallIcon, VideoOffIcon } from './icons'

/**
 * The video call screen.
 *
 * It is an overlay on top of the running app, not a route: the shell underneath stays mounted, so
 * ending a call returns to the exact tab, scroll position and sheet the user left. Its geometry
 * comes from the same viewport variables every other screen uses — the safe-area insets and the
 * live viewport height telegram.ts publishes — which is what keeps it inside Telegram's fullscreen
 * viewport without adding a second fullscreen implementation.
 */
export function CallScreen({ call, language }: { call: CallCenter; language: Language }) {
  const t = translator(language)
  const inCall = call.phase === 'preparing' || call.phase === 'connecting' || call.phase === 'connected'

  // Telegram's back button follows the topmost screen. During a call that is this one, and the
  // action it maps to is the same as the visible primary control.
  useBackButton(call.phase !== 'idle', () => {
    if (call.phase === 'incoming') call.decline()
    else if (call.phase === 'ended') call.dismiss()
    else call.hangUp()
  })

  if (call.phase === 'idle') return null

  return (
    <div className="call-screen" role="dialog" aria-modal="true" aria-label={t('videoCall')}>
      {inCall ? <ActiveCall call={call} language={language} /> : <CallPrompt call={call} language={language} />}
    </div>
  )
}

/** The invitation screens — incoming, outgoing and the closing summary. No device is open here. */
function CallPrompt({ call, language }: { call: CallCenter; language: Language }) {
  const t = translator(language)
  const name = call.peer?.display_name ?? ''

  const status: MessageKey = (() => {
    if (call.phase === 'ended') return call.failure || endedMessage(call.endReason)
    if (call.phase === 'joining') return 'callConnecting'
    return call.phase === 'incoming' ? 'incomingCall' : 'callWaiting'
  })()

  return (
    <div className="call-prompt">
      <div className="call-prompt-identity">
        <Avatar src={call.peer?.photo_url} name={name} size="large" />
        <h2>{name}</h2>
        <p role="status">{t(status)}</p>
        {(call.phase === 'outgoing' || call.phase === 'joining') && <span className="call-pulse" aria-hidden="true" />}
      </div>
      <div className="call-prompt-actions">
        {call.phase === 'incoming' && (
          <>
            <button type="button" className="call-action decline" disabled={call.busy} onClick={call.decline} aria-label={t('declineCall')}>
              <i aria-hidden="true"><CallEndIcon /></i>
              <span>{t('declineCall')}</span>
            </button>
            <button type="button" className="call-action accept" disabled={call.busy} onClick={call.accept} aria-label={t('acceptCall')}>
              <i aria-hidden="true"><VideoCallIcon /></i>
              <span>{t('acceptCall')}</span>
            </button>
          </>
        )}
        {(call.phase === 'outgoing' || call.phase === 'joining') && (
          <button type="button" className="call-action decline" disabled={call.busy} onClick={call.hangUp} aria-label={t('cancelCall')}>
            <i aria-hidden="true"><CallEndIcon /></i>
            <span>{t('cancelCall')}</span>
          </button>
        )}
        {call.phase === 'ended' && call.failure && (
          <button type="button" className="secondary-button" onClick={call.dismiss}>
            {t('close')}
          </button>
        )}
      </div>
    </div>
  )
}

function endedMessage(reason: string): MessageKey {
  switch (reason) {
    case 'rejected':
      return 'callDeclined'
    case 'cancelled':
      return 'callCancelled'
    case 'timeout':
      return 'callNoAnswer'
    case 'failed':
      return 'callFailed'
    case 'peer_disconnected':
      return 'callPeerLeft'
    default:
      return 'callEnded'
  }
}

/** The live call: remote video edge to edge, a small local preview, and four controls. */
function ActiveCall({ call, language }: { call: CallCenter; language: Language }) {
  const t = translator(language)
  const remote = useVideoStream(call.remoteStream)
  const local = useVideoStream(call.localStream)
  const duration = useCallDuration(call.phase === 'connected' ? call.connectedAt : 0)

  const status: MessageKey | '' =
    call.phase === 'preparing'
      ? 'callRequestingDevices'
      : call.phase === 'connecting'
        ? 'callConnecting'
        : call.interrupted
          ? 'callReconnecting'
          : ''

  return (
    <>
      {/* The remote stream fills the screen. It is attached straight to the element: no frame ever
          passes through React state or a canvas. */}
      <video ref={remote.ref} className="call-remote" autoPlay playsInline aria-label={call.peer?.display_name ?? t('videoCall')} />
      {!call.remoteStream && (
        <div className="call-remote-placeholder" aria-hidden="true">
          <Avatar src={call.peer?.photo_url} name={call.peer?.display_name ?? ''} size="large" />
        </div>
      )}

      {remote.blocked && (
        // Some WebViews refuse to start a stream with audio without a direct gesture. Rather than
        // showing a black rectangle, the screen asks for the one tap that unblocks it.
        <button type="button" className="call-unblock" onClick={remote.play}>
          {t('callTapToStart')}
        </button>
      )}

      <header className="call-head">
        <strong>{call.peer?.display_name}</strong>
        <span>{status ? t(status) : duration}</span>
      </header>

      <div className={`call-local${call.cameraOff ? ' is-off' : ''}`}>
        <video ref={local.ref} autoPlay playsInline muted aria-label={t('yourCamera')} />
        {call.cameraOff && <span aria-hidden="true">⊘</span>}
      </div>

      <div className="call-controls">
        <button
          type="button"
          className={`call-control${call.muted ? ' is-off' : ''}`}
          aria-pressed={call.muted}
          aria-label={call.muted ? t('unmuteMicrophone') : t('muteMicrophone')}
          onClick={call.toggleMicrophone}
        >
          <i aria-hidden="true">{call.muted ? <MicOffIcon /> : <MicIcon />}</i>
        </button>
        <button
          type="button"
          className={`call-control${call.cameraOff ? ' is-off' : ''}`}
          aria-pressed={call.cameraOff}
          aria-label={call.cameraOff ? t('turnCameraOn') : t('turnCameraOff')}
          onClick={call.toggleCamera}
        >
          <i aria-hidden="true">{call.cameraOff ? <VideoOffIcon /> : <VideoCallIcon />}</i>
        </button>
        <button
          type="button"
          className="call-control"
          disabled={!call.canFlipCamera || call.cameraOff}
          aria-label={t('switchCamera')}
          onClick={call.flipCamera}
        >
          <i aria-hidden="true"><FlipCameraIcon /></i>
        </button>
        <button type="button" className="call-control end" aria-label={t('endCall')} onClick={call.hangUp}>
          <i aria-hidden="true"><CallEndIcon /></i>
        </button>
      </div>
    </>
  )
}

/**
 * Binds a MediaStream to a video element and starts it.
 *
 * `blocked` covers the autoplay refusal some WebViews return for a stream carrying audio; the
 * caller shows a single tap target rather than leaving the user on a black screen.
 */
function useVideoStream(stream: MediaStream | null) {
  const ref = useRef<HTMLVideoElement>(null)
  const [blocked, setBlocked] = useState(false)

  useEffect(() => {
    const element = ref.current
    if (!element) return undefined
    element.srcObject = stream
    if (!stream) return undefined
    setBlocked(false)
    const started = element.play()
    if (started) started.then(() => setBlocked(false)).catch(() => setBlocked(true))
    return () => {
      element.srcObject = null
    }
  }, [stream])

  const play = () => {
    ref.current
      ?.play()
      .then(() => setBlocked(false))
      .catch(() => setBlocked(true))
  }

  return { ref, blocked, play }
}

/** mm:ss since the connection came up. The interval exists only while a call is connected. */
function useCallDuration(connectedAt: number): string {
  const [seconds, setSeconds] = useState(0)

  useEffect(() => {
    if (!connectedAt) {
      setSeconds(0)
      return undefined
    }
    const tick = () => setSeconds(Math.max(0, Math.floor((Date.now() - connectedAt) / 1000)))
    tick()
    const timer = window.setInterval(tick, 1000)
    return () => window.clearInterval(timer)
  }, [connectedAt])

  if (!connectedAt) return ''
  const minutes = Math.floor(seconds / 60)
  return `${minutes}:${String(seconds % 60).padStart(2, '0')}`
}

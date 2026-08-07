import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { APIError, api } from '../api'
import { useAppVisible } from '../hooks'
import type { MessageKey } from '../i18n'
import { createPoller } from '../lib/poller'
import { haptic } from '../telegram'
import { Facing, MediaAccessError, hasMultipleCameras, openCamera, openCameraTrack, stopStream, supportsCalls } from './media'
import { PeerSession } from './peer'
import type { CallEndReason, CallEvent, CallPeer, CallRecord, ICEServer } from './types'

/**
 * The client half of the call state machine. It mirrors internal/calls, but the server stays
 * authoritative: every transition here is either the answer to a request the server accepted or an
 * event the server pushed.
 */
export type CallPhase =
  /** No call. The signalling channel is open and listening for an invitation. */
  | 'idle'
  /** We invited someone and are waiting for them to accept. No device is open yet. */
  | 'outgoing'
  /** The callee opened the Mini App from Telegram and is joining the invitation. */
  | 'joining'
  /** Someone is calling us. No device is open yet — that is what accepting is for. */
  | 'incoming'
  /** Accepted; asking the platform for the camera and microphone. */
  | 'preparing'
  /** Devices are open and the peer connection is negotiating. */
  | 'connecting'
  | 'connected'
  /** Terminal, with a reason to show before the screen closes. */
  | 'ended'

interface CallView {
  phase: CallPhase
  callID: string
  peer: CallPeer | null
  outgoing: boolean
  endReason: CallEndReason | ''
  /** A local failure — permissions, no camera, an unusable WebView — as a translatable key. */
  failure: MessageKey | ''
  interrupted: boolean
  connectedAt: number
}

const IDLE: CallView = {
  phase: 'idle',
  callID: '',
  peer: null,
  outgoing: false,
  endReason: '',
  failure: '',
  interrupted: false,
  connectedAt: 0,
}

/** How long a finished call stays on screen before it closes itself. */
const ENDED_LINGER_MS = 1800

/** Maps a server refusal to the line the user sees. */
function inviteFailure(error: unknown): MessageKey {
  if (error instanceof APIError) {
    const known: Partial<Record<string, MessageKey>> = {
      peer_busy: 'callPeerBusy',
      call_busy: 'callAlreadyActive',
      calls_disabled: 'callsDisabled',
      self_call: 'callSelf',
      user_not_found: 'profileHidden',
      rate_limit_exceeded: 'callTooMany',
      profile_required: 'callProfileRequired',
    }
    return known[error.code] ?? 'callFailed'
  }
  return 'callFailed'
}

export interface CallCenter {
  enabled: boolean
  phase: CallPhase
  peer: CallPeer | null
  outgoing: boolean
  endReason: CallEndReason | ''
  failure: MessageKey | ''
  interrupted: boolean
  connectedAt: number
  localStream: MediaStream | null
  remoteStream: MediaStream | null
  muted: boolean
  cameraOff: boolean
  canFlipCamera: boolean
  /** True while a request is in flight, so a button can be disabled without freezing the screen. */
  busy: boolean
  start: (userID: string) => void
  accept: () => void
  decline: () => void
  hangUp: () => void
  dismiss: () => void
  toggleMicrophone: () => void
  toggleCamera: () => void
  flipCamera: () => void
}

export function useCallCenter(
  userID: string | undefined,
  notify: (text: string) => void,
  translate: (key: MessageKey) => string,
  launchCallID = '',
  onLaunchCallHandled?: (callID: string) => void,
): CallCenter {
  const [enabled, setEnabled] = useState(false)
  const [view, setView] = useState<CallView>(IDLE)
  const [localStream, setLocalStream] = useState<MediaStream | null>(null)
  const [remoteStream, setRemoteStream] = useState<MediaStream | null>(null)
  const [muted, setMuted] = useState(false)
  const [cameraOff, setCameraOff] = useState(false)
  const [canFlipCamera, setCanFlipCamera] = useState(false)
  const [busy, setBusy] = useState(false)
  const visible = useAppVisible()

  // Everything the event handler touches lives in refs: the signalling loop must not be torn down
  // and rebuilt every time the call state changes, and an event can land between two renders.
  const session = useRef<PeerSession | null>(null)
  const ice = useRef<ICEServer[]>([])
  const active = useRef({ id: '', outgoing: false })
  const facing = useRef<Facing>('user')
  const pendingOffer = useRef('')
  const pendingCandidates = useRef<RTCIceCandidateInit[]>([])
  const cursor = useRef(0)
  const lingerTimer = useRef(0)
  const inviteTimer = useRef(0)
  const inviteTimeoutMs = useRef(60_000)
  const handledLaunchCall = useRef('')
  const phase = useRef<CallPhase>('idle')
  phase.current = view.phase

  const clearTimers = useCallback(() => {
    window.clearTimeout(lingerTimer.current)
    window.clearTimeout(inviteTimer.current)
    lingerTimer.current = 0
    inviteTimer.current = 0
  }, [])

  /**
   * Releases every resource a call held. This runs on every exit — the End button, a remote
   * hang-up, a failure, an unmount — because anything less leaves the camera light on and makes
   * the next call fail to open the device.
   */
  const release = useCallback(() => {
    session.current?.close()
    session.current = null
    setLocalStream((current) => {
      stopStream(current)
      return null
    })
    setRemoteStream(null)
    pendingOffer.current = ''
    pendingCandidates.current = []
    active.current = { id: '', outgoing: false }
    setMuted(false)
    setCameraOff(false)
    facing.current = 'user'
  }, [])

  /** Ends the local side of a call and shows why, without contacting the server. */
  const finish = useCallback(
    (endReason: CallEndReason | '', failure: MessageKey | '' = '') => {
      window.clearTimeout(inviteTimer.current)
      inviteTimer.current = 0
      release()
      setView((current) => (current.phase === 'idle' ? current : { ...current, phase: 'ended', endReason, failure, interrupted: false }))
      // A failure is left on screen until it is read; an ordinary ending closes itself.
      if (!failure) {
        window.clearTimeout(lingerTimer.current)
        lingerTimer.current = window.setTimeout(() => setView(IDLE), ENDED_LINGER_MS)
      }
    },
    [release],
  )

  /** Tells the server we are leaving, then tears down locally regardless of the outcome. */
  const leave = useCallback(
    async (callID: string, action: 'reject' | 'end', endReason: CallEndReason) => {
      finish(endReason)
      if (!callID) return
      try {
        await (action === 'reject' ? api.rejectCall(callID) : api.endCall(callID))
      } catch {
        // The call is already over locally. A failed request only means the server will reap it
        // with its own timeout, so there is nothing useful to report here.
      }
    },
    [finish],
  )

  /**
   * Opens the devices and builds the peer connection. Called only after both sides agreed, which
   * is why the permission prompt cannot appear before a call is actually happening.
   */
  const negotiate = useCallback(
    async (callID: string, isCaller: boolean) => {
      setView((current) => ({ ...current, phase: 'preparing' }))
      let stream: MediaStream
      try {
        stream = await openCamera(facing.current)
      } catch (error) {
        haptic('error')
        const code = error instanceof MediaAccessError ? error.code : 'call_media_failed'
        // The other side is told immediately rather than being left on a connecting spinner.
        void api.endCall(callID).catch(() => undefined)
        finish('failed', code as MessageKey)
        return
      }
      // The call may have ended while the permission dialog was open.
      if (active.current.id !== callID) {
        stopStream(stream)
        return
      }

      const peerSession = new PeerSession(ice.current, {
        onCandidate: (candidate) => {
          void api.signalCall(callID, { type: 'ice_candidate', candidate }).catch(() => undefined)
        },
        onRemoteStream: (incoming) => setRemoteStream(incoming),
        onConnected: () => {
          setView((current) => {
            if (current.callID !== callID || current.phase === 'connected' || current.phase === 'ended') {
              return current.phase === 'connected' && current.interrupted ? { ...current, interrupted: false } : current
            }
            haptic('success')
            return { ...current, phase: 'connected', interrupted: false, connectedAt: Date.now() }
          })
          void api.callState(callID, 'connected').catch(() => undefined)
        },
        onFailed: () => {
          void api.callState(callID, 'failed').catch(() => undefined)
          finish('failed')
        },
        onInterrupted: (interrupted) =>
          setView((current) => (current.callID === callID && current.phase === 'connected' ? { ...current, interrupted } : current)),
      })
      session.current = peerSession
      peerSession.attachLocal(stream)
      setLocalStream(stream)
      void hasMultipleCameras().then(setCanFlipCamera)
      setView((current) => (current.callID === callID ? { ...current, phase: 'connecting' } : current))

      try {
        if (isCaller) {
          const sdp = await peerSession.createOffer()
          await api.signalCall(callID, { type: 'webrtc_offer', sdp })
        } else if (pendingOffer.current) {
          const sdp = pendingOffer.current
          pendingOffer.current = ''
          const answer = await peerSession.answerOffer(sdp)
          await api.signalCall(callID, { type: 'webrtc_answer', sdp: answer })
        }
        for (const candidate of pendingCandidates.current.splice(0)) {
          await peerSession.addCandidate(candidate)
        }
      } catch {
        void api.callState(callID, 'failed').catch(() => undefined)
        finish('failed')
      }
    },
    [finish],
  )

  const handleEvent = useCallback(
    async (event: CallEvent) => {
      const current = active.current
      switch (event.type) {
        case 'incoming_call': {
          // Our own state can be stale after a reconnect. Rather than silently opening a second
          // call screen, an invitation that arrives while we are busy is declined at once.
          if (phase.current !== 'idle' || current.id) {
            void api.rejectCall(event.call_id).catch(() => undefined)
            return
          }
          active.current = { id: event.call_id, outgoing: false }
          clearTimers()
          haptic('warning')
          setView({ ...IDLE, phase: 'incoming', callID: event.call_id, peer: event.peer ?? null, outgoing: false })
          return
        }
        case 'receiver_opened': {
          if (current.id !== event.call_id || !current.outgoing) return
          window.clearTimeout(inviteTimer.current)
          inviteTimer.current = 0
          setView((existing) =>
            existing.callID === event.call_id && existing.phase === 'outgoing'
              ? { ...existing, phase: 'joining', peer: event.peer ?? existing.peer }
              : existing,
          )
          return
        }
        case 'call_accepted': {
          if (current.id !== event.call_id || !current.outgoing) return
          window.clearTimeout(inviteTimer.current)
          inviteTimer.current = 0
          await negotiate(event.call_id, true)
          return
        }
        case 'webrtc_offer': {
          if (current.id !== event.call_id || !event.sdp) return
          // The offer can arrive before the camera prompt has been answered.
          if (!session.current) {
            pendingOffer.current = event.sdp
            return
          }
          try {
            const answer = await session.current.answerOffer(event.sdp)
            await api.signalCall(event.call_id, { type: 'webrtc_answer', sdp: answer })
            for (const candidate of pendingCandidates.current.splice(0)) {
              await session.current.addCandidate(candidate)
            }
          } catch {
            void api.callState(event.call_id, 'failed').catch(() => undefined)
            finish('failed')
          }
          return
        }
        case 'webrtc_answer': {
          if (current.id !== event.call_id || !event.sdp || !session.current) return
          try {
            await session.current.applyAnswer(event.sdp)
          } catch {
            void api.callState(event.call_id, 'failed').catch(() => undefined)
            finish('failed')
          }
          return
        }
        case 'ice_candidate': {
          if (current.id !== event.call_id || !event.candidate) return
          if (session.current) await session.current.addCandidate(event.candidate)
          else pendingCandidates.current.push(event.candidate)
          return
        }
        case 'call_connected':
          return
        case 'call_rejected':
        case 'call_cancelled':
        case 'call_ended': {
          if (current.id !== event.call_id) return
          haptic('warning')
          finish(event.reason ?? 'hangup')
          return
        }
      }
    },
    [clearTimers, finish, negotiate],
  )

  // One long poll at a time, restarted after each answer. The request itself parks on the server,
  // so the short interval below is only the gap between a response and the next request.
  const channel = useMemo(
    () =>
      createPoller({
        intervalMs: 150,
        run: (signal) => api.callEvents(cursor.current, signal),
        onResult: (result) => {
          if (result.reset) {
            // We fell behind what the server still buffers, so the transitions we missed are
            // unknown. Anything in flight is abandoned rather than resumed from a guess.
            cursor.current = result.cursor
            if (active.current.id) finish('failed')
            return
          }
          cursor.current = result.cursor
          for (const event of result.events) void handleEvent(event)
        },
        onError: () => undefined,
      }),
    [finish, handleEvent],
  )

  const resumeLaunchCall = useCallback(
    async (callID: string) => {
      if (!userID || phase.current !== 'idle') {
        onLaunchCallHandled?.(callID)
        return
      }
      setBusy(true)
      let openedLocally = false
      try {
        const opened = await api.openCall(callID)
        const call = opened.call
        if (call.callee.id !== userID || (call.status !== 'ringing' && call.status !== 'receiver_opened')) {
          notify(translate('callFailed'))
          return
        }
        ice.current = opened.ice_servers ?? ice.current
        active.current = { id: call.id, outgoing: false }
        openedLocally = true
        clearTimers()
        haptic('tap')
        setView({ ...IDLE, phase: 'preparing', callID: call.id, peer: call.caller, outgoing: false })

        const accepted = await api.acceptCall(call.id)
        ice.current = accepted.ice_servers ?? ice.current
        await negotiate(call.id, false)
      } catch (error) {
        haptic('error')
        if (openedLocally) finish('failed')
        notify(error instanceof APIError ? error.message : translate('callFailed'))
      } finally {
        setBusy(false)
        onLaunchCallHandled?.(callID)
      }
    },
    [clearTimers, finish, negotiate, notify, onLaunchCallHandled, translate, userID],
  )

  // Read the feature's availability once. A client that cannot do WebRTC at all never shows a
  // call button, rather than offering one that fails at the last step.
  //
  // The same response reports whether a call is already ringing for this user, which is what makes
  // the Telegram "Answer" button work: it opens the Mini App cold, and the app lands directly on
  // the incoming-call screen instead of the tab it happened to start on. It also restores a call
  // after an accidental reload.
  useEffect(() => {
    if (!userID) return
    let cancelled = false
    api
      .callConfig()
      .then((config) => {
        if (cancelled) return
        ice.current = config.ice_servers ?? []
        inviteTimeoutMs.current = Math.max(15, config.invite_timeout_seconds) * 1000
        const ready = config.enabled && supportsCalls()
        setEnabled(ready)
        if (!ready || phase.current !== 'idle') return
        if (launchCallID && handledLaunchCall.current !== launchCallID) {
          handledLaunchCall.current = launchCallID
          void resumeLaunchCall(launchCallID)
          return
        }
        if (!config.current) return
        const call = config.current
        // Only pre-negotiation invitations can be resumed. A call that was already accepted has a
        // peer connection that did not survive the reload, so it is left for the server to reap.
        if (call.status !== 'ringing' && call.status !== 'receiver_opened') return
        if (call.callee.id === userID) {
          active.current = { id: call.id, outgoing: false }
          haptic('warning')
          setView({ ...IDLE, phase: 'incoming', callID: call.id, peer: call.caller, outgoing: false })
          return
        }
        if (call.caller.id === userID) {
          active.current = { id: call.id, outgoing: true }
          clearTimers()
          setView({ ...IDLE, phase: call.status === 'receiver_opened' ? 'joining' : 'outgoing', callID: call.id, peer: call.callee, outgoing: true })
          if (call.status === 'ringing') {
            inviteTimer.current = window.setTimeout(() => finish('timeout'), inviteTimeoutMs.current + 10_000)
          }
        }
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [clearTimers, finish, launchCallID, resumeLaunchCall, userID])

  // The channel runs while the app is on screen, and keeps running through a call even if the
  // client reports itself hidden — dropping it there would make the server declare us gone.
  useEffect(() => {
    if (!enabled || !userID) return undefined
    const inCall = view.phase !== 'idle'
    if (!visible && !inCall) {
      channel.stop()
      return undefined
    }
    channel.start()
    return undefined
  }, [enabled, userID, visible, view.phase, channel])

  useEffect(
    () => () => {
      channel.stop()
      clearTimers()
      release()
    },
    [channel, clearTimers, release],
  )

  const start = useCallback(
    (targetID: string) => {
      if (!enabled || busy || phase.current !== 'idle') return
      setBusy(true)
      void api
        .createCall(targetID)
        .then((response) => {
          ice.current = response.ice_servers ?? ice.current
          active.current = { id: response.call.id, outgoing: true }
          clearTimers()
          haptic('tap')
          setView({ ...IDLE, phase: 'outgoing', callID: response.call.id, peer: response.call.callee, outgoing: true })
          // The server ends an unanswered invitation on its own; this is only the backstop for a
          // client whose channel is down and would otherwise ring forever.
          inviteTimer.current = window.setTimeout(() => finish('timeout'), inviteTimeoutMs.current + 10_000)
          channel.refresh()
        })
        .catch((error) => {
          haptic('error')
          // Both dialled at once: the server hands back the invitation that already exists, so we
          // answer that one instead of opening a second call.
          const payload = error instanceof APIError ? (error.payload as { call?: CallRecord } | null) : null
          if (error instanceof APIError && error.code === 'incoming_call_pending' && payload?.call) {
            active.current = { id: payload.call.id, outgoing: false }
            setView({ ...IDLE, phase: 'incoming', callID: payload.call.id, peer: payload.call.caller, outgoing: false })
            return
          }
          notify(translate(inviteFailure(error)))
        })
        .finally(() => setBusy(false))
    },
    [busy, channel, clearTimers, enabled, finish, notify, translate],
  )

  const accept = useCallback(() => {
    const callID = active.current.id
    if (busy || phase.current !== 'incoming' || !callID) return
    setBusy(true)
    void api
      .acceptCall(callID)
      .then(async (response) => {
        ice.current = response.ice_servers ?? ice.current
        await negotiate(callID, false)
      })
      .catch((error) => {
        haptic('error')
        notify(translate(inviteFailure(error)))
        finish('failed')
      })
      .finally(() => setBusy(false))
  }, [busy, finish, negotiate, notify, translate])

  const decline = useCallback(() => {
    const callID = active.current.id
    if (busy || !callID) return
    setBusy(true)
    void leave(callID, 'reject', 'rejected').finally(() => setBusy(false))
  }, [busy, leave])

  const hangUp = useCallback(() => {
    const callID = active.current.id
    if (!callID) {
      finish('')
      return
    }
    setBusy(true)
    void leave(callID, 'end', view.outgoing && (phase.current === 'outgoing' || phase.current === 'joining') ? 'cancelled' : 'hangup').finally(() => setBusy(false))
  }, [finish, leave, view.outgoing])

  const dismiss = useCallback(() => {
    clearTimers()
    setView(IDLE)
  }, [clearTimers])

  const toggleMicrophone = useCallback(() => {
    setMuted((current) => {
      const next = !current
      session.current?.setMicrophoneEnabled(!next)
      haptic('tap')
      return next
    })
  }, [])

  const toggleCamera = useCallback(() => {
    setCameraOff((current) => {
      const next = !current
      session.current?.setCameraEnabled(!next)
      haptic('tap')
      return next
    })
  }, [])

  const flipCamera = useCallback(() => {
    const peerSession = session.current
    if (!peerSession) return
    const next: Facing = facing.current === 'user' ? 'environment' : 'user'
    void openCameraTrack(next)
      .then(async (track) => {
        track.enabled = !cameraOff
        await peerSession.replaceVideoTrack(track)
        facing.current = next
        haptic('tap')
      })
      .catch(() => notify(translate('callFlipFailed')))
  }, [cameraOff, notify, translate])

  return {
    enabled,
    phase: view.phase,
    peer: view.peer,
    outgoing: view.outgoing,
    endReason: view.endReason,
    failure: view.failure,
    interrupted: view.interrupted,
    connectedAt: view.connectedAt,
    localStream,
    remoteStream,
    muted,
    cameraOff,
    canFlipCamera,
    busy,
    start,
    accept,
    decline,
    hangUp,
    dismiss,
    toggleMicrophone,
    toggleCamera,
    flipCamera,
  }
}

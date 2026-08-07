import type { ICEServer } from './types'

/**
 * One RTCPeerConnection and everything attached to it.
 *
 * The class exists so a call has exactly one owner for the connection, the local stream and the
 * listeners: React effects can run twice, a socket can redeliver, and a user can tap End twice —
 * none of which may leave a second connection or a live camera behind. Every method is a no-op
 * after `close()`.
 */

export interface PeerCallbacks {
  /** A locally gathered candidate, to be trickled to the other side immediately. */
  onCandidate: (candidate: RTCIceCandidateInit) => void
  /** Fired once the remote stream has at least one track. */
  onRemoteStream: (stream: MediaStream) => void
  onConnected: () => void
  onFailed: () => void
  /** A recoverable interruption; the browser is still trying to restore the path. */
  onInterrupted: (interrupted: boolean) => void
}

export class PeerSession {
  readonly connection: RTCPeerConnection
  readonly remoteStream = new MediaStream()

  private localStream: MediaStream | null = null
  private videoSender: RTCRtpSender | null = null
  // Candidates can arrive before the description they belong to. Applying one early throws, so
  // they wait here until the remote description is in place.
  private queuedCandidates: RTCIceCandidateInit[] = []
  private hasRemoteDescription = false
  private announcedRemote = false
  private connectedOnce = false
  private closed = false

  constructor(iceServers: ICEServer[], private callbacks: PeerCallbacks) {
    this.connection = new RTCPeerConnection({
      iceServers: iceServers.map((server) => ({
        urls: server.urls,
        username: server.username,
        credential: server.credential,
      })),
      // A small pool lets the first candidates be ready before the offer is even created, which
      // shaves a round trip off the time to first frame.
      iceCandidatePoolSize: 2,
      bundlePolicy: 'max-bundle',
    })

    this.connection.onicecandidate = (event) => {
      if (this.closed || !event.candidate) return
      this.callbacks.onCandidate(event.candidate.toJSON())
    }
    this.connection.ontrack = (event) => {
      if (this.closed) return
      this.remoteStream.addTrack(event.track)
      if (!this.announcedRemote) {
        this.announcedRemote = true
        this.callbacks.onRemoteStream(this.remoteStream)
      }
    }
    this.connection.onconnectionstatechange = () => this.readState()
    this.connection.oniceconnectionstatechange = () => this.readState()
  }

  /** Adds the local camera and microphone to the connection. */
  attachLocal(stream: MediaStream) {
    if (this.closed) return
    this.localStream = stream
    for (const track of stream.getTracks()) {
      const sender = this.connection.addTrack(track, stream)
      if (track.kind === 'video') this.videoSender = sender
    }
  }

  async createOffer(): Promise<string> {
    const offer = await this.connection.createOffer()
    await this.connection.setLocalDescription(offer)
    return this.connection.localDescription?.sdp ?? offer.sdp ?? ''
  }

  /** Applies the caller's offer and produces the answer. */
  async answerOffer(sdp: string): Promise<string> {
    await this.connection.setRemoteDescription({ type: 'offer', sdp })
    await this.drainCandidates()
    const answer = await this.connection.createAnswer()
    await this.connection.setLocalDescription(answer)
    return this.connection.localDescription?.sdp ?? answer.sdp ?? ''
  }

  async applyAnswer(sdp: string) {
    // A duplicate answer would throw an InvalidStateError and abort a working call.
    if (this.closed || this.hasRemoteDescription) return
    await this.connection.setRemoteDescription({ type: 'answer', sdp })
    await this.drainCandidates()
  }

  async addCandidate(candidate: RTCIceCandidateInit) {
    if (this.closed) return
    if (!this.hasRemoteDescription) {
      this.queuedCandidates.push(candidate)
      return
    }
    try {
      await this.connection.addIceCandidate(candidate)
    } catch {
      /* a candidate the browser rejects is not fatal; the remaining pairs still negotiate */
    }
  }

  /** Mutes or unmutes the microphone locally. The track stays open so the call is not renegotiated. */
  setMicrophoneEnabled(enabled: boolean) {
    for (const track of this.localStream?.getAudioTracks() ?? []) track.enabled = enabled
  }

  setCameraEnabled(enabled: boolean) {
    for (const track of this.localStream?.getVideoTracks() ?? []) track.enabled = enabled
  }

  /**
   * Swaps the outgoing camera. `replaceTrack` keeps the same sender and transceiver, so flipping
   * the camera does not renegotiate the session or interrupt the audio.
   */
  async replaceVideoTrack(track: MediaStreamTrack) {
    if (this.closed || !this.videoSender) {
      track.stop()
      return
    }
    const previous = this.localStream?.getVideoTracks()[0]
    await this.videoSender.replaceTrack(track)
    if (this.localStream) {
      if (previous) this.localStream.removeTrack(previous)
      this.localStream.addTrack(track)
    }
    previous?.stop()
  }

  get local(): MediaStream | null {
    return this.localStream
  }

  /**
   * Tears everything down: senders, listeners, the connection and every capture device. Safe to
   * call more than once, which matters because both the End button and the unmount path call it.
   */
  close() {
    if (this.closed) return
    this.closed = true
    this.connection.onicecandidate = null
    this.connection.ontrack = null
    this.connection.onconnectionstatechange = null
    this.connection.oniceconnectionstatechange = null
    for (const sender of this.connection.getSenders()) {
      try {
        sender.track?.stop()
      } catch {
        /* already ended */
      }
    }
    for (const track of this.localStream?.getTracks() ?? []) {
      try {
        track.stop()
      } catch {
        /* already ended */
      }
    }
    for (const track of this.remoteStream.getTracks()) this.remoteStream.removeTrack(track)
    this.localStream = null
    this.videoSender = null
    this.queuedCandidates = []
    try {
      this.connection.close()
    } catch {
      /* closing an already-closed connection throws on some WebViews */
    }
  }

  private async drainCandidates() {
    this.hasRemoteDescription = true
    const pending = this.queuedCandidates
    this.queuedCandidates = []
    for (const candidate of pending) {
      try {
        await this.connection.addIceCandidate(candidate)
      } catch {
        /* see addCandidate */
      }
    }
  }

  /**
   * Collapses the two state machines the browser exposes into the three outcomes the UI cares
   * about. `disconnected` is deliberately not fatal: it is often a few seconds of a changing
   * network that ICE recovers from on its own.
   */
  private readState() {
    if (this.closed) return
    const state = this.connection.connectionState
    const ice = this.connection.iceConnectionState
    if (state === 'connected' || ice === 'connected' || ice === 'completed') {
      this.connectedOnce = true
      this.callbacks.onInterrupted(false)
      this.callbacks.onConnected()
      return
    }
    if (state === 'failed' || ice === 'failed' || state === 'closed') {
      this.callbacks.onFailed()
      return
    }
    if (this.connectedOnce && (state === 'disconnected' || ice === 'disconnected')) {
      this.callbacks.onInterrupted(true)
    }
  }
}

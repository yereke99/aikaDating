/** Wire types for one-to-one video calls. They mirror internal/calls exactly. */

export type CallStatus = 'ringing' | 'receiver_opened' | 'accepted' | 'connected' | 'ended'

export type CallEndReason = 'hangup' | 'rejected' | 'cancelled' | 'timeout' | 'failed' | 'peer_disconnected'

export interface CallPeer {
  id: string
  display_name: string
  photo_url?: string
}

export interface CallRecord {
  id: string
  status: CallStatus
  reason?: CallEndReason
  caller: CallPeer
  callee: CallPeer
  created_at: string
  accepted_at?: string
  ended_at?: string
}

export type CallEventType =
  | 'incoming_call'
  | 'receiver_opened'
  | 'call_accepted'
  | 'call_rejected'
  | 'call_cancelled'
  | 'call_ended'
  | 'call_connected'
  | 'webrtc_offer'
  | 'webrtc_answer'
  | 'ice_candidate'

export interface CallEvent {
  seq: number
  type: CallEventType
  call_id: string
  at: string
  peer?: CallPeer
  reason?: CallEndReason
  sdp?: string
  candidate?: RTCIceCandidateInit
}

export interface ICEServer {
  urls: string[]
  username?: string
  credential?: string
}

export interface CallConfig {
  enabled: boolean
  ice_servers: ICEServer[]
  invite_timeout_seconds: number
  event_wait_seconds: number
  current?: CallRecord
  server_time: string
}

export interface CallResponse {
  call: CallRecord
  ice_servers?: ICEServer[]
  server_time: string
}

export interface CallEventsResponse {
  events: CallEvent[]
  cursor: number
  reset?: boolean
  server_time: string
}

/** What the caller sends on `/signal`; the type strings are the same ones events carry back. */
export type SignalPayload =
  | { type: 'webrtc_offer'; sdp: string }
  | { type: 'webrtc_answer'; sdp: string }
  | { type: 'ice_candidate'; candidate: RTCIceCandidateInit }

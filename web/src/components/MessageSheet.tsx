import { FormEvent, useRef, useState } from 'react'
import { APIError, api } from '../api'
import { useCountdown } from '../hooks'
import { translator } from '../i18n'
import { deadlineFromError, formatRemaining } from '../lib/cooldown'
import { haptic } from '../telegram'
import type { ActionResult, Language, PublicProfile } from '../types'
import { Avatar } from './Avatar'
import { Sheet, SheetBody, SheetFoot, SheetHead, useKeyboardOpen } from './Sheet'

const MESSAGE_LIMIT = 300

/**
 * Personal message composer.
 *
 * The sheet is a three-row grid inside a container bound to the live viewport height, so the footer
 * that holds Send is pinned to the bottom of whatever space the keyboard leaves. Nothing about the
 * keyboard is measured here — the shell already publishes a height that excludes it.
 */
export function MessageSheet({
  profile,
  language,
  cooldownUntil,
  onClose,
  onSent,
  onCooldown,
}: {
  profile: PublicProfile
  language: Language
  cooldownUntil?: string
  onClose: () => void
  onSent: (result: ActionResult) => void
  onCooldown: (nextAllowedAt: string) => void
}) {
  const t = translator(language)
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const keyboardOpen = useKeyboardOpen()
  const remaining = useCountdown(cooldownUntil)
  // A second submit — a double tap, an Enter while the button is still busy — must never reach the
  // network. React state alone is too slow for that; a ref flips synchronously.
  const inFlight = useRef(false)

  const trimmed = message.trim()
  const blocked = remaining > 0
  const canSend = trimmed.length > 0 && !sending && !blocked

  async function send(event: FormEvent) {
    event.preventDefault()
    if (!canSend || inFlight.current) return
    inFlight.current = true
    setSending(true)
    setError('')
    try {
      const result = await api.message(profile.id, trimmed)
      haptic('success')
      onSent(result)
    } catch (caught) {
      haptic('error')
      const cooldown = caught instanceof APIError ? deadlineFromError(caught.payload) : null
      if (cooldown) {
        // The text stays in the box: the message was refused, not sent, and retyping it would be
        // the app's fault rather than the user's.
        onCooldown(cooldown.nextAllowedAt)
        setError('')
      } else {
        setError(caught instanceof Error ? caught.message : t('serverError'))
      }
      setSending(false)
    } finally {
      inFlight.current = false
    }
  }

  return (
    <Sheet onClose={onClose} onSubmit={send} className="message-sheet" labelledBy="message-sheet-title">
      <SheetHead>
        <div className="sheet-person">
          <Avatar src={profile.photos?.[0]?.thumb_url || profile.photo_url} name={profile.display_name} size="small" />
          <div>
            <strong id="message-sheet-title">{profile.display_name}</strong>
            {profile.username && <span className="username">@{profile.username}</span>}
          </div>
        </div>
      </SheetHead>

      <SheetBody>
        <textarea
          autoFocus
          required
          maxLength={MESSAGE_LIMIT}
          className={`message-input${keyboardOpen ? ' compact' : ''}`}
          placeholder={t('messagePlaceholder')}
          aria-label={t('message')}
          value={message}
          disabled={blocked}
          onChange={(event) => setMessage(event.target.value)}
        />
        <div className="composer-meta">
          {error ? (
            <span className="inline-error" role="alert">
              {error}
            </span>
          ) : (
            <span />
          )}
          <span className="counter">
            {Array.from(message).length}/{MESSAGE_LIMIT}
          </span>
        </div>
        {blocked && (
          <p className="cooldown-note" role="status">
            {t('messageCooldown')} {t('tryAgainIn')} <b>{formatRemaining(remaining)}</b>
          </p>
        )}
      </SheetBody>

      <SheetFoot>
        <div className="sheet-actions">
          <button type="button" className="secondary-button" onClick={onClose}>
            {t('cancel')}
          </button>
          <button type="submit" className="primary-button" disabled={!canSend}>
            {blocked ? formatRemaining(remaining) : sending ? t('sending') : t('send')}
          </button>
        </div>
      </SheetFoot>
    </Sheet>
  )
}

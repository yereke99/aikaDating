import { FormEvent, memo, useCallback, useEffect, useRef, useState } from 'react'
import { APIError, api, setAuthorization } from './api'
import { Avatar } from './components/Avatar'
import { DateField } from './components/DateField'
import { MessageSheet } from './components/MessageSheet'
import { PhotoCarousel, PhotoViewer } from './components/PhotoCarousel'
import { PhotoManager } from './components/PhotoManager'
import { Sheet, SheetBody, SheetFoot, SheetHead } from './components/Sheet'
import { useBackButton, useCountdown } from './hooks'
import { MessageKey, translator } from './i18n'
import { Cooldowns, deadlineFromError, formatRemaining } from './lib/cooldown'
import { ageFromBirthDate } from './lib/date'
import { useNearbyFeed } from './nearby'
import { haptic, initializeTelegram, LocationFailure, openLocationSettings, requestLocation, startParameter } from './telegram'
import type { AdminStats, AdminUser, Gallery, Gender, Language, Me, ProfileInput, PublicProfile } from './types'

const telegramApp = initializeTelegram()

const RADII = [5, 10, 20, 500]
const GENDER_FILTERS: [value: string, label: MessageKey][] = [
  ['', 'all'],
  ['male', 'male'],
  ['female', 'female'],
  ['other', 'other'],
]
const LANGUAGES: [Language, string][] = [
  ['ru', 'Русский'],
  ['kk', 'Қазақша'],
  ['en', 'English'],
]
const MIN_AGE = 18
const MAX_AGE = 100
const WIZARD_STEPS = 3

function displayName(me: Me) {
  return me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' ') || me.username || 'AikaBot'
}

function characters(value: string) {
  return Array.from(value.trim()).length
}

function validateStep(step: number, form: ProfileInput, photo?: string): MessageKey | '' {
  if (step === 0) return photo ? '' : 'photoRequired'
  if (step === 1) {
    const name = characters(form.display_name)
    if (name < 2 || name > 80) return 'invalidName'
    if (!form.gender) return 'genderRequired'
    const age = ageFromBirthDate(form.birth_date)
    if (age === null) return 'invalidBirthDate'
    if (age < MIN_AGE || age > MAX_AGE) return 'ageRestriction'
    return ''
  }
  const purpose = characters(form.purpose)
  if (purpose < 2 || purpose > 120) return 'invalidPurpose'
  if (characters(form.bio) > 500) return 'bioTooLong'
  return ''
}

function ScreenHeading({ eyebrow, title, body }: { eyebrow?: boolean; title: string; body?: string }) {
  return (
    <section className="screen-heading">
      {eyebrow && <p className="eyebrow">AikaBot</p>}
      <h1>{title}</h1>
      {body && <p>{body}</p>}
    </section>
  )
}

function FullPageState({
  icon,
  title,
  body,
  action,
  actionLabel,
  loading,
}: {
  icon?: string
  title: string
  body?: string
  action?: () => void
  actionLabel?: string
  loading?: boolean
}) {
  return (
    <main className="full-state">
      {loading ? <div className="spinner" role="status" aria-label={title} /> : <div className="state-icon">{icon}</div>}
      <h1>{title}</h1>
      {body && <p>{body}</p>}
      {action && (
        <button className="primary-button" onClick={action}>
          {actionLabel}
        </button>
      )}
    </main>
  )
}

type ProfileFormState = ReturnType<typeof useProfileForm>

function useProfileForm(me: Me, onSaved: (me: Me) => void) {
  const t = translator(me.app_language)
  const [form, setForm] = useState<ProfileInput>({
    display_name: me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' '),
    gender: me.gender || '',
    birth_date: me.birth_date || '',
    purpose: me.purpose || '',
    bio: me.bio || '',
    custom_photo_url: me.custom_photo_url || '',
    app_language: me.app_language,
    is_active: me.is_active,
  })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  // The gallery is authoritative for the avatar; a Telegram photo still counts as one for accounts
  // that never uploaded anything.
  const photo = me.photos?.[0]?.url || form.custom_photo_url || me.telegram_photo_url

  function update(patch: Partial<ProfileInput>) {
    setError('')
    setForm((current) => ({ ...current, ...patch }))
  }

  async function save(): Promise<boolean> {
    setSaving(true)
    setError('')
    try {
      const updated = await api.updateProfile(form)
      haptic('success')
      onSaved(updated)
      return true
    } catch (caught) {
      haptic('error')
      setError(caught instanceof Error ? caught.message : t('serverError'))
      return false
    } finally {
      setSaving(false)
    }
  }

  return { t, form, update, photo, saving, error, setError, save, language: me.app_language }
}

function BasicsFields({ state, invalid }: { state: ProfileFormState; invalid: MessageKey | '' }) {
  const { t, form, update, language } = state
  return (
    <>
      <label className="field">
        <span className="field-label">{t('displayName')}</span>
        <input
          className="field-control"
          required
          minLength={2}
          maxLength={80}
          autoComplete="name"
          placeholder={t('namePlaceholder')}
          aria-invalid={invalid === 'invalidName' || undefined}
          value={form.display_name}
          onChange={(event) => update({ display_name: event.target.value })}
        />
      </label>
      {/* Two columns only where they genuinely fit; each track is min-width:0 in the stylesheet so
          neither control can push the card wider than the screen. */}
      <div className="field-grid">
        <label className="field">
          <span className="field-label">{t('gender')}</span>
          <select
            className="field-control"
            required
            aria-invalid={invalid === 'genderRequired' || undefined}
            value={form.gender}
            onChange={(event) => update({ gender: event.target.value as Gender })}
          >
            <option value="" disabled>
              —
            </option>
            <option value="male">{t('male')}</option>
            <option value="female">{t('female')}</option>
            <option value="other">{t('other')}</option>
          </select>
        </label>
        <DateField
          label={t('birthDate')}
          value={form.birth_date}
          language={language}
          minAge={MIN_AGE}
          maxAge={MAX_AGE}
          invalid={invalid === 'invalidBirthDate' || invalid === 'ageRestriction'}
          onChange={(birth_date) => update({ birth_date })}
        />
      </div>
    </>
  )
}

function GoalFields({ state, withLanguage, invalid }: { state: ProfileFormState; withLanguage?: boolean; invalid: MessageKey | '' }) {
  const { t, form, update } = state
  return (
    <>
      <label className="field">
        <span className="field-label">{t('purpose')}</span>
        <input
          className="field-control"
          required
          minLength={2}
          maxLength={120}
          placeholder={t('purposePlaceholder')}
          aria-invalid={invalid === 'invalidPurpose' || undefined}
          value={form.purpose}
          onChange={(event) => update({ purpose: event.target.value })}
        />
      </label>
      <label className="field">
        <span className="field-label">{t('bio')}</span>
        <textarea
          className="field-control"
          maxLength={500}
          rows={4}
          placeholder={t('bioPlaceholder')}
          value={form.bio}
          onChange={(event) => update({ bio: event.target.value })}
        />
      </label>
      {withLanguage && (
        <label className="field">
          <span className="field-label">{t('language')}</span>
          <select className="field-control" value={form.app_language} onChange={(event) => update({ app_language: event.target.value as Language })}>
            {LANGUAGES.map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </label>
      )}
    </>
  )
}

function ProfileScreen({
  me,
  onSaved,
  onUpdated,
  notify,
}: {
  me: Me
  /** Called after an explicit Save, which is the only change worth announcing. */
  onSaved: (me: Me) => void
  /** Silent state sync for gallery edits, which report themselves. */
  onUpdated: (me: Me) => void
  notify: (text: string) => void
}) {
  const state = useProfileForm(me, onSaved)
  const { t, form, photo, saving, error, setError } = state
  const [invalid, setInvalid] = useState<MessageKey | ''>('')

  async function submit(event: FormEvent) {
    event.preventDefault()
    for (let step = 0; step < WIZARD_STEPS; step += 1) {
      const problem = validateStep(step, form, photo)
      if (problem) {
        setInvalid(problem)
        setError('')
        haptic('error')
        return
      }
    }
    setInvalid('')
    await state.save()
  }

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('editProfile')} />
      <form className="profile-form" onSubmit={submit} noValidate>
        <div className="form-card">
          <PhotoManager me={me} language={me.app_language} notify={notify} onGallery={(gallery) => applyGallery(gallery, onUpdated, me)} />
        </div>
        <div className="form-card">
          <BasicsFields state={state} invalid={invalid} />
        </div>
        <div className="form-card">
          <GoalFields state={state} withLanguage invalid={invalid} />
        </div>
        {(invalid || error) && (
          <div className="inline-error" role="alert">
            {invalid ? t(invalid) : error}
          </div>
        )}
        <button disabled={saving} className="primary-button block-button" type="submit">
          {saving ? t('saving') : t('save')}
        </button>
      </form>
    </main>
  )
}

/** A gallery change also changes the avatar, so the whole profile is refreshed from the response. */
function applyGallery(gallery: Gallery, onSaved: (me: Me) => void, current: Me) {
  onSaved(gallery.me ? { ...gallery.me, photos: gallery.photos, max_photos: gallery.max_photos } : { ...current, photos: gallery.photos, max_photos: gallery.max_photos })
}

function OnboardingScreen({ me, onSaved, notify }: { me: Me; onSaved: (me: Me) => void; notify: (text: string) => void }) {
  const state = useProfileForm(me, onSaved)
  const { t, form, photo, saving, error, setError } = state
  const [step, setStep] = useState(0)
  const [invalid, setInvalid] = useState<MessageKey | ''>('')
  const scroller = useRef<HTMLDivElement>(null)

  const titles: MessageKey[] = ['stepPhotoTitle', 'stepBasicsTitle', 'stepGoalTitle']
  const bodies: MessageKey[] = ['stepPhotoBody', 'stepBasicsBody', 'stepGoalBody']

  function goTo(next: number) {
    setInvalid('')
    setStep(next)
    scroller.current?.scrollTo({ top: 0 })
  }

  useBackButton(step > 0, () => goTo(step - 1))

  async function advance() {
    const problem = validateStep(step, form, photo)
    if (problem) {
      setInvalid(problem)
      setError('')
      haptic('error')
      return
    }
    if (step < WIZARD_STEPS - 1) {
      haptic('select')
      goTo(step + 1)
      return
    }
    await state.save()
  }

  return (
    <div className="app-shell">
      <header className="wizard-head">
        <div className="bar-inner">
          <div className="wizard-meta">
            <p className="eyebrow">AikaBot</p>
            <span>
              {t('step')} {step + 1} / {WIZARD_STEPS}
            </span>
          </div>
          <div className="wizard-progress" role="progressbar" aria-valuemin={1} aria-valuemax={WIZARD_STEPS} aria-valuenow={step + 1}>
            {Array.from({ length: WIZARD_STEPS }, (_, index) => (
              <i key={index} className={index <= step ? 'done' : ''} />
            ))}
          </div>
        </div>
      </header>
      <div className="app-main">
        <div className="screen-scroll" ref={scroller}>
          <main className="screen">
            <ScreenHeading title={t(titles[step])} body={t(bodies[step])} />
            <div className="form-card">
              {step === 0 && <PhotoManager me={me} language={me.app_language} notify={notify} onGallery={(gallery) => applyGallery(gallery, onSaved, me)} />}
              {step === 1 && <BasicsFields state={state} invalid={invalid} />}
              {step === 2 && <GoalFields state={state} withLanguage invalid={invalid} />}
            </div>
            {(invalid || error) && (
              <div className="inline-error" role="alert">
                {invalid ? t(invalid) : error}
              </div>
            )}
          </main>
        </div>
      </div>
      <footer className="wizard-foot">
        <div className="bar-inner">
          {step > 0 && (
            <button type="button" className="secondary-button" onClick={() => goTo(step - 1)}>
              {t('back')}
            </button>
          )}
          <button type="button" className="primary-button" disabled={saving} onClick={advance}>
            {saving ? t('saving') : step === WIZARD_STEPS - 1 ? t('finish') : t('next')}
          </button>
        </div>
      </footer>
    </div>
  )
}

function LocationPrompt({
  language,
  error,
  loading,
  onRequest,
}: {
  language: Language
  error: MessageKey | ''
  loading: boolean
  onRequest: () => void
}) {
  const t = translator(language)
  return (
    <section className="location-card">
      <div className="location-visual">⌖</div>
      <h2>{t('locationTitle')}</h2>
      <p>{t('locationBody')}</p>
      {error && (
        <div className="inline-error" role="alert">
          {t(error)}
        </div>
      )}
      <button disabled={loading} className="primary-button" onClick={onRequest}>
        {loading ? '…' : error ? t('retry') : t('allowLocation')}
      </button>
      {error === 'location_denied' && telegramApp?.LocationManager && (
        <button className="text-button" onClick={() => openLocationSettings(telegramApp)}>
          {t('openSettings')}
        </button>
      )}
    </section>
  )
}

/**
 * One person in the list. Memoised on the profile object, which the merge keeps referentially stable
 * for anyone whose data did not change — so a two-second refresh rerenders only the cards that
 * actually moved.
 */
const ProfileCard = memo(function ProfileCard({
  profile,
  language,
  cooldowns,
  onLike,
  onMessage,
  onOpen,
}: {
  profile: PublicProfile
  language: Language
  cooldowns?: Cooldowns
  onLike: () => void
  onMessage: () => void
  onOpen: () => void
}) {
  const t = translator(language)
  const likeLeft = useCountdown(cooldowns?.like)
  const messageLeft = useCountdown(cooldowns?.message)
  const meta = [profile.age, profile.gender ? t(profile.gender) : ''].filter(Boolean).join(' · ')
  const photos = profile.photos ?? []

  return (
    <article className="person-card">
      <button className="person-main" onClick={onOpen} aria-label={profile.display_name}>
        <Avatar src={photos[0]?.thumb_url || profile.photo_url} name={profile.display_name} />
        <div className="person-copy">
          <div className="person-title-row">
            <h3>{profile.display_name}</h3>
            {profile.distance_km !== undefined && (
              <span className="distance">
                {profile.distance_km} {t('km')}
              </span>
            )}
          </div>
          {profile.username && <p className="username">@{profile.username}</p>}
          {meta && <p className="person-meta">{meta}</p>}
          {profile.purpose && <p className="purpose">{profile.purpose}</p>}
        </div>
        {photos.length > 1 && (
          <span className="photo-count" aria-label={`${photos.length} ${t('photoCount')}`}>
            ⧉ {photos.length}
          </span>
        )}
      </button>
      <div className="card-actions">
        <button className="like-button" disabled={likeLeft > 0} onClick={onLike} aria-label={t('like')}>
          <em>♥</em>
          <span>{likeLeft > 0 ? formatRemaining(likeLeft) : t('like')}</span>
        </button>
        <button className="message-button" onClick={onMessage} aria-label={t('message')}>
          <span aria-hidden="true">✦</span>
          <span>{messageLeft > 0 ? formatRemaining(messageLeft) : t('message')}</span>
        </button>
      </div>
    </article>
  )
})

function PublicProfileSheet({
  profile,
  language,
  cooldowns,
  onClose,
  onMessage,
  onLike,
}: {
  profile: PublicProfile
  language: Language
  cooldowns?: Cooldowns
  onClose: () => void
  onMessage: () => void
  onLike: () => void
}) {
  const t = translator(language)
  const [viewer, setViewer] = useState(false)
  const likeLeft = useCountdown(cooldowns?.like)
  const messageLeft = useCountdown(cooldowns?.message)
  const photos = profile.photos ?? []

  return (
    <>
      <Sheet onClose={onClose} className="public-sheet" labelledBy="public-profile-title">
        <SheetHead>
          <span className="visually-hidden" id="public-profile-title">
            {profile.display_name}
          </span>
        </SheetHead>
        <SheetBody>
          <PhotoCarousel
            photos={photos}
            language={language}
            fallback={profile.photo_url}
            alt={profile.display_name}
            onOpen={() => photos.length > 0 && setViewer(true)}
          />
          <div className="public-identity">
            <h2>{profile.display_name}</h2>
            {profile.username && <p className="username">@{profile.username}</p>}
            <p className="person-meta">{[profile.age, profile.gender && t(profile.gender)].filter(Boolean).join(' · ')}</p>
            {profile.distance_km !== undefined && (
              <p className="distance-line">
                ⌖ {t('distance')} {profile.distance_km} {t('km')}
              </p>
            )}
          </div>
          {profile.purpose && (
            <div className="detail-block">
              <span>{t('purpose')}</span>
              <p>{profile.purpose}</p>
            </div>
          )}
          {profile.bio && (
            <div className="detail-block">
              <span>{t('bio')}</span>
              <p>{profile.bio}</p>
            </div>
          )}
          {likeLeft > 0 && (
            <p className="cooldown-note" role="status">
              {t('likeCooldown')} {t('tryAgainIn')} <b>{formatRemaining(likeLeft)}</b>
            </p>
          )}
        </SheetBody>
        <SheetFoot>
          <div className="sheet-actions">
            <button className="secondary-button" disabled={likeLeft > 0} onClick={onLike}>
              ♥ {likeLeft > 0 ? formatRemaining(likeLeft) : t('like')}
            </button>
            <button className="primary-button" onClick={onMessage}>
              {messageLeft > 0 ? formatRemaining(messageLeft) : t('message')}
            </button>
          </div>
        </SheetFoot>
      </Sheet>
      {viewer && <PhotoViewer photos={photos} language={language} alt={profile.display_name} onClose={() => setViewer(false)} />}
    </>
  )
}

function Nearby({
  me,
  onLocation,
  notify,
  initialProfile,
  clearInitialProfile,
}: {
  me: Me
  onLocation: () => void
  notify: (text: string) => void
  initialProfile: PublicProfile | null
  clearInitialProfile: () => void
}) {
  const t = translator(me.app_language)
  const [radius, setRadius] = useState(20)
  const [gender, setGender] = useState('')
  const [locationError, setLocationError] = useState<MessageKey | ''>('')
  const [locating, setLocating] = useState(false)
  const [messageTarget, setMessageTarget] = useState<PublicProfile | null>(null)
  const [openProfile, setOpenProfile] = useState<PublicProfile | null>(initialProfile)
  // Guards a card whose like request is already in flight from a second tap.
  const likesInFlight = useRef(new Set<string>())

  const feed = useNearbyFeed({ enabled: me.location_available, radius, gender })
  const { profiles, cooldowns, noteCooldown } = feed

  useEffect(() => {
    if (initialProfile) setOpenProfile(initialProfile)
  }, [initialProfile])

  // Keep an open sheet in step with the polled data, without reopening or resetting it.
  useEffect(() => {
    setOpenProfile((current) => {
      if (!current) return current
      const fresh = profiles.find((profile) => profile.id === current.id)
      return fresh && fresh !== current ? fresh : current
    })
  }, [profiles])

  async function locate() {
    setLocating(true)
    setLocationError('')
    try {
      const location = await requestLocation(telegramApp)
      await api.updateLocation(location.latitude, location.longitude)
      onLocation()
    } catch (caught) {
      if (caught instanceof LocationFailure) setLocationError(caught.code)
      else setLocationError('location_unavailable')
    } finally {
      setLocating(false)
    }
  }

  const sendLike = useCallback(
    async (profile: PublicProfile) => {
      if (likesInFlight.current.has(profile.id)) return
      likesInFlight.current.add(profile.id)
      try {
        const result = await api.like(profile.id)
        noteCooldown(profile.id, 'like', result.next_allowed_at)
        notify(result.message || t('likeSent'))
        haptic('success')
      } catch (caught) {
        haptic('error')
        const cooldown = caught instanceof APIError ? deadlineFromError(caught.payload) : null
        if (cooldown) {
          noteCooldown(profile.id, cooldown.action, cooldown.nextAllowedAt)
          notify(t('likeCooldown'))
        } else {
          notify(caught instanceof Error ? caught.message : t('serverError'))
        }
      } finally {
        likesInFlight.current.delete(profile.id)
      }
    },
    [noteCooldown, notify, t],
  )

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('nearby')} />
      {!me.location_available ? (
        <LocationPrompt language={me.app_language} error={locationError} loading={locating} onRequest={locate} />
      ) : (
        <>
          <div className="filters">
            <div className="filter-group">
              <span>{t('filterRadius')}</span>
              <div className="chip-row" role="group" aria-label={t('filterRadius')}>
                {RADII.map((value) => (
                  <button
                    key={value}
                    type="button"
                    className="chip"
                    aria-pressed={radius === value}
                    onClick={() => {
                      setRadius(value)
                      haptic('select')
                    }}
                  >
                    {value} {t('km')}
                  </button>
                ))}
              </div>
            </div>
            <div className="filter-group">
              <span>{t('filterGender')}</span>
              <div className="chip-row" role="group" aria-label={t('filterGender')}>
                {GENDER_FILTERS.map(([value, label]) => (
                  <button
                    key={label}
                    type="button"
                    className="chip"
                    aria-pressed={gender === value}
                    onClick={() => {
                      setGender(value)
                      haptic('select')
                    }}
                  >
                    {t(label)}
                  </button>
                ))}
              </div>
            </div>
          </div>

          {/* A dropped connection dims a badge rather than clearing the list or shouting every
              two seconds. */}
          {feed.offline && profiles.length > 0 && (
            <p className="connection-note" role="status">
              {t('reconnecting')}
            </p>
          )}
          {feed.loading && profiles.length === 0 && (
            <div className="skeleton-list">
              {[1, 2, 3].map((index) => (
                <div className="skeleton-card" key={index}>
                  <i />
                  <div>
                    <b />
                    <b />
                    <b />
                  </div>
                </div>
              ))}
            </div>
          )}
          {feed.error && profiles.length === 0 && (
            <div className="content-state">
              <span>!</span>
              <p>{feed.error}</p>
              <button className="secondary-button" onClick={feed.reload}>
                {t('retry')}
              </button>
            </div>
          )}
          {!feed.loading && !feed.error && profiles.length === 0 && (
            <div className="content-state">
              <span>⌖</span>
              <h2>{t('noPeople')}</h2>
              <p>{t('noPeopleBody')}</p>
            </div>
          )}
          {profiles.length > 0 && (
            <div className="people-list">
              {profiles.map((profile) => (
                <ProfileCard
                  key={profile.id}
                  profile={profile}
                  language={me.app_language}
                  cooldowns={cooldowns[profile.id]}
                  onLike={() => void sendLike(profile)}
                  onMessage={() => setMessageTarget(profile)}
                  onOpen={() => setOpenProfile(profile)}
                />
              ))}
            </div>
          )}
          {feed.hasMore && (
            <button disabled={feed.loading} className="secondary-button block-button" onClick={() => void feed.loadMore()}>
              {t('loadMore')}
            </button>
          )}
        </>
      )}

      {openProfile && (
        <PublicProfileSheet
          profile={openProfile}
          language={me.app_language}
          cooldowns={cooldowns[openProfile.id]}
          onClose={() => {
            setOpenProfile(null)
            clearInitialProfile()
          }}
          onLike={() => void sendLike(openProfile)}
          onMessage={() => {
            setMessageTarget(openProfile)
            setOpenProfile(null)
          }}
        />
      )}
      {messageTarget && (
        <MessageSheet
          profile={messageTarget}
          language={me.app_language}
          cooldownUntil={cooldowns[messageTarget.id]?.message}
          onClose={() => setMessageTarget(null)}
          onSent={(result) => {
            noteCooldown(messageTarget.id, 'message', result.next_allowed_at)
            setMessageTarget(null)
            notify(result.message || t('messageSent'))
          }}
          onCooldown={(nextAllowedAt) => noteCooldown(messageTarget.id, 'message', nextAllowedAt)}
        />
      )}
    </main>
  )
}

function Settings({ me, onSaved, notify }: { me: Me; onSaved: (me: Me) => void; notify: (text: string) => void }) {
  const t = translator(me.app_language)
  const [language, setLanguage] = useState(me.app_language)
  const [active, setActive] = useState(me.is_active)
  const [saving, setSaving] = useState(false)

  async function save() {
    setSaving(true)
    try {
      const updated = await api.updateProfile({
        display_name: me.display_name || displayName(me),
        gender: me.gender || '',
        birth_date: me.birth_date || '',
        purpose: me.purpose || '',
        bio: me.bio || '',
        custom_photo_url: me.custom_photo_url || '',
        app_language: language,
        is_active: active,
      })
      onSaved(updated)
      notify(translator(updated.app_language)('saved'))
      haptic('success')
    } catch (caught) {
      notify(caught instanceof Error ? caught.message : t('serverError'))
      haptic('error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('settings')} />
      <section className="card">
        <label className="field">
          <span className="field-label">{t('language')}</span>
          <select className="field-control" value={language} onChange={(event) => setLanguage(event.target.value as Language)}>
            {LANGUAGES.map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </label>
      </section>
      <section className="card toggle-row">
        <div>
          <strong>{t('activeProfile')}</strong>
          <p>{t('activeHelp')}</p>
        </div>
        <button
          role="switch"
          aria-checked={active}
          aria-label={t('activeProfile')}
          className={`switch ${active ? 'on' : ''}`}
          onClick={() => {
            setActive(!active)
            haptic('tap')
          }}
        >
          <i />
        </button>
      </section>
      <button disabled={saving} className="primary-button block-button" onClick={save}>
        {saving ? t('saving') : t('save')}
      </button>
    </main>
  )
}

function Admin({ me }: { me: Me }) {
  const t = translator(me.app_language)
  const [stats, setStats] = useState<AdminStats | null>(null)
  const [items, setItems] = useState<AdminUser[]>([])
  const [search, setSearch] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    const timer = window.setTimeout(
      () => {
        Promise.all([api.adminStats(), api.adminUsers(search)])
          .then(([nextStats, result]) => {
            setStats(nextStats)
            setItems(result.users)
            setError('')
          })
          .catch((caught) => setError(caught instanceof Error ? caught.message : t('serverError')))
      },
      search ? 250 : 0,
    )
    return () => window.clearTimeout(timer)
  }, [search, me.app_language])

  const cards: [MessageKey, number][] = stats
    ? [
        ['total', stats.total],
        ['completed', stats.completed],
        ['incomplete', stats.incomplete],
        ['active', stats.active],
        ['blocked', stats.blocked],
      ]
    : []

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('adminTitle')} />
      {stats && (
        <div className="stats-grid">
          {cards.map(([label, value]) => (
            <div key={label}>
              <strong>{value}</strong>
              <span>{t(label)}</span>
            </div>
          ))}
        </div>
      )}
      <input className="field-control" type="search" aria-label={t('search')} placeholder={t('search')} value={search} onChange={(event) => setSearch(event.target.value)} />
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <div className="admin-list">
        {items.map((item) => (
          <article key={item.telegram_user_id} className="admin-user">
            <div>
              <strong>{item.display_name}</strong>
              {item.username && <span className="username">@{item.username}</span>}
              <code>{item.telegram_user_id}</code>
            </div>
            <p>{[item.age, item.gender, item.purpose].filter(Boolean).join(' · ')}</p>
            <dl>
              <div>
                <dt>{t('registered')}</dt>
                <dd>{new Date(item.registered_at).toLocaleDateString(me.app_language)}</dd>
              </div>
              <div>
                <dt>{t('lastSeen')}</dt>
                <dd>{new Date(item.last_seen_at).toLocaleString(me.app_language)}</dd>
              </div>
              <div>
                <dt>{t('hasLocation')}</dt>
                <dd>{item.location_available ? t('yes') : t('no')}</dd>
              </div>
            </dl>
          </article>
        ))}
      </div>
    </main>
  )
}

type Tab = 'nearby' | 'profile' | 'settings' | 'admin'

/**
 * Keeps a focused field visible when the on-screen keyboard covers the lower half. Sheets are left
 * alone: they size themselves to the live viewport, and scrolling them here would fight that.
 */
function useKeyboardAwareFocus() {
  useEffect(() => {
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target as HTMLElement | null
      if (!target?.matches?.('input, textarea, select')) return
      if (target.closest('.bottom-sheet')) return
      window.setTimeout(() => target.scrollIntoView({ block: 'center', behavior: 'smooth' }), 280)
    }
    document.addEventListener('focusin', onFocusIn)
    return () => document.removeEventListener('focusin', onFocusIn)
  }, [])
}

export default function App() {
  const initialized = useRef(false)
  const [me, setMe] = useState<Me | null>(null)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState('')
  const [tab, setTab] = useState<Tab>('nearby')
  const [toast, setToast] = useState('')
  const [deepProfile, setDeepProfile] = useState<PublicProfile | null>(null)
  const toastTimer = useRef(0)

  useKeyboardAwareFocus()

  useEffect(() => {
    if (initialized.current) return
    initialized.current = true
    const local = ['localhost', '127.0.0.1'].includes(window.location.hostname)
    if (telegramApp?.initData) setAuthorization(`tma ${telegramApp.initData}`)
    else if (local) setAuthorization('dev')
    else {
      setError(translator('ru')('telegramUnavailable'))
      setStatus('error')
      return
    }
    api
      .authenticate()
      .then(async (user) => {
        setMe(user)
        document.documentElement.lang = user.app_language
        const parameter = startParameter(telegramApp)
        const match = /^profile_([0-9a-f-]{36})$/.exec(parameter)
        if (match) {
          try {
            setDeepProfile(await api.publicProfile(match[1]))
          } catch {
            /* unavailable profiles stay hidden */
          }
        }
        setStatus('ready')
      })
      .catch((caught) => {
        const language = (telegramApp?.initDataUnsafe.user?.language_code?.split('-')[0] as Language) || 'ru'
        const t = translator(['ru', 'kk', 'en'].includes(language) ? language : 'ru')
        if (caught instanceof APIError && caught.code === 'telegram_auth_expired') setError(t('authExpired'))
        else if (caught instanceof TypeError) setError(t('networkError'))
        else setError(t('authFailed'))
        setStatus('error')
      })
  }, [])

  useEffect(() => () => window.clearTimeout(toastTimer.current), [])

  const notify = useCallback((text: string) => {
    setToast(text)
    window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(''), 2600)
  }, [])

  if (status === 'loading') return <FullPageState loading title="AikaBot" />
  if (status === 'error' || !me)
    return <FullPageState icon="!" title="AikaBot" body={error} action={() => window.location.reload()} actionLabel={translator('ru')('retry')} />
  if (!me.is_profile_completed)
    return (
      <OnboardingScreen
        me={me}
        notify={notify}
        onSaved={(user) => {
          setMe(user)
          document.documentElement.lang = user.app_language
        }}
      />
    )

  const t = translator(me.app_language)
  // Short labels in the tab bar, full titles on the screens themselves.
  const tabs: [Tab, string, MessageKey, MessageKey][] = [
    ['nearby', '⌖', 'navNearby', 'nearby'],
    ['profile', '◉', 'navProfile', 'profile'],
    ['settings', '⚙', 'navSettings', 'settings'],
  ]
  if (me.is_admin) tabs.push(['admin', '◆', 'navAdmin', 'admin'])

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="bar-inner">
          <Avatar src={me.photos?.[0]?.thumb_url || me.photo_url} name={displayName(me)} size="small" />
          <div className="app-header__identity">
            <strong>{displayName(me)}</strong>
            {me.username && <span>@{me.username}</span>}
          </div>
          <button className="language-button" aria-label={t('language')} onClick={() => setTab('settings')}>
            {me.app_language.toUpperCase()}
          </button>
        </div>
      </header>
      <div className="app-main">
        <div className="screen-scroll">
          {tab === 'nearby' && (
            <Nearby
              me={me}
              notify={notify}
              initialProfile={deepProfile}
              clearInitialProfile={() => setDeepProfile(null)}
              onLocation={() => setMe({ ...me, location_available: true })}
            />
          )}
          {tab === 'profile' && (
            <ProfileScreen
              me={me}
              notify={notify}
              onUpdated={setMe}
              onSaved={(user) => {
                setMe(user)
                notify(translator(user.app_language)('saved'))
              }}
            />
          )}
          {tab === 'settings' && (
            <Settings
              me={me}
              onSaved={(user) => {
                setMe(user)
                document.documentElement.lang = user.app_language
              }}
              notify={notify}
            />
          )}
          {tab === 'admin' && me.is_admin && <Admin me={me} />}
        </div>
        {toast && (
          <div className="toast" role="status">
            {toast}
          </div>
        )}
      </div>
      <nav className="bottom-nav">
        <div className="bar-inner">
          {tabs.map(([value, icon, label, title]) => (
            <button
              key={value}
              className={tab === value ? 'active' : ''}
              aria-label={t(title)}
              aria-current={tab === value ? 'page' : undefined}
              onClick={() => {
                setTab(value)
                haptic('tap')
              }}
            >
              <i aria-hidden="true">{icon}</i>
              <span>{t(label)}</span>
            </button>
          ))}
        </div>
      </nav>
    </div>
  )
}

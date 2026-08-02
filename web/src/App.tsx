import { FormEvent, ReactNode, useEffect, useRef, useState } from 'react'
import { APIError, api, setAuthorization } from './api'
import { MessageKey, translator } from './i18n'
import { haptic, initializeTelegram, LocationFailure, openLocationSettings, pushBackHandler, requestLocation, startParameter } from './telegram'
import type { AdminStats, AdminUser, Gender, Language, Me, ProfileInput, PublicProfile } from './types'

const telegramApp = initializeTelegram()

const RADII = [5, 10, 20, 500]
const GENDER_FILTERS: [value: string, label: MessageKey][] = [['', 'all'], ['male', 'male'], ['female', 'female'], ['other', 'other']]
const LANGUAGES: [Language, string][] = [['ru', 'Русский'], ['kk', 'Қазақша'], ['en', 'English']]
const MIN_AGE = 18
const MAX_AGE = 100
const WIZARD_STEPS = 3

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join('') || 'A'
}

function displayName(me: Me) {
  return me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' ') || me.username || 'AikaBot'
}

function characters(value: string) {
  return Array.from(value.trim()).length
}

function isoDate(date: Date) {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/** Mirrors the server's age rule so the wizard can block a step instead of failing on submit. */
function ageFromBirthDate(value: string): number | null {
  const parts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value.trim())
  if (!parts) return null
  const birth = new Date(Number(parts[1]), Number(parts[2]) - 1, Number(parts[3]))
  if (Number.isNaN(birth.getTime())) return null
  const now = new Date()
  const beforeBirthday =
    now.getMonth() < birth.getMonth() || (now.getMonth() === birth.getMonth() && now.getDate() < birth.getDate())
  return now.getFullYear() - birth.getFullYear() - (beforeBirthday ? 1 : 0)
}

function birthDateBounds() {
  const now = new Date()
  const oldest = new Date(now.getFullYear() - MAX_AGE, now.getMonth(), now.getDate())
  const youngest = new Date(now.getFullYear() - MIN_AGE, now.getMonth(), now.getDate())
  return { min: isoDate(oldest), max: isoDate(youngest) }
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

async function normalizeProfilePhoto(file: File): Promise<Blob> {
  if (!file.type.startsWith('image/')) throw new Error('invalid_photo')
  const objectURL = URL.createObjectURL(file)
  try {
    const image = new Image()
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve()
      image.onerror = () => reject(new Error('invalid_photo'))
      image.src = objectURL
    })
    const maxSide = 1600
    const scale = Math.min(1, maxSide / Math.max(image.naturalWidth, image.naturalHeight))
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(image.naturalWidth * scale))
    canvas.height = Math.max(1, Math.round(image.naturalHeight * scale))
    const context = canvas.getContext('2d')
    if (!context) throw new Error('invalid_photo')
    context.drawImage(image, 0, 0, canvas.width, canvas.height)
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.86))
    if (!blob) throw new Error('invalid_photo')
    return blob
  } finally {
    URL.revokeObjectURL(objectURL)
  }
}

/** Binds Telegram's native back button while `active`, stacking under any sheet opened later. */
function useBackButton(active: boolean, onBack: () => void) {
  const latest = useRef(onBack)
  latest.current = onBack
  useEffect(() => {
    if (!active) return undefined
    return pushBackHandler(() => latest.current())
  }, [active])
}

function Avatar({ src, name, size = 'normal' }: { src?: string; name: string; size?: 'small' | 'normal' | 'large' }) {
  const [failed, setFailed] = useState(false)
  return (
    <div className={`avatar avatar-${size}`} aria-label={name}>
      {src && !failed ? <img src={src} alt="" loading="lazy" onError={() => setFailed(true)} /> : <span>{initials(name)}</span>}
    </div>
  )
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

function FullPageState({ icon, title, body, action, actionLabel, loading }: { icon?: string; title: string; body?: string; action?: () => void; actionLabel?: string; loading?: boolean }) {
  return (
    <main className="full-state">
      {loading ? <div className="spinner" role="status" aria-label={title} /> : <div className="state-icon">{icon}</div>}
      <h1>{title}</h1>
      {body && <p>{body}</p>}
      {action && <button className="primary-button" onClick={action}>{actionLabel}</button>}
    </main>
  )
}

type ProfileFormState = ReturnType<typeof useProfileForm>

function useProfileForm(me: Me, onSaved: (me: Me) => void) {
  const t = translator(me.app_language)
  const [form, setForm] = useState<ProfileInput>({
    display_name: me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' '),
    gender: me.gender || '', birth_date: me.birth_date || '', purpose: me.purpose || '', bio: me.bio || '',
    custom_photo_url: me.custom_photo_url || '', app_language: me.app_language, is_active: me.is_active,
  })
  const [saving, setSaving] = useState(false)
  const [uploadingPhoto, setUploadingPhoto] = useState(false)
  const [error, setError] = useState('')
  const photo = form.custom_photo_url || me.telegram_photo_url

  function update(patch: Partial<ProfileInput>) {
    setError('')
    setForm((current) => ({ ...current, ...patch }))
  }

  async function uploadPhoto(file?: File) {
    if (!file) return
    setUploadingPhoto(true)
    setError('')
    try {
      const normalized = await normalizeProfilePhoto(file)
      const updated = await api.uploadPhoto(normalized)
      setForm((current) => ({ ...current, custom_photo_url: updated.custom_photo_url || '' }))
      onSaved(updated)
      haptic('success')
    } catch (caught) {
      haptic('error')
      setError(caught instanceof APIError ? caught.message : t('invalidPhoto'))
    } finally {
      setUploadingPhoto(false)
    }
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

  return { t, form, update, photo, saving, uploadingPhoto, error, setError, uploadPhoto, save, telegramPhoto: me.telegram_photo_url, fallbackName: displayName(me) }
}

function PhotoFields({ state }: { state: ProfileFormState }) {
  const { t, form, photo, uploadingPhoto, uploadPhoto, telegramPhoto, fallbackName } = state
  const galleryInput = useRef<HTMLInputElement>(null)
  const cameraInput = useRef<HTMLInputElement>(null)
  const pick = (input: HTMLInputElement) => {
    void uploadPhoto(input.files?.[0]).finally(() => { input.value = '' })
  }
  return (
    <div className="photo-field">
      <Avatar src={photo} name={form.display_name || fallbackName} size="large" />
      <div className="photo-copy">
        {telegramPhoto && !form.custom_photo_url && <span className="field-hint">{t('telegramPhoto')}</span>}
        <div className="photo-actions">
          <button type="button" className="secondary-button" disabled={uploadingPhoto} onClick={() => galleryInput.current?.click()}>{t('choosePhoto')}</button>
          <button type="button" className="secondary-button selfie-button" disabled={uploadingPhoto} onClick={() => cameraInput.current?.click()}>◎ {t('takeSelfie')}</button>
        </div>
        <span className="field-hint">{uploadingPhoto ? t('uploadingPhoto') : t('photoHelp')}</span>
      </div>
      <input ref={galleryInput} className="visually-hidden" type="file" accept="image/jpeg,image/png,image/*" onChange={(event) => pick(event.currentTarget)} />
      <input ref={cameraInput} className="visually-hidden" type="file" accept="image/*" capture="user" onChange={(event) => pick(event.currentTarget)} />
    </div>
  )
}

function BasicsFields({ state }: { state: ProfileFormState }) {
  const { t, form, update } = state
  const bounds = birthDateBounds()
  return (
    <>
      <label className="field">{t('displayName')}
        <input required minLength={2} maxLength={80} autoComplete="name" placeholder={t('namePlaceholder')} value={form.display_name} onChange={(event) => update({ display_name: event.target.value })} />
      </label>
      <div className="field-grid">
        <label className="field">{t('gender')}
          <select required value={form.gender} onChange={(event) => update({ gender: event.target.value as Gender })}>
            <option value="" disabled>—</option>
            <option value="male">{t('male')}</option>
            <option value="female">{t('female')}</option>
            <option value="other">{t('other')}</option>
          </select>
        </label>
        <label className="field">{t('birthDate')}
          <input required type="date" min={bounds.min} max={bounds.max} value={form.birth_date} onChange={(event) => update({ birth_date: event.target.value })} />
        </label>
      </div>
    </>
  )
}

function GoalFields({ state, withLanguage }: { state: ProfileFormState; withLanguage?: boolean }) {
  const { t, form, update } = state
  return (
    <>
      <label className="field">{t('purpose')}
        <input required minLength={2} maxLength={120} placeholder={t('purposePlaceholder')} value={form.purpose} onChange={(event) => update({ purpose: event.target.value })} />
      </label>
      <label className="field">{t('bio')}
        <textarea maxLength={500} rows={4} placeholder={t('bioPlaceholder')} value={form.bio} onChange={(event) => update({ bio: event.target.value })} />
      </label>
      {withLanguage && (
        <label className="field">{t('language')}
          <select value={form.app_language} onChange={(event) => update({ app_language: event.target.value as Language })}>
            {LANGUAGES.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      )}
    </>
  )
}

function ProfileScreen({ me, onSaved }: { me: Me; onSaved: (me: Me) => void }) {
  const state = useProfileForm(me, onSaved)
  const { t, form, photo, saving, uploadingPhoto, error, setError } = state
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
        <div className="form-card"><PhotoFields state={state} /></div>
        <div className="form-card"><BasicsFields state={state} /></div>
        <div className="form-card"><GoalFields state={state} withLanguage /></div>
        {(invalid || error) && <div className="inline-error" role="alert">{invalid ? t(invalid) : error}</div>}
        <button disabled={saving || uploadingPhoto} className="primary-button block-button" type="submit">{saving ? t('saving') : t('save')}</button>
      </form>
    </main>
  )
}

function OnboardingScreen({ me, onSaved }: { me: Me; onSaved: (me: Me) => void }) {
  const state = useProfileForm(me, onSaved)
  const { t, form, photo, saving, uploadingPhoto, error, setError } = state
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
            <span>{t('step')} {step + 1} / {WIZARD_STEPS}</span>
          </div>
          <div className="wizard-progress" role="progressbar" aria-valuemin={1} aria-valuemax={WIZARD_STEPS} aria-valuenow={step + 1}>
            {Array.from({ length: WIZARD_STEPS }, (_, index) => <i key={index} className={index <= step ? 'done' : ''} />)}
          </div>
        </div>
      </header>
      <div className="app-main">
        <div className="screen-scroll" ref={scroller}>
          <main className="screen">
            <ScreenHeading title={t(titles[step])} body={t(bodies[step])} />
            <div className="form-card">
              {step === 0 && <PhotoFields state={state} />}
              {step === 1 && <BasicsFields state={state} />}
              {step === 2 && <GoalFields state={state} withLanguage />}
            </div>
            {(invalid || error) && <div className="inline-error" role="alert">{invalid ? t(invalid) : error}</div>}
          </main>
        </div>
      </div>
      <footer className="wizard-foot">
        <div className="bar-inner">
          {step > 0 && <button type="button" className="secondary-button" onClick={() => goTo(step - 1)}>{t('back')}</button>}
          <button type="button" className="primary-button" disabled={saving || uploadingPhoto} onClick={advance}>
            {saving ? t('saving') : step === WIZARD_STEPS - 1 ? t('finish') : t('next')}
          </button>
        </div>
      </footer>
    </div>
  )
}

function LocationPrompt({ language, error, loading, onRequest }: { language: Language; error: MessageKey | ''; loading: boolean; onRequest: () => void }) {
  const t = translator(language)
  return (
    <section className="location-card">
      <div className="location-visual">⌖</div>
      <h2>{t('locationTitle')}</h2>
      <p>{t('locationBody')}</p>
      {error && <div className="inline-error" role="alert">{t(error)}</div>}
      <button disabled={loading} className="primary-button" onClick={onRequest}>{loading ? '…' : error ? t('retry') : t('allowLocation')}</button>
      {error === 'location_denied' && telegramApp?.LocationManager && <button className="text-button" onClick={() => openLocationSettings(telegramApp)}>{t('openSettings')}</button>}
    </section>
  )
}

function ProfileCard({ profile, language, onLike, onMessage, onOpen }: { profile: PublicProfile; language: Language; onLike: () => void; onMessage: () => void; onOpen: () => void }) {
  const t = translator(language)
  const meta = [profile.age, profile.gender ? t(profile.gender) : ''].filter(Boolean).join(' · ')
  return (
    <article className="person-card">
      <button className="person-main" onClick={onOpen} aria-label={profile.display_name}>
        <Avatar src={profile.photo_url} name={profile.display_name} />
        <div className="person-copy">
          <div className="person-title-row">
            <h3>{profile.display_name}</h3>
            {profile.distance_km !== undefined && <span className="distance">{profile.distance_km} {t('km')}</span>}
          </div>
          {profile.username && <p className="username">@{profile.username}</p>}
          {meta && <p className="person-meta">{meta}</p>}
          {profile.purpose && <p className="purpose">{profile.purpose}</p>}
        </div>
      </button>
      <div className="card-actions">
        <button className="like-button" onClick={onLike}><em>♥</em><span>{t('like')}</span></button>
        <button className="message-button" onClick={onMessage}><span>✦</span><span>{t('message')}</span></button>
      </div>
    </article>
  )
}

function Sheet({ onClose, onSubmit, className, children }: { onClose: () => void; onSubmit?: (event: FormEvent) => void; className?: string; children: ReactNode }) {
  useBackButton(true, onClose)
  const sheetClass = `bottom-sheet${className ? ` ${className}` : ''}`
  return (
    <div className="sheet-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      {onSubmit
        ? <form className={sheetClass} onSubmit={onSubmit}>{children}</form>
        : <section className={sheetClass}>{children}</section>}
    </div>
  )
}

function MessageSheet({ profile, language, onClose, onSent }: { profile: PublicProfile; language: Language; onClose: () => void; onSent: () => void }) {
  const t = translator(language)
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')

  async function send(event: FormEvent) {
    event.preventDefault()
    if (!message.trim()) return
    setSending(true)
    setError('')
    try {
      await api.like(profile.id, message.trim())
      haptic('success')
      onSent()
    } catch (caught) {
      haptic('error')
      setError(caught instanceof Error ? caught.message : t('serverError'))
      setSending(false)
    }
  }

  return (
    <Sheet onClose={onClose} onSubmit={send}>
      <div className="sheet-handle" />
      <div className="sheet-person">
        <Avatar src={profile.photo_url} name={profile.display_name} size="small" />
        <div><strong>{profile.display_name}</strong>{profile.username && <span className="username">@{profile.username}</span>}</div>
      </div>
      <textarea autoFocus required maxLength={300} rows={4} placeholder={t('messagePlaceholder')} value={message} onChange={(event) => setMessage(event.target.value)} />
      <div className="counter">{Array.from(message).length}/300</div>
      {error && <div className="inline-error" role="alert">{error}</div>}
      <div className="sheet-actions">
        <button type="button" className="secondary-button" onClick={onClose}>{t('cancel')}</button>
        <button disabled={sending || !message.trim()} className="primary-button">{sending ? '…' : t('send')}</button>
      </div>
    </Sheet>
  )
}

function PublicProfileSheet({ profile, language, onClose, onMessage }: { profile: PublicProfile; language: Language; onClose: () => void; onMessage: () => void }) {
  const t = translator(language)
  return (
    <Sheet onClose={onClose} className="public-sheet">
      <div className="sheet-handle" />
      <Avatar src={profile.photo_url} name={profile.display_name} size="large" />
      <h2>{profile.display_name}</h2>
      {profile.username && <p className="username">@{profile.username}</p>}
      <p className="person-meta">{[profile.age, profile.gender && t(profile.gender)].filter(Boolean).join(' · ')}</p>
      {profile.distance_km !== undefined && <p className="distance-line">⌖ {t('distance')} {profile.distance_km} {t('km')}</p>}
      {profile.purpose && <div className="detail-block"><span>{t('purpose')}</span><p>{profile.purpose}</p></div>}
      {profile.bio && <div className="detail-block"><span>{t('bio')}</span><p>{profile.bio}</p></div>}
      <div className="sheet-actions">
        <button className="secondary-button" onClick={onClose}>{t('close')}</button>
        <button className="primary-button" onClick={onMessage}>{t('message')}</button>
      </div>
    </Sheet>
  )
}

function Nearby({ me, onLocation, notify, initialProfile, clearInitialProfile }: { me: Me; onLocation: () => void; notify: (text: string) => void; initialProfile: PublicProfile | null; clearInitialProfile: () => void }) {
  const t = translator(me.app_language)
  const [radius, setRadius] = useState(20)
  const [gender, setGender] = useState('')
  const [profiles, setProfiles] = useState<PublicProfile[]>([])
  const [page, setPage] = useState(1)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [locationError, setLocationError] = useState<MessageKey | ''>('')
  const [locating, setLocating] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const [messageTarget, setMessageTarget] = useState<PublicProfile | null>(null)
  const [openProfile, setOpenProfile] = useState<PublicProfile | null>(initialProfile)

  useEffect(() => { if (initialProfile) setOpenProfile(initialProfile) }, [initialProfile])
  useEffect(() => {
    if (!me.location_available) return
    let active = true
    setLoading(true); setLoadError('')
    api.nearby(radius, 1, gender).then((result) => {
      if (!active) return
      setProfiles(result.users); setPage(1); setHasMore(result.has_more)
    }).catch((error) => active && setLoadError(error instanceof Error ? error.message : t('networkError')))
      .finally(() => active && setLoading(false))
    return () => { active = false }
  }, [me.location_available, radius, gender, me.app_language, reloadKey])

  async function locate() {
    setLocating(true); setLocationError('')
    try {
      const location = await requestLocation(telegramApp)
      await api.updateLocation(location.latitude, location.longitude)
      onLocation()
    } catch (caught) {
      if (caught instanceof LocationFailure) setLocationError(caught.code)
      else setLocationError('location_unavailable')
    } finally { setLocating(false) }
  }

  async function loadMore() {
    setLoading(true)
    try {
      const result = await api.nearby(radius, page + 1, gender)
      setProfiles((current) => [...current, ...result.users]); setPage(result.page); setHasMore(result.has_more)
    } catch (caught) { setLoadError(caught instanceof Error ? caught.message : t('networkError')) }
    finally { setLoading(false) }
  }

  async function sendLike(profile: PublicProfile) {
    try { const result = await api.like(profile.id); notify(result.message || t('likeSent')); haptic('success') }
    catch (caught) { notify(caught instanceof Error ? caught.message : t('serverError')); haptic('error') }
  }

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('nearby')} />
      {!me.location_available ? <LocationPrompt language={me.app_language} error={locationError} loading={locating} onRequest={locate} /> : <>
        <div className="filters">
          <div className="filter-group">
            <span>{t('filterRadius')}</span>
            <div className="chip-row" role="group" aria-label={t('filterRadius')}>
              {RADII.map((value) => (
                <button key={value} type="button" className="chip" aria-pressed={radius === value} onClick={() => { setRadius(value); haptic('select') }}>{value} {t('km')}</button>
              ))}
            </div>
          </div>
          <div className="filter-group">
            <span>{t('filterGender')}</span>
            <div className="chip-row" role="group" aria-label={t('filterGender')}>
              {GENDER_FILTERS.map(([value, label]) => (
                <button key={label} type="button" className="chip" aria-pressed={gender === value} onClick={() => { setGender(value); haptic('select') }}>{t(label)}</button>
              ))}
            </div>
          </div>
        </div>
        {loading && profiles.length === 0 && <div className="skeleton-list">{[1, 2, 3].map((index) => <div className="skeleton-card" key={index}><i /><div><b /><b /><b /></div></div>)}</div>}
        {loadError && <div className="content-state"><span>!</span><p>{loadError}</p><button className="secondary-button" onClick={() => setReloadKey((value) => value + 1)}>{t('retry')}</button></div>}
        {!loading && !loadError && profiles.length === 0 && <div className="content-state"><span>⌖</span><h2>{t('noPeople')}</h2><p>{t('noPeopleBody')}</p></div>}
        {profiles.length > 0 && (
          <div className="people-list">
            {profiles.map((profile) => (
              <ProfileCard key={profile.id} profile={profile} language={me.app_language} onLike={() => sendLike(profile)} onMessage={() => setMessageTarget(profile)} onOpen={() => setOpenProfile(profile)} />
            ))}
          </div>
        )}
        {hasMore && <button disabled={loading} className="secondary-button block-button" onClick={loadMore}>{t('loadMore')}</button>}
      </>}
      {openProfile && <PublicProfileSheet profile={openProfile} language={me.app_language} onClose={() => { setOpenProfile(null); clearInitialProfile() }} onMessage={() => { setMessageTarget(openProfile); setOpenProfile(null) }} />}
      {messageTarget && <MessageSheet profile={messageTarget} language={me.app_language} onClose={() => setMessageTarget(null)} onSent={() => { setMessageTarget(null); notify(t('likeSent')) }} />}
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
        display_name: me.display_name || displayName(me), gender: me.gender || '', birth_date: me.birth_date || '', purpose: me.purpose || '', bio: me.bio || '',
        custom_photo_url: me.custom_photo_url || '', app_language: language, is_active: active,
      })
      onSaved(updated); notify(translator(updated.app_language)('saved')); haptic('success')
    } catch (caught) { notify(caught instanceof Error ? caught.message : t('serverError')); haptic('error') }
    finally { setSaving(false) }
  }

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('settings')} />
      <section className="card">
        <label className="field">{t('language')}
          <select value={language} onChange={(event) => setLanguage(event.target.value as Language)}>
            {LANGUAGES.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
        </label>
      </section>
      <section className="card toggle-row">
        <div><strong>{t('activeProfile')}</strong><p>{t('activeHelp')}</p></div>
        <button role="switch" aria-checked={active} aria-label={t('activeProfile')} className={`switch ${active ? 'on' : ''}`} onClick={() => { setActive(!active); haptic('tap') }}><i /></button>
      </section>
      <button disabled={saving} className="primary-button block-button" onClick={save}>{saving ? t('saving') : t('save')}</button>
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
    const timer = window.setTimeout(() => {
      Promise.all([api.adminStats(), api.adminUsers(search)]).then(([nextStats, result]) => {
        setStats(nextStats); setItems(result.users); setError('')
      }).catch((caught) => setError(caught instanceof Error ? caught.message : t('serverError')))
    }, search ? 250 : 0)
    return () => window.clearTimeout(timer)
  }, [search, me.app_language])

  const cards: [MessageKey, number][] = stats
    ? [['total', stats.total], ['completed', stats.completed], ['incomplete', stats.incomplete], ['active', stats.active], ['blocked', stats.blocked]]
    : []

  return (
    <main className="screen">
      <ScreenHeading eyebrow title={t('adminTitle')} />
      {stats && <div className="stats-grid">{cards.map(([label, value]) => <div key={label}><strong>{value}</strong><span>{t(label)}</span></div>)}</div>}
      <input type="search" aria-label={t('search')} placeholder={t('search')} value={search} onChange={(event) => setSearch(event.target.value)} />
      {error && <div className="inline-error" role="alert">{error}</div>}
      <div className="admin-list">
        {items.map((item) => (
          <article key={item.telegram_user_id} className="admin-user">
            <div><strong>{item.display_name}</strong>{item.username && <span className="username">@{item.username}</span>}<code>{item.telegram_user_id}</code></div>
            <p>{[item.age, item.gender, item.purpose].filter(Boolean).join(' · ')}</p>
            <dl>
              <div><dt>{t('registered')}</dt><dd>{new Date(item.registered_at).toLocaleDateString(me.app_language)}</dd></div>
              <div><dt>{t('lastSeen')}</dt><dd>{new Date(item.last_seen_at).toLocaleString(me.app_language)}</dd></div>
              <div><dt>{t('hasLocation')}</dt><dd>{item.location_available ? t('yes') : t('no')}</dd></div>
            </dl>
          </article>
        ))}
      </div>
    </main>
  )
}

type Tab = 'nearby' | 'profile' | 'settings' | 'admin'

/** Keeps a focused field visible when the on-screen keyboard covers the lower half. */
function useKeyboardAwareFocus() {
  useEffect(() => {
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target as HTMLElement | null
      if (!target?.matches?.('input, textarea, select')) return
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

  useKeyboardAwareFocus()

  useEffect(() => {
    if (initialized.current) return
    initialized.current = true
    const local = ['localhost', '127.0.0.1'].includes(window.location.hostname)
    if (telegramApp?.initData) setAuthorization(`tma ${telegramApp.initData}`)
    else if (local) setAuthorization('dev')
    else { setError(translator('ru')('telegramUnavailable')); setStatus('error'); return }
    api.authenticate().then(async (user) => {
      setMe(user); document.documentElement.lang = user.app_language
      const parameter = startParameter(telegramApp)
      const match = /^profile_([0-9a-f-]{36})$/.exec(parameter)
      if (match) {
        try { setDeepProfile(await api.publicProfile(match[1])) } catch { /* unavailable profiles stay hidden */ }
      }
      setStatus('ready')
    }).catch((caught) => {
      const language = (telegramApp?.initDataUnsafe.user?.language_code?.split('-')[0] as Language) || 'ru'
      const t = translator(['ru', 'kk', 'en'].includes(language) ? language : 'ru')
      if (caught instanceof APIError && caught.code === 'telegram_auth_expired') setError(t('authExpired'))
      else if (caught instanceof TypeError) setError(t('networkError'))
      else setError(t('authFailed'))
      setStatus('error')
    })
  }, [])

  function notify(text: string) {
    setToast(text)
    window.setTimeout(() => setToast(''), 2600)
  }

  if (status === 'loading') return <FullPageState loading title="AikaBot" />
  if (status === 'error' || !me) return <FullPageState icon="!" title="AikaBot" body={error} action={() => window.location.reload()} actionLabel={translator('ru')('retry')} />
  if (!me.is_profile_completed) return <OnboardingScreen me={me} onSaved={(user) => { setMe(user); document.documentElement.lang = user.app_language }} />

  const t = translator(me.app_language)
  const tabs: [Tab, string, MessageKey][] = [['nearby', '⌖', 'nearby'], ['profile', '◉', 'profile'], ['settings', '⚙', 'settings']]
  if (me.is_admin) tabs.push(['admin', '◆', 'admin'])

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="bar-inner">
          <Avatar src={me.photo_url} name={displayName(me)} size="small" />
          <div className="app-header__identity">
            <strong>{displayName(me)}</strong>
            {me.username && <span>@{me.username}</span>}
          </div>
          <button className="language-button" aria-label={t('language')} onClick={() => setTab('settings')}>{me.app_language.toUpperCase()}</button>
        </div>
      </header>
      <div className="app-main">
        <div className="screen-scroll">
          {tab === 'nearby' && <Nearby me={me} notify={notify} initialProfile={deepProfile} clearInitialProfile={() => setDeepProfile(null)} onLocation={() => setMe({ ...me, location_available: true })} />}
          {tab === 'profile' && <ProfileScreen me={me} onSaved={(user) => { setMe(user); notify(translator(user.app_language)('saved')) }} />}
          {tab === 'settings' && <Settings me={me} onSaved={(user) => { setMe(user); document.documentElement.lang = user.app_language }} notify={notify} />}
          {tab === 'admin' && me.is_admin && <Admin me={me} />}
        </div>
        {toast && <div className="toast" role="status">{toast}</div>}
      </div>
      <nav className="bottom-nav">
        <div className="bar-inner">
          {tabs.map(([value, icon, label]) => (
            <button key={value} className={tab === value ? 'active' : ''} aria-current={tab === value ? 'page' : undefined} onClick={() => { setTab(value); haptic('tap') }}>
              <i>{icon}</i><span>{t(label)}</span>
            </button>
          ))}
        </div>
      </nav>
    </div>
  )
}

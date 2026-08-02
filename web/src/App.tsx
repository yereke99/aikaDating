import { FormEvent, useEffect, useRef, useState } from 'react'
import { APIError, api, setAuthorization } from './api'
import { MessageKey, translator } from './i18n'
import { initializeTelegram, LocationFailure, openLocationSettings, requestLocation, startParameter } from './telegram'
import type { AdminStats, AdminUser, Gender, Language, Me, ProfileInput, PublicProfile } from './types'

const telegramApp = initializeTelegram()
const radii = [5, 10, 20, 500]

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]?.toUpperCase()).join('') || 'A'
}

function displayName(me: Me) {
  return me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' ') || me.username || 'AikaBot'
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

function Avatar({ src, name, size = 'normal' }: { src?: string; name: string; size?: 'small' | 'normal' | 'large' }) {
  const [failed, setFailed] = useState(false)
  return (
    <div className={`avatar avatar-${size}`} aria-label={name}>
      {src && !failed ? <img src={src} alt="" onError={() => setFailed(true)} /> : <span>{initials(name)}</span>}
    </div>
  )
}

function FullPageState({ icon, title, body, action, actionLabel }: { icon: string; title: string; body?: string; action?: () => void; actionLabel?: string }) {
  return (
    <main className="full-state">
      <div className="state-icon">{icon}</div>
      <h1>{title}</h1>
      {body && <p>{body}</p>}
      {action && <button className="primary-button" onClick={action}>{actionLabel}</button>}
    </main>
  )
}

function ProfileForm({ me, onboarding, onSaved }: { me: Me; onboarding?: boolean; onSaved: (me: Me) => void }) {
  const t = translator(me.app_language)
  const [form, setForm] = useState<ProfileInput>({
    display_name: me.display_name || [me.first_name, me.last_name].filter(Boolean).join(' '),
    gender: me.gender || '', birth_date: me.birth_date || '', purpose: me.purpose || '', bio: me.bio || '',
    custom_photo_url: me.custom_photo_url || '', app_language: me.app_language, is_active: me.is_active,
  })
  const [saving, setSaving] = useState(false)
  const [uploadingPhoto, setUploadingPhoto] = useState(false)
  const [error, setError] = useState('')
  const galleryInput = useRef<HTMLInputElement>(null)
  const cameraInput = useRef<HTMLInputElement>(null)
  const name = form.display_name || displayName(me)
  const photo = form.custom_photo_url || me.telegram_photo_url

  async function submit(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    setError('')
    try {
      const updated = await api.updateProfile(form)
      telegramApp?.HapticFeedback?.notificationOccurred('success')
      onSaved(updated)
    } catch (caught) {
      telegramApp?.HapticFeedback?.notificationOccurred('error')
      setError(caught instanceof Error ? caught.message : t('serverError'))
    } finally {
      setSaving(false)
    }
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
      telegramApp?.HapticFeedback?.notificationOccurred('success')
    } catch (caught) {
      telegramApp?.HapticFeedback?.notificationOccurred('error')
      setError(caught instanceof APIError ? caught.message : t('invalidPhoto'))
    } finally {
      setUploadingPhoto(false)
    }
  }

  return (
    <main className={`screen profile-screen ${onboarding ? 'onboarding' : ''}`}>
      <section className="screen-heading">
        <p className="eyebrow">AikaBot</p>
        <h1>{onboarding ? t('onboardingTitle') : t('editProfile')}</h1>
        {onboarding && <p>{t('onboardingBody')}</p>}
      </section>
      <form className="profile-form" onSubmit={submit}>
        <div className="photo-preview">
          <Avatar src={photo} name={name} size="large" />
          <div className="photo-copy">
            {me.telegram_photo_url && !form.custom_photo_url && <span className="hint">{t('telegramPhoto')}</span>}
            <div className="photo-actions">
              <button type="button" className="secondary-button" disabled={uploadingPhoto} onClick={() => galleryInput.current?.click()}>{t('choosePhoto')}</button>
              <button type="button" className="secondary-button selfie-button" disabled={uploadingPhoto} onClick={() => cameraInput.current?.click()}>◎ {t('takeSelfie')}</button>
            </div>
            <small>{uploadingPhoto ? t('uploadingPhoto') : t('photoHelp')}</small>
          </div>
          <input ref={galleryInput} className="visually-hidden" type="file" accept="image/jpeg,image/png,image/*" onChange={(event) => { const input = event.currentTarget; void uploadPhoto(input.files?.[0]).finally(() => { input.value = '' }) }} />
          <input ref={cameraInput} className="visually-hidden" type="file" accept="image/*" capture="user" onChange={(event) => { const input = event.currentTarget; void uploadPhoto(input.files?.[0]).finally(() => { input.value = '' }) }} />
        </div>
        <label>{t('displayName')}<input required minLength={2} maxLength={80} value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} /></label>
        <div className="field-row">
          <label>{t('gender')}
            <select required value={form.gender} onChange={(e) => setForm({ ...form, gender: e.target.value as Gender })}>
              <option value="" disabled>—</option><option value="male">{t('male')}</option><option value="female">{t('female')}</option><option value="other">{t('other')}</option>
            </select>
          </label>
          <label>{t('birthDate')}<input required type="date" value={form.birth_date} onChange={(e) => setForm({ ...form, birth_date: e.target.value })} /></label>
        </div>
        <label>{t('purpose')}<input required minLength={2} maxLength={120} value={form.purpose} onChange={(e) => setForm({ ...form, purpose: e.target.value })} /></label>
        <label>{t('bio')}<textarea maxLength={500} rows={4} value={form.bio} onChange={(e) => setForm({ ...form, bio: e.target.value })} /></label>
        <label>{t('language')}
          <select value={form.app_language} onChange={(e) => setForm({ ...form, app_language: e.target.value as Language })}>
            <option value="ru">Русский</option><option value="kk">Қазақша</option><option value="en">English</option>
          </select>
        </label>
        {error && <div className="inline-error" role="alert">{error}</div>}
        <button disabled={saving} className="primary-button" type="submit">{saving ? t('saving') : t('save')}</button>
      </form>
    </main>
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
          <div className="person-title-row"><h3>{profile.display_name}</h3>{profile.distance_km !== undefined && <span className="distance">{profile.distance_km} {t('km')}</span>}</div>
          {profile.username && <p className="username">@{profile.username}</p>}
          <p className="person-meta">{meta}</p>
          {profile.purpose && <p className="purpose">{profile.purpose}</p>}
        </div>
      </button>
      <div className="card-actions">
        <button className="like-button" onClick={onLike}>♥ <span>{t('like')}</span></button>
        <button className="message-button" onClick={onMessage}>✦ <span>{t('message')}</span></button>
      </div>
    </article>
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
    setSending(true); setError('')
    try {
      await api.like(profile.id, message.trim())
      onSent()
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : t('serverError'))
      setSending(false)
    }
  }
  return (
    <div className="sheet-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <form className="bottom-sheet" onSubmit={send}>
        <div className="sheet-handle" />
        <div className="sheet-person"><Avatar src={profile.photo_url} name={profile.display_name} size="small" /><div><strong>{profile.display_name}</strong>{profile.username && <span>@{profile.username}</span>}</div></div>
        <textarea autoFocus required maxLength={300} rows={4} placeholder={t('messagePlaceholder')} value={message} onChange={(e) => setMessage(e.target.value)} />
        <div className="counter">{Array.from(message).length}/300</div>
        {error && <div className="inline-error">{error}</div>}
        <div className="sheet-actions"><button type="button" className="secondary-button" onClick={onClose}>{t('cancel')}</button><button disabled={sending || !message.trim()} className="primary-button">{sending ? '…' : t('send')}</button></div>
      </form>
    </div>
  )
}

function PublicProfileSheet({ profile, language, onClose, onMessage }: { profile: PublicProfile; language: Language; onClose: () => void; onMessage: () => void }) {
  const t = translator(language)
  return (
    <div className="sheet-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <section className="bottom-sheet public-sheet">
        <div className="sheet-handle" />
        <Avatar src={profile.photo_url} name={profile.display_name} size="large" />
        <h2>{profile.display_name}</h2>
        {profile.username && <p className="username">@{profile.username}</p>}
        <p className="person-meta">{[profile.age, profile.gender && t(profile.gender)].filter(Boolean).join(' · ')}</p>
        {profile.distance_km !== undefined && <p className="distance-line">⌖ {t('distance')} {profile.distance_km} {t('km')}</p>}
        {profile.purpose && <div className="detail-block"><span>{t('purpose')}</span><p>{profile.purpose}</p></div>}
        {profile.bio && <div className="detail-block"><span>{t('bio')}</span><p>{profile.bio}</p></div>}
        <div className="sheet-actions"><button className="secondary-button" onClick={onClose}>{t('close')}</button><button className="primary-button" onClick={onMessage}>{t('message')}</button></div>
      </section>
    </div>
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
    try { const result = await api.like(profile.id); notify(result.message || t('likeSent')); telegramApp?.HapticFeedback?.notificationOccurred('success') }
    catch (caught) { notify(caught instanceof Error ? caught.message : t('serverError')); telegramApp?.HapticFeedback?.notificationOccurred('error') }
  }

  return (
    <main className="screen nearby-screen">
      <section className="screen-heading compact"><p className="eyebrow">AikaBot</p><h1>{t('nearby')}</h1></section>
      {!me.location_available ? <LocationPrompt language={me.app_language} error={locationError} loading={locating} onRequest={locate} /> : <>
        <div className="filters-row">
          <div className="radius-filter">{radii.map((value) => <button key={value} className={radius === value ? 'active' : ''} onClick={() => setRadius(value)}>{value} {t('km')}</button>)}</div>
          <select aria-label={t('gender')} value={gender} onChange={(e) => setGender(e.target.value)}><option value="">{t('all')}</option><option value="male">{t('male')}</option><option value="female">{t('female')}</option><option value="other">{t('other')}</option></select>
        </div>
        {loading && profiles.length === 0 && <div className="skeleton-list">{[1, 2, 3].map((i) => <div className="skeleton-card" key={i}><i /><div><b /><b /><b /></div></div>)}</div>}
        {loadError && <div className="content-state"><span>!</span><p>{loadError}</p><button className="secondary-button" onClick={() => setReloadKey((value) => value + 1)}>{t('retry')}</button></div>}
        {!loading && !loadError && profiles.length === 0 && <div className="content-state"><span>⌖</span><h2>{t('noPeople')}</h2><p>{t('noPeopleBody')}</p></div>}
        <div className="people-list">{profiles.map((profile) => <ProfileCard key={profile.id} profile={profile} language={me.app_language} onLike={() => sendLike(profile)} onMessage={() => setMessageTarget(profile)} onOpen={() => setOpenProfile(profile)} />)}</div>
        {hasMore && <button disabled={loading} className="secondary-button load-more" onClick={loadMore}>{t('loadMore')}</button>}
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
      onSaved(updated); notify(translator(updated.app_language)('saved'))
    } catch (caught) { notify(caught instanceof Error ? caught.message : t('serverError')) }
    finally { setSaving(false) }
  }
  return (
    <main className="screen settings-screen">
      <section className="screen-heading compact"><p className="eyebrow">AikaBot</p><h1>{t('settings')}</h1></section>
      <section className="settings-card"><label>{t('language')}<select value={language} onChange={(e) => setLanguage(e.target.value as Language)}><option value="ru">Русский</option><option value="kk">Қазақша</option><option value="en">English</option></select></label></section>
      <section className="settings-card toggle-row"><div><strong>{t('activeProfile')}</strong><p>{t('activeHelp')}</p></div><button role="switch" aria-checked={active} className={`switch ${active ? 'on' : ''}`} onClick={() => setActive(!active)}><i /></button></section>
      <button disabled={saving} className="primary-button settings-save" onClick={save}>{saving ? t('saving') : t('save')}</button>
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
  return (
    <main className="screen admin-screen">
      <section className="screen-heading compact"><p className="eyebrow">AikaBot</p><h1>{t('adminTitle')}</h1></section>
      {stats && <div className="stats-grid">{([['total', stats.total], ['completed', stats.completed], ['incomplete', stats.incomplete], ['active', stats.active], ['blocked', stats.blocked]] as [MessageKey, number][]).map(([label, value]) => <div key={label}><strong>{value}</strong><span>{t(label)}</span></div>)}</div>}
      <input className="search-input" type="search" placeholder={t('search')} value={search} onChange={(e) => setSearch(e.target.value)} />
      {error && <div className="inline-error">{error}</div>}
      <div className="admin-list">{items.map((item) => <article key={item.telegram_user_id} className="admin-user"><div><strong>{item.display_name}</strong>{item.username && <span>@{item.username}</span>}<code>{item.telegram_user_id}</code></div><p>{[item.age, item.gender, item.purpose].filter(Boolean).join(' · ')}</p><dl><div><dt>{t('registered')}</dt><dd>{new Date(item.registered_at).toLocaleDateString(me.app_language)}</dd></div><div><dt>{t('lastSeen')}</dt><dd>{new Date(item.last_seen_at).toLocaleString(me.app_language)}</dd></div><div><dt>{t('hasLocation')}</dt><dd>{item.location_available ? t('yes') : t('no')}</dd></div></dl></article>)}</div>
    </main>
  )
}

type Tab = 'nearby' | 'profile' | 'settings' | 'admin'

export default function App() {
  const initialized = useRef(false)
  const [me, setMe] = useState<Me | null>(null)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [error, setError] = useState('')
  const [tab, setTab] = useState<Tab>('nearby')
  const [toast, setToast] = useState('')
  const [deepProfile, setDeepProfile] = useState<PublicProfile | null>(null)

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

  if (status === 'loading') return <FullPageState icon="♥" title="AikaBot" body="…" />
  if (status === 'error' || !me) return <FullPageState icon="!" title="AikaBot" body={error} action={() => window.location.reload()} actionLabel={translator('ru')('retry')} />
  if (!me.is_profile_completed) return <ProfileForm me={me} onboarding onSaved={(user) => { setMe(user); document.documentElement.lang = user.app_language }} />

  const t = translator(me.app_language)
  return (
    <div className="app-shell">
      <header className="profile-header">
        <Avatar src={me.photo_url} name={displayName(me)} size="small" />
        <div><strong>{displayName(me)}</strong>{me.username && <span>@{me.username}</span>}</div>
        <button aria-label={t('settings')} onClick={() => setTab('settings')}>{me.app_language.toUpperCase()}</button>
      </header>
      <div className="screen-scroll">
        {tab === 'nearby' && <Nearby me={me} notify={notify} initialProfile={deepProfile} clearInitialProfile={() => setDeepProfile(null)} onLocation={() => setMe({ ...me, location_available: true })} />}
        {tab === 'profile' && <ProfileForm me={me} onSaved={(user) => { setMe(user); notify(translator(user.app_language)('saved')) }} />}
        {tab === 'settings' && <Settings me={me} onSaved={(user) => { setMe(user); document.documentElement.lang = user.app_language }} notify={notify} />}
        {tab === 'admin' && me.is_admin && <Admin me={me} />}
      </div>
      <nav className="bottom-nav">
        <button className={tab === 'nearby' ? 'active' : ''} onClick={() => setTab('nearby')}><i>⌖</i><span>{t('nearby')}</span></button>
        <button className={tab === 'profile' ? 'active' : ''} onClick={() => setTab('profile')}><i>◉</i><span>{t('profile')}</span></button>
        <button className={tab === 'settings' ? 'active' : ''} onClick={() => setTab('settings')}><i>⚙</i><span>{t('settings')}</span></button>
        {me.is_admin && <button className={tab === 'admin' ? 'active' : ''} onClick={() => setTab('admin')}><i>◆</i><span>{t('admin')}</span></button>}
      </nav>
      {toast && <div className="toast" role="status">{toast}</div>}
    </div>
  )
}

import { useState } from 'react'

function initials(name: string) {
  return (
    name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part[0]?.toUpperCase())
      .join('') || 'A'
  )
}

export function Avatar({ src, name, size = 'normal' }: { src?: string; name: string; size?: 'small' | 'normal' | 'large' }) {
  const [failed, setFailed] = useState(false)
  return (
    <div className={`avatar avatar-${size}`} aria-label={name}>
      {src && !failed ? <img src={src} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} /> : <span>{initials(name)}</span>}
    </div>
  )
}

export { initials }

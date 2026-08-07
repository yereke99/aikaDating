import { FormEvent, ReactNode, useEffect, useState } from 'react'
import { useBackButton } from '../hooks'

/**
 * The one bottom-sheet primitive. Its height is bound to the *live* viewport, so when the keyboard
 * opens the sheet shrinks with it instead of sliding underneath it: the footer — where the primary
 * action lives — stays on screen without any per-screen offset maths.
 *
 * Layout contract for callers: `<SheetHead>` and `<SheetFoot>` are pinned, everything between them
 * scrolls.
 */
export function Sheet({
  onClose,
  onSubmit,
  className,
  labelledBy,
  children,
}: {
  onClose: () => void
  onSubmit?: (event: FormEvent) => void
  className?: string
  labelledBy?: string
  children: ReactNode
}) {
  useBackButton(true, onClose)

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const sheetClass = `bottom-sheet${className ? ` ${className}` : ''}`
  const shared = { className: sheetClass, role: 'dialog' as const, 'aria-modal': true, 'aria-labelledby': labelledBy }
  return (
    <div
      className="sheet-backdrop"
      // Only a press that starts and ends on the backdrop dismisses, so a text selection that
      // drifts out of the textarea cannot close the sheet and lose what was typed.
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) event.currentTarget.dataset.dismiss = 'armed'
      }}
      onPointerUp={(event) => {
        const armed = event.currentTarget.dataset.dismiss === 'armed'
        delete event.currentTarget.dataset.dismiss
        if (armed && event.target === event.currentTarget) onClose()
      }}
    >
      {onSubmit ? <form {...shared} onSubmit={onSubmit}>{children}</form> : <section {...shared}>{children}</section>}
    </div>
  )
}

export function SheetHead({ children }: { children: ReactNode }) {
  return (
    <header className="sheet-head">
      <div className="sheet-handle" />
      {children}
    </header>
  )
}

export function SheetBody({ children }: { children: ReactNode }) {
  return <div className="sheet-body">{children}</div>
}

export function SheetFoot({ children }: { children: ReactNode }) {
  return <footer className="sheet-foot">{children}</footer>
}

/**
 * Tracks whether the on-screen keyboard is up. Used to trim decoration — a handle, a headline —
 * out of the way so the composer keeps its full height on a short viewport.
 */
export function useKeyboardOpen(): boolean {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const viewport = window.visualViewport
    if (!viewport) return undefined
    const sync = () => {
      const covered = Math.max(0, window.innerHeight - viewport.height - viewport.offsetTop)
      setOpen(covered > 120 || viewport.height < window.innerHeight * 0.78)
    }
    sync()
    viewport.addEventListener('resize', sync)
    viewport.addEventListener('scroll', sync)
    return () => {
      viewport.removeEventListener('resize', sync)
      viewport.removeEventListener('scroll', sync)
    }
  }, [])
  return open
}

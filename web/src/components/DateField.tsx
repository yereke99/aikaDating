import { useMemo, useState } from 'react'
import { MessageKey, translator } from '../i18n'
import {
  CalendarDate,
  birthDateBounds,
  clampDay,
  daysInMonth,
  displayCalendarDate,
  formatCalendarDate,
  monthNames,
  parseCalendarDate,
} from '../lib/date'
import type { Language } from '../types'
import { Sheet, SheetBody, SheetFoot, SheetHead } from './Sheet'

/**
 * Birth date picker.
 *
 * `<input type="date">` is what used to break this screen: inside Telegram's WebView iOS renders it
 * with an intrinsic width that ignores the column it sits in, so the control spilled out of the
 * card, and the value it produces is a string the surrounding code was tempted to push through
 * `new Date(...)`. This control is a plain button plus three selects in a sheet — it can only be as
 * wide as its container, it reads in the app's own language, and the value never stops being a
 * `YYYY-MM-DD` calendar date.
 */
export function DateField({
  label,
  value,
  language,
  minAge,
  maxAge,
  invalid,
  onChange,
}: {
  label: string
  value: string
  language: Language
  minAge: number
  maxAge: number
  invalid?: boolean
  onChange: (value: string) => void
}) {
  const t = translator(language)
  const [open, setOpen] = useState(false)
  const display = value ? displayCalendarDate(value, language) : ''

  return (
    <div className="field">
      <span className="field-label">{label}</span>
      <button
        type="button"
        className={`field-control date-control${invalid ? ' is-invalid' : ''}`}
        aria-invalid={invalid || undefined}
        onClick={() => setOpen(true)}
      >
        <span className={display ? '' : 'placeholder'}>{display || t('selectDate')}</span>
        <i aria-hidden="true">▾</i>
      </button>
      {open && (
        <DateSheet
          value={value}
          language={language}
          minAge={minAge}
          maxAge={maxAge}
          onCancel={() => setOpen(false)}
          onConfirm={(next) => {
            onChange(next)
            setOpen(false)
          }}
        />
      )}
    </div>
  )
}

function DateSheet({
  value,
  language,
  minAge,
  maxAge,
  onCancel,
  onConfirm,
}: {
  value: string
  language: Language
  minAge: number
  maxAge: number
  onCancel: () => void
  onConfirm: (value: string) => void
}) {
  const t = translator(language)
  const bounds = useMemo(() => birthDateBounds(minAge, maxAge), [minAge, maxAge])
  const [draft, setDraft] = useState<CalendarDate>(() => parseCalendarDate(value) ?? { year: bounds.max.year, month: 1, day: 1 })

  const years = useMemo(() => {
    const list: number[] = []
    for (let year = bounds.max.year; year >= bounds.min.year; year -= 1) list.push(year)
    return list
  }, [bounds.max.year, bounds.min.year])
  const months = useMemo(() => monthNames(language), [language])
  const days = useMemo(() => Array.from({ length: daysInMonth(draft.year, draft.month) }, (_, index) => index + 1), [draft.year, draft.month])

  // A day that no longer exists after a month or year change is pulled back to the last valid one
  // rather than silently rolling into the next month.
  const update = (patch: Partial<CalendarDate>) => setDraft((current) => clampDay({ ...current, ...patch }))

  const select = (name: MessageKey, current: number, options: { value: number; label: string }[], onPick: (value: number) => void) => (
    <label className="field">
      <span className="field-label">{t(name)}</span>
      <select className="field-control" value={current} onChange={(event) => onPick(Number(event.target.value))}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  )

  return (
    <Sheet onClose={onCancel} labelledBy="date-sheet-title" className="date-sheet">
      <SheetHead>
        <h2 id="date-sheet-title">{t('selectDate')}</h2>
      </SheetHead>
      <SheetBody>
        <div className="date-grid">
          {select(
            'day',
            draft.day,
            days.map((day) => ({ value: day, label: String(day) })),
            (day) => update({ day }),
          )}
          {select(
            'month',
            draft.month,
            months.map((name, index) => ({ value: index + 1, label: name })),
            (month) => update({ month }),
          )}
          {select(
            'year',
            draft.year,
            years.map((year) => ({ value: year, label: String(year) })),
            (year) => update({ year }),
          )}
        </div>
        <p className="field-hint">{displayCalendarDate(formatCalendarDate(draft), language)}</p>
      </SheetBody>
      <SheetFoot>
        <div className="sheet-actions">
          <button type="button" className="secondary-button" onClick={onCancel}>
            {t('cancel')}
          </button>
          <button type="button" className="primary-button" onClick={() => onConfirm(formatCalendarDate(draft))}>
            {t('done')}
          </button>
        </div>
      </SheetFoot>
    </Sheet>
  )
}

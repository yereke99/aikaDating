import type { Language } from '../types'

/**
 * A birth date is a calendar date, not an instant. Everything here works on the `YYYY-MM-DD` string
 * the API uses and on plain numbers — no value is ever routed through `Date.toISOString()`, which is
 * what shifts a birthday by a day for anyone east or west of UTC.
 */
export interface CalendarDate {
  year: number
  month: number // 1-12
  day: number // 1-31
}

const PATTERN = /^(\d{4})-(\d{2})-(\d{2})$/

export function daysInMonth(year: number, month: number): number {
  if (month === 2) return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0 ? 29 : 28
  return [4, 6, 9, 11].includes(month) ? 30 : 31
}

/** Parses `YYYY-MM-DD`, rejecting impossible dates such as 2025-02-30. */
export function parseCalendarDate(value: string): CalendarDate | null {
  const parts = PATTERN.exec(value.trim())
  if (!parts) return null
  const year = Number(parts[1])
  const month = Number(parts[2])
  const day = Number(parts[3])
  if (month < 1 || month > 12) return null
  if (day < 1 || day > daysInMonth(year, month)) return null
  return { year, month, day }
}

export function formatCalendarDate(date: CalendarDate): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.year}-${pad(date.month)}-${pad(date.day)}`
}

/** Clamps the day when a month or year change makes the current day impossible (e.g. 31 → 30). */
export function clampDay(date: CalendarDate): CalendarDate {
  return { ...date, day: Math.min(date.day, daysInMonth(date.year, date.month)) }
}

export function todayCalendarDate(now = new Date()): CalendarDate {
  return { year: now.getFullYear(), month: now.getMonth() + 1, day: now.getDate() }
}

/** Whole years between two calendar dates. Mirrors the server's rule exactly. */
export function ageOn(birth: CalendarDate, today: CalendarDate): number {
  let age = today.year - birth.year
  if (today.month < birth.month || (today.month === birth.month && today.day < birth.day)) age -= 1
  return age
}

export function ageFromBirthDate(value: string, now = new Date()): number | null {
  const birth = parseCalendarDate(value)
  return birth ? ageOn(birth, todayCalendarDate(now)) : null
}

/**
 * Month names are carried by the app rather than taken from `Intl`. A WebView built with trimmed
 * ICU data renders Kazakh months as "M11", and a date of birth is not a place to show that. These
 * lists also give Russian its genitive display form, which `Intl` only produces for some locales.
 */
const MONTHS: Record<Language, string[]> = {
  ru: ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'],
  kk: ['Қаңтар', 'Ақпан', 'Наурыз', 'Сәуір', 'Мамыр', 'Маусым', 'Шілде', 'Тамыз', 'Қыркүйек', 'Қазан', 'Қараша', 'Желтоқсан'],
  en: ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'],
}

/** Russian names a date with the genitive month: "2 ноября 2006 г." */
const MONTHS_IN_DATE: Partial<Record<Language, string[]>> = {
  ru: ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'],
}

/** Renders the date for display only; the stored value is never derived from this. */
export function displayCalendarDate(value: string, language: Language): string {
  const date = parseCalendarDate(value)
  if (!date) return ''
  const names = MONTHS_IN_DATE[language] ?? MONTHS[language] ?? MONTHS.ru
  const month = names[date.month - 1]
  if (language === 'kk') return `${date.day} ${month.toLowerCase()} ${date.year} ж.`
  if (language === 'en') return `${date.day} ${month} ${date.year}`
  return `${date.day} ${month} ${date.year} г.`
}

export function monthNames(language: Language): string[] {
  return MONTHS[language] ?? MONTHS.ru
}

/** Oldest and youngest selectable birth dates for the configured age range. */
export function birthDateBounds(minAge: number, maxAge: number, now = new Date()) {
  const today = todayCalendarDate(now)
  return {
    min: { ...today, year: today.year - maxAge },
    max: clampDay({ ...today, year: today.year - minAge }),
  }
}

export function isWithinBounds(date: CalendarDate, minAge: number, maxAge: number, now = new Date()): boolean {
  const age = ageOn(date, todayCalendarDate(now))
  return age >= minAge && age <= maxAge
}

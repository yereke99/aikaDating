import test from 'node:test'
import assert from 'node:assert/strict'

import {
  ageFromBirthDate,
  ageOn,
  birthDateBounds,
  clampDay,
  daysInMonth,
  displayCalendarDate,
  formatCalendarDate,
  isWithinBounds,
  monthNames,
  parseCalendarDate,
} from './date.ts'

test('parses and formats a calendar date without touching the clock', () => {
  const parsed = parseCalendarDate('2006-11-02')
  assert.deepEqual(parsed, { year: 2006, month: 11, day: 2 })
  assert.equal(formatCalendarDate(parsed!), '2006-11-02')
})

test('rejects impossible and malformed dates', () => {
  for (const value of ['2025-02-30', '2025-13-01', '2025-00-10', '2025-1-1', 'yesterday', '', '2025-04-31']) {
    assert.equal(parseCalendarDate(value), null, value)
  }
  assert.deepEqual(parseCalendarDate('2024-02-29'), { year: 2024, month: 2, day: 29 })
})

test('a round trip never shifts the day in any timezone', () => {
  // The two offsets that historically broke ISO-based handling: UTC+13 rolls a morning date
  // forward, UTC-11 rolls it back.
  for (const zone of ['Pacific/Apia', 'Pacific/Midway', 'UTC', 'Asia/Almaty']) {
    process.env.TZ = zone
    for (const value of ['2006-11-02', '2000-01-01', '1999-12-31', '2024-02-29']) {
      assert.equal(formatCalendarDate(parseCalendarDate(value)!), value, `${zone} ${value}`)
    }
  }
  process.env.TZ = 'UTC'
})

test('day counts follow the Gregorian leap rule', () => {
  assert.equal(daysInMonth(2024, 2), 29)
  assert.equal(daysInMonth(2025, 2), 28)
  assert.equal(daysInMonth(2000, 2), 29)
  assert.equal(daysInMonth(1900, 2), 28)
  assert.equal(daysInMonth(2025, 4), 30)
  assert.equal(daysInMonth(2025, 12), 31)
})

test('changing month clamps an impossible day instead of overflowing', () => {
  assert.deepEqual(clampDay({ year: 2025, month: 2, day: 31 }), { year: 2025, month: 2, day: 28 })
  assert.deepEqual(clampDay({ year: 2024, month: 2, day: 31 }), { year: 2024, month: 2, day: 29 })
  assert.deepEqual(clampDay({ year: 2025, month: 1, day: 31 }), { year: 2025, month: 1, day: 31 })
})

test('age matches the server rule on and around the birthday', () => {
  const birth = { year: 2000, month: 8, day: 5 }
  assert.equal(ageOn(birth, { year: 2026, month: 8, day: 4 }), 25)
  assert.equal(ageOn(birth, { year: 2026, month: 8, day: 5 }), 26)
  assert.equal(ageOn(birth, { year: 2026, month: 8, day: 6 }), 26)
  assert.equal(ageOn(birth, { year: 2026, month: 7, day: 31 }), 25)
})

test('age from a birth date string is null when unparseable', () => {
  const now = new Date(2026, 7, 5, 12)
  assert.equal(ageFromBirthDate('2000-08-05', now), 26)
  assert.equal(ageFromBirthDate('not-a-date', now), null)
})

test('bounds cover exactly the allowed age range', () => {
  const now = new Date(2026, 7, 5, 12)
  const bounds = birthDateBounds(18, 100, now)
  assert.deepEqual(bounds.max, { year: 2008, month: 8, day: 5 })
  assert.deepEqual(bounds.min, { year: 1926, month: 8, day: 5 })
  assert.equal(isWithinBounds({ year: 2008, month: 8, day: 5 }, 18, 100, now), true)
  assert.equal(isWithinBounds({ year: 2008, month: 8, day: 6 }, 18, 100, now), false)
  assert.equal(isWithinBounds({ year: 1925, month: 1, day: 1 }, 18, 100, now), false)
})

test('display formatting stays on the selected calendar day in every language', () => {
  // Named months come from the app, not from the runtime's ICU data, so a WebView with trimmed
  // locale data still reads "қараша" rather than "M11".
  for (const zone of ['Pacific/Apia', 'Pacific/Midway', 'UTC']) {
    process.env.TZ = zone
    assert.equal(displayCalendarDate('2006-11-02', 'en'), '2 November 2006', zone)
    assert.equal(displayCalendarDate('2006-11-02', 'ru'), '2 ноября 2006 г.', zone)
    assert.equal(displayCalendarDate('2006-11-02', 'kk'), '2 қараша 2006 ж.', zone)
  }
  assert.equal(displayCalendarDate('broken', 'ru'), '')
  process.env.TZ = 'UTC'
})

test('picker month lists are complete and localized', () => {
  for (const language of ['ru', 'kk', 'en'] as const) {
    const names = monthNames(language)
    assert.equal(names.length, 12, language)
    assert.equal(new Set(names).size, 12, language)
    assert.ok(names.every((name) => name.length > 2), language)
  }
  assert.equal(monthNames('kk')[10], 'Қараша')
})

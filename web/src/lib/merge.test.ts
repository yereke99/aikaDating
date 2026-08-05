import test from 'node:test'
import assert from 'node:assert/strict'

import { appendPage, mergeById, mergeWindow, moveItem, sameList } from './merge.ts'

const person = (id: string, distance: number) => ({ id, display_name: id, distance_km: distance })

test('unchanged users keep their exact object, so memoised cards do not rerender', () => {
  const current = [person('a', 1), person('b', 2)]
  const merged = mergeById(current, [person('a', 1), person('b', 2)])
  assert.equal(merged[0], current[0])
  assert.equal(merged[1], current[1])
  assert.equal(sameList(current, merged), true)
})

test('a changed user is replaced while its neighbours are untouched', () => {
  const current = [person('a', 1), person('b', 2)]
  const merged = mergeById(current, [person('a', 1), person('b', 3)])
  assert.equal(merged[0], current[0])
  assert.notEqual(merged[1], current[1])
  assert.equal(merged[1].distance_km, 3)
  assert.equal(sameList(current, merged), false)
})

test('polling never duplicates a card', () => {
  let list = [person('a', 1)]
  for (let poll = 0; poll < 5; poll += 1) {
    list = mergeById(list, [person('a', 1), person('b', 2)])
  }
  assert.deepEqual(
    list.map((item) => item.id),
    ['a', 'b'],
  )
})

test('users who leave the radius disappear and newcomers land at their ranked position', () => {
  const current = [person('a', 1), person('b', 2), person('c', 3)]
  const merged = mergeById(current, [person('new', 0.5), person('a', 1), person('c', 3)])
  assert.deepEqual(
    merged.map((item) => item.id),
    ['new', 'a', 'c'],
  )
})

test('existing cards hold their position when the server reshuffles by a small distance change', () => {
  const current = [person('a', 1), person('b', 2)]
  // The server now ranks b first, but both are already on screen: order is left alone.
  const merged = mergeById(current, [person('b', 0.9), person('a', 1)])
  assert.deepEqual(
    merged.map((item) => item.id),
    ['a', 'b'],
  )
})

test('paging appends without repeating anything already listed', () => {
  const current = [person('a', 1), person('b', 2)]
  const next = appendPage(current, [person('b', 2), person('c', 3)])
  assert.deepEqual(
    next.map((item) => item.id),
    ['a', 'b', 'c'],
  )
})

test('moving a photo reorders without losing or duplicating entries', () => {
  const photos = ['one', 'two', 'three']
  assert.deepEqual(moveItem(photos, 2, 0), ['three', 'one', 'two'])
  assert.deepEqual(moveItem(photos, 0, 2), ['two', 'three', 'one'])
  assert.deepEqual(moveItem(photos, 1, 1), photos)
  assert.deepEqual(moveItem(photos, 0, 5), photos)
  assert.deepEqual(moveItem(photos, -1, 0), photos)
  assert.deepEqual(photos, ['one', 'two', 'three'], 'the source array is not mutated')
})

test('a first-page refresh does not delete results loaded from later pages', () => {
  const current = [person('a', 1), person('b', 2), person('c', 3), person('d', 4)]
  // The poll covers the first two ranks only: b left the radius, d is simply out of scope.
  const merged = mergeWindow(current, [person('a', 1), person('new', 1.5)], 2)
  assert.deepEqual(
    merged.map((item) => item.id),
    ['a', 'new', 'c', 'd'],
  )
})

test('a refresh covering the whole list still removes people who left', () => {
  const current = [person('a', 1), person('b', 2)]
  const merged = mergeWindow(current, [person('a', 1)], 20)
  assert.deepEqual(
    merged.map((item) => item.id),
    ['a'],
  )
})

import { describe, expect, it } from 'vitest'
import { formatCents } from './money'

describe('formatCents', () => {
  it.each([
    [0, '$0.00'],
    [1, '$0.01'],
    [2400, '$24.00'],
    [3800, '$38.00'],
    [16400, '$164.00'],
    // Six figures: the grouping separator is easy to lose to a naive
    // toFixed(2) implementation.
    [123456789, '$1,234,567.89'],
  ])('formats %i cents as %s', (cents, want) => {
    expect(formatCents(cents)).toBe(want)
  })

  // A float here means cents were divided somewhere upstream — rule 5 of the
  // handoff. Silently rounding would hide a real money bug.
  it.each([12.5, 0.1, NaN])('refuses non-integer cents (%s)', (cents) => {
    expect(() => formatCents(cents)).toThrow(TypeError)
  })
})

import { describe, expect, it } from 'vitest'
import { relativeTime, remaining } from './time'

const NOW = Date.parse('2026-09-01T12:00:00Z')

/** RFC3339 with a zone, which is what the API emits. */
function at(offsetMs: number): string {
  return new Date(NOW + offsetMs).toISOString()
}

describe('remaining', () => {
  it('counts down from expires_at, not from a hardcoded TTL', () => {
    // The point of rule 7: RESERVATION_TTL is server config. A 20-minute hold
    // must render as 20 minutes even though the default is 15.
    expect(remaining(at(20 * 60_000), NOW).label).toBe('20:00')
    expect(remaining(at(15 * 60_000), NOW).label).toBe('15:00')
    expect(remaining(at(90_000), NOW).label).toBe('01:30')
    expect(remaining(at(9_000), NOW).label).toBe('00:09')
  })

  it('floors rather than rounds', () => {
    // 59.6s left rounded up would show 01:00 — a minute that does not exist,
    // and the timer would appear to stall for half a second.
    expect(remaining(at(59_600), NOW).label).toBe('00:59')
  })

  it('clamps at zero and reports expiry', () => {
    const past = remaining(at(-5_000), NOW)
    expect(past.ms).toBe(0)
    expect(past.expired).toBe(true)
    expect(past.label).toBe('00:00')
  })

  it('treats the exact deadline as expired', () => {
    expect(remaining(at(0), NOW).expired).toBe(true)
  })

  it('is timezone-independent', () => {
    // Same instant, two zone offsets. The arithmetic is on epoch ms, so the
    // client's own timezone must not change the answer.
    const utc = remaining('2026-09-01T12:10:00Z', NOW)
    const offset = remaining('2026-09-01T14:10:00+02:00', NOW)
    expect(offset.label).toBe(utc.label)
    expect(utc.label).toBe('10:00')
  })

  // An order with no hold left to run (completed, cancelled) has no expiry.
  // That is not the same as expired — showing "00:00" would read as a failure.
  it.each([null, undefined, ''])('renders a placeholder for a missing expiry (%s)', (value) => {
    const r = remaining(value, NOW)
    expect(r.label).toBe('--:--')
    expect(r.expired).toBe(false)
  })

  it('does not treat an unparseable timestamp as expired', () => {
    expect(remaining('not-a-date', NOW).expired).toBe(false)
  })
})

describe('relativeTime', () => {
  it.each([
    [-30_000, 'just now'],
    [-2 * 60_000, '2 min ago'],
    [-3 * 3600_000, '3 hr ago'],
    [-30 * 3600_000, 'Yesterday'],
  ])('renders %i ms ago as %s', (offset, want) => {
    expect(relativeTime(at(offset), NOW)).toBe(want)
  })

  it('falls back to a date for anything older', () => {
    expect(relativeTime(at(-10 * 86_400_000), NOW)).toMatch(/Aug/)
  })

  it.each([null, undefined, 'nonsense'])('is empty for %s', (value) => {
    expect(relativeTime(value, NOW)).toBe('')
  })
})

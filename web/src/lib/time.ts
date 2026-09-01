/**
 * The reservation countdown.
 *
 * Rule 7 of the handoff: the hold length is server configuration
 * (RESERVATION_TTL, default 15m) and may change without a frontend deploy, so
 * every timer here derives from the order's own `expires_at`. There is no
 * literal 15:00 anywhere in this app.
 *
 * Rule 6: timestamps are RFC3339 with a zone. Date parses those correctly, and
 * the arithmetic below is on absolute epoch milliseconds, so the client's own
 * timezone never enters into it.
 */

export interface Remaining {
  /** Whole milliseconds left, clamped at zero. */
  ms: number
  /** True once the hold is gone. The sweeper will release the stock. */
  expired: boolean
  /** mm:ss, the format the design uses in the held-unit banner. */
  label: string
}

export function remaining(expiresAt: string | null | undefined, now: number = Date.now()): Remaining {
  // An order that never reserved stock (already completed, cancelled) can come
  // back with no expiry. That is not "expired" — there is simply no countdown.
  if (!expiresAt) {
    return { ms: 0, expired: false, label: '--:--' }
  }

  const deadline = Date.parse(expiresAt)
  if (Number.isNaN(deadline)) {
    return { ms: 0, expired: false, label: '--:--' }
  }

  const ms = Math.max(0, deadline - now)
  return { ms, expired: ms === 0, label: formatDuration(ms) }
}

function formatDuration(ms: number): string {
  // Floor, not round: with 59.6s left a rounded "01:00" would show a full
  // minute that does not exist, and the timer would appear to stall.
  const total = Math.floor(ms / 1000)
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return `${pad(minutes)}:${pad(seconds)}`
}

function pad(n: number): string {
  return n.toString().padStart(2, '0')
}

/** "2 min ago" / "Yesterday", for the orders list. */
export function relativeTime(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return ''

  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''

  const seconds = Math.round((now - then) / 1000)
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)} hr ago`
  if (seconds < 172_800) return 'Yesterday'

  return new Date(then).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

import type { ButtonHTMLAttributes, ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { formatCents } from '../lib/money'
import { remaining } from '../lib/time'
import { useEffect, useState } from 'react'

/**
 * The design's whole vocabulary is three button treatments: a black fill for
 * the primary action, a hairline box for a secondary one, and a greyed box for
 * something unavailable. No radii, no shadows, no colour.
 */
type Variant = 'fill' | 'outline'

const VARIANTS: Record<Variant, string> = {
  fill: 'bg-ink text-surface',
  outline: 'border border-ink text-ink',
}

const DISABLED = 'border border-edge text-faint cursor-not-allowed'

export function Button({
  variant = 'outline',
  className = '',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant }) {
  const look = props.disabled ? DISABLED : VARIANTS[variant]
  return (
    <button
      {...props}
      className={`px-4 py-3 text-center text-[13.5px] transition-opacity ${look} ${
        props.disabled ? '' : 'hover:opacity-80'
      } ${className}`}
    />
  )
}

export function LinkButton({
  to,
  variant = 'outline',
  children,
}: {
  to: string
  variant?: Variant
  children: ReactNode
}) {
  return (
    <Link to={to} className={`inline-block px-5 py-3 text-[13.5px] ${VARIANTS[variant]}`}>
      {children}
    </Link>
  )
}

/** Money always renders in the mono face — it is data, not prose. */
export function Money({ cents, className = '' }: { cents: number; className?: string }) {
  return <span className={`font-mono ${className}`}>{formatCents(cents)}</span>
}

export function Label({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <div className={`font-mono text-[11px] tracking-[0.14em] uppercase text-muted ${className}`}>
      {children}
    </div>
  )
}

export function StatusBadge({ status, muted = false }: { status: string; muted?: boolean }) {
  return (
    <span
      className={`font-mono border px-2 py-0.5 text-[10.5px] tracking-[0.08em] uppercase ${
        muted ? 'border-edge text-faint' : 'border-ink text-ink'
      }`}
    >
      {status}
    </span>
  )
}

/**
 * The 2px stock rule from the landing and detail screens. `available` is
 * computed server-side; this only ever renders it.
 */
export function StockBar({ available, quantity }: { available: number; quantity: number }) {
  const pct = quantity > 0 ? Math.max(0, Math.min(100, (available / quantity) * 100)) : 0
  return (
    <div className="h-0.5 bg-rule">
      <div className="h-0.5 bg-ink" style={{ width: `${pct}%` }} />
    </div>
  )
}

/**
 * The reservation countdown, ticking client-side off the order's own
 * expires_at. Rule 7: never a hardcoded 15:00 — RESERVATION_TTL is server
 * config and can change without a frontend deploy.
 */
export function Countdown({
  expiresAt,
  onExpire,
}: {
  expiresAt: string | null | undefined
  onExpire?: () => void
}) {
  const [now, setNow] = useState(() => Date.now())
  const left = remaining(expiresAt, now)

  useEffect(() => {
    if (!expiresAt) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [expiresAt])

  // Fires once, when the timer crosses zero, so the caller can re-fetch the
  // order and let the server say what actually happened to it.
  useEffect(() => {
    if (left.expired) onExpire?.()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [left.expired])

  return <span className="font-mono">{left.label}</span>
}

export function ErrorState({
  headline,
  body,
  action,
}: {
  headline: string
  body?: string
  action?: ReactNode
}) {
  return (
    <div className="mx-auto max-w-[520px] px-8 py-28 text-center">
      <div className="text-[26px] font-bold tracking-[-0.02em]">{headline}</div>
      {body ? <div className="mt-3 text-[14.5px] leading-relaxed text-muted">{body}</div> : null}
      {action ? <div className="mt-8 flex justify-center gap-3">{action}</div> : null}
    </div>
  )
}

export function Panel({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`border border-edge bg-surface ${className}`}>{children}</div>
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return <div className="px-8 py-24 text-center font-mono text-[11.5px] text-muted">{label}…</div>
}

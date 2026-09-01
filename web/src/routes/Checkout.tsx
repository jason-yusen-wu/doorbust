import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { Elements, PaymentElement, useElements, useStripe } from '@stripe/react-stripe-js'
import { ApiError, cancelOrder, checkout, getOrder } from '../lib/api'
import type { Order } from '../lib/types'
import { errorCopy } from '../lib/errors'
import { isReleasable } from '../lib/status'
import { formatCents } from '../lib/money'
import { useAuth, useUnauthorizedHandler } from '../auth/AuthContext'
import { stripePromise } from '../lib/stripe'
import { Button, Countdown, ErrorState, Label, LinkButton, Money, Panel, Spinner } from '../components/ui'
import { Header } from '../components/Header'

export function Checkout() {
  const { orderId } = useParams()
  const id = Number(orderId)
  const navigate = useNavigate()
  const location = useLocation()
  const { token, me } = useAuth()
  const handleUnauthorized = useUnauthorizedHandler()

  const [order, setOrder] = useState<Order | null>(null)
  const [clientSecret, setClientSecret] = useState<string | null>(null)
  const [error, setError] = useState<ApiError | null>(null)

  // Rule 2: POST /checkout is valid only while the order is `pending` and flips
  // it to `awaiting_payment`. Firing it twice returns 409, and in StrictMode
  // the mount effect runs twice — so the ref, not state, is what guarantees one
  // call.
  const checkoutStarted = useRef(false)

  const productName = (location.state as { productName?: string } | null)?.productName

  const refresh = useCallback(async (): Promise<Order | null> => {
    if (!token) return null
    try {
      const fresh = await getOrder(id, token)
      setOrder(fresh)
      return fresh
    } catch (err) {
      if (handleUnauthorized(err)) return null
      if (err instanceof ApiError) setError(err)
      return null
    }
  }, [id, token, handleUnauthorized])

  useEffect(() => {
    if (!token) {
      navigate(`/signin?next=${encodeURIComponent(`/checkout/${id}`)}`, { replace: true })
      return
    }
    if (!Number.isFinite(id)) return

    let cancelled = false

    void (async () => {
      const current = await refresh()
      if (cancelled || !current) return

      // Anything already resolved belongs on the status screen, which knows how
      // to narrate all six statuses. Only these two are payable.
      if (current.status !== 'pending' && current.status !== 'awaiting_payment') {
        navigate(`/orders/${current.id}`, { replace: true, state: { productName } })
        return
      }

      if (checkoutStarted.current) return
      checkoutStarted.current = true

      try {
        const result = await checkout(id, token)
        if (cancelled) return
        setClientSecret(result.client_secret)
        setOrder(result.order)
      } catch (err) {
        if (cancelled) return
        if (handleUnauthorized(err)) return
        if (err instanceof ApiError) {
          // Someone already ran checkout, or the sweeper expired the hold.
          // Re-read and let the server's status decide where this goes.
          if (err.code === 'order_not_pending') {
            const fresh = await refresh()
            if (fresh) navigate(`/orders/${fresh.id}`, { replace: true, state: { productName } })
            return
          }
          setError(err)
        }
      }
    })()

    return () => {
      cancelled = true
    }
  }, [id, token, navigate, refresh, handleUnauthorized, productName])

  const release = async () => {
    if (!token || !order) return
    try {
      await cancelOrder(order.id, token)
      navigate(`/orders/${order.id}`, { replace: true, state: { productName } })
    } catch (err) {
      if (handleUnauthorized(err)) return
      if (err instanceof ApiError) setError(err)
    }
  }

  if (error) {
    const copy = errorCopy(error.code, error.message)
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <ErrorState
          headline={copy.headline}
          body={copy.body}
          action={<LinkButton to="/">See what's live</LinkButton>}
        />
      </Panel>
    )
  }

  if (!order) {
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <Spinner />
      </Panel>
    )
  }

  return (
    <Panel className="mx-auto max-w-[1120px]">
      {/* The hold banner, pinned above the header exactly as the design has it. */}
      <div className="flex items-center justify-between bg-ink px-8 py-3.5 font-mono text-[13px] text-surface">
        <span>
          Your unit is held — <Countdown expiresAt={order.expires_at} onExpire={refresh} />{' '}
          remaining
        </span>
        <span className="text-faint">Order #{order.id}</span>
      </div>

      <Header />

      <div className="grid grid-cols-[1fr_420px]">
        <div className="border-r border-rule px-8 pt-12 pb-16">
          <h1 className="text-[28px] font-bold tracking-[-0.02em]">Payment</h1>
          <p className="mt-2.5 text-[14.5px] text-muted">
            Stripe handles the card. We never see the number.
          </p>

          <div className="mt-9 max-w-[520px]">
            <Label className="mb-2">Email</Label>
            <div className="border border-edge bg-page px-3.5 py-3 text-[14px] text-muted">
              {me?.email ?? '—'}
            </div>
          </div>

          {clientSecret ? (
            <Elements stripe={stripePromise} options={{ clientSecret, appearance: STRIPE_APPEARANCE }}>
              <PaymentForm order={order} onPaid={() => navigate(`/orders/${order.id}`, { state: { productName } })} />
            </Elements>
          ) : (
            <div className="mt-9 max-w-[520px] font-mono text-[11.5px] text-muted">
              Preparing payment…
            </div>
          )}
        </div>

        <div className="px-8 py-12">
          <Label>Order summary</Label>
          <div className="mt-5 text-[16px] font-medium">
            {productName ?? order.product_name ?? `Sale #${order.product_id}`}
          </div>
          <div className="mt-1.5 font-mono text-[11.5px] text-muted">Sale #{order.product_id}</div>

          <div className="mt-6 flex flex-col gap-3 font-mono text-[13px]">
            <Row label="Subtotal">
              <Money cents={order.total_in_cents} />
            </Row>
            <Row label="Shipping">Calculated by vendor</Row>
            <div className="flex justify-between border-t border-ink pt-3 text-[15px]">
              <span>Total</span>
              <Money cents={order.total_in_cents} />
            </div>
          </div>

          <div className="mt-8 border-t border-rule pt-5 font-mono text-[11.5px] leading-loose text-muted">
            Status <span className="text-ink">{order.status}</span>
            <br />
            {order.expires_at ? (
              <>
                Expires <span className="text-ink">{new Date(order.expires_at).toLocaleTimeString()}</span>
              </>
            ) : null}
          </div>

          {/* Rule 4: DELETE is pending-only. Once checkout has produced a
              client_secret the order is awaiting_payment and this 409s, so it
              is removed rather than left to fail. */}
          {isReleasable(order.status) && !clientSecret ? (
            <button onClick={release} className="mt-6 text-[13px] text-muted underline hover:text-ink">
              Release my unit
            </button>
          ) : null}
        </div>
      </div>
    </Panel>
  )
}

function PaymentForm({ order, onPaid }: { order: Order; onPaid: () => void }) {
  const stripe = useStripe()
  const elements = useElements()
  const [submitting, setSubmitting] = useState(false)
  const [message, setMessage] = useState<string | null>(null)

  const expired = order.expires_at ? Date.parse(order.expires_at) <= Date.now() : false

  const pay = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!stripe || !elements || submitting) return

    setSubmitting(true)
    setMessage(null)

    const { error } = await stripe.confirmPayment({
      elements,
      redirect: 'if_required',
    })

    if (error) {
      setMessage(error.message ?? 'The card was declined.')
      setSubmitting(false)
      return
    }

    // Rule 3: a successful confirm is NOT a completed order. The webhook
    // worker calls CompleteOrder and commits the stock; until GET /orders/:id
    // says "completed", nothing here may claim the unit is paid for. The status
    // screen polls for that.
    onPaid()
  }

  return (
    <form onSubmit={pay} className="mt-6 max-w-[520px]">
      <Label className="mb-2">Card</Label>
      <PaymentElement />

      <Button variant="fill" type="submit" className="mt-9 w-full" disabled={!stripe || submitting || expired}>
        {expired ? 'Hold expired' : submitting ? 'Paying…' : `Pay ${formatCents(order.total_in_cents)}`}
      </Button>

      {message ? (
        <p className="mt-3 border border-ink px-3 py-2 font-mono text-[11.5px]">{message}</p>
      ) : (
        <p className="mt-3.5 font-mono text-[11.5px] leading-loose text-muted">
          If the hold runs out before you pay, the unit is released and the payment is refused.
        </p>
      )}
    </form>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between">
      <span className="text-muted">{label}</span>
      <span>{children}</span>
    </div>
  )
}

/** Stripe's iframe cannot inherit our CSS, so the tokens are restated here. */
const STRIPE_APPEARANCE = {
  theme: 'flat' as const,
  variables: {
    colorPrimary: '#0a0a0a',
    colorBackground: '#ffffff',
    colorText: '#0a0a0a',
    colorTextSecondary: '#6b6b6b',
    borderRadius: '0px',
    fontFamily: '"Helvetica Neue", Helvetica, Arial, sans-serif',
  },
}

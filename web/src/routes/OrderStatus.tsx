import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { ApiError, getOrder, getProduct } from '../lib/api'
import type { Order } from '../lib/types'
import { errorCopy } from '../lib/errors'
import { isPaid, statusCopy } from '../lib/status'
import { useAuth, useUnauthorizedHandler } from '../auth/AuthContext'
import { ErrorState, Label, LinkButton, Money, Panel, Spinner, StatusBadge } from '../components/ui'
import { Header } from '../components/Header'

/** How long to keep waiting on the worker before saying so plainly. */
const SETTLE_TIMEOUT_MS = 60_000
const POLL_MS = 2000

export function OrderStatus() {
  const { id } = useParams()
  const orderId = Number(id)
  const navigate = useNavigate()
  const location = useLocation()
  const { token } = useAuth()
  const handleUnauthorized = useUnauthorizedHandler()

  const [order, setOrder] = useState<Order | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [gaveUp, setGaveUp] = useState(false)
  // The name is carried in route state because GET /orders/{id} does not
  // return product_name; a cold load falls back to GET /products/{id} below.
  const [productName, setProductName] = useState<string | undefined>(
    (location.state as { productName?: string } | null)?.productName,
  )

  const startedAt = useRef(Date.now())

  const load = useCallback(async (): Promise<Order | null> => {
    if (!token) return null
    try {
      const fresh = await getOrder(orderId, token)
      setOrder(fresh)
      setError(null)
      return fresh
    } catch (err) {
      if (handleUnauthorized(err)) return null
      if (err instanceof ApiError) setError(err)
      return null
    }
  }, [orderId, token, handleUnauthorized])

  useEffect(() => {
    if (!token) {
      navigate(`/signin?next=${encodeURIComponent(`/orders/${orderId}`)}`, { replace: true })
      return
    }
    if (!Number.isFinite(orderId)) return

    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined

    // Completion is asynchronous: Stripe's webhook lands in stripe_events and
    // an in-process worker calls CompleteOrder. Polling is how the buyer sees
    // that happen — there is no push channel on this API.
    const tick = async () => {
      const fresh = await load()
      if (cancelled || !fresh) return

      if (!statusCopy(fresh.status).settling) return

      if (Date.now() - startedAt.current > SETTLE_TIMEOUT_MS) {
        setGaveUp(true)
        return
      }
      timer = setTimeout(tick, POLL_MS)
    }

    void tick()
    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [orderId, token, navigate, load])

  // Cold load (a bookmarked link, a refresh): fetch the name we were not given.
  useEffect(() => {
    if (productName || !order) return

    const controller = new AbortController()
    getProduct(order.product_id, controller.signal)
      .then((p) => setProductName(p.name))
      .catch(() => {
        // The name is decoration here; the order's own facts already render.
      })
    return () => controller.abort()
  }, [order, productName])

  if (error) {
    // 403 renders identically to 404 on purpose: distinguishing them would
    // confirm that someone else's order exists.
    const copy = errorCopy(error.code, error.message)
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <ErrorState
          headline={copy.headline}
          body={copy.body}
          action={<LinkButton to="/orders">View all orders</LinkButton>}
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

  const copy = statusCopy(order.status)

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />

      <div className="flex justify-center px-8 pt-20 pb-24">
        <div className="w-[600px]">
          <Label>Order #{order.id}</Label>
          <h1 className="mt-3.5 text-[34px] font-bold tracking-[-0.02em]">
            {gaveUp && copy.settling ? "Still confirming — we'll email you." : copy.headline}
          </h1>
          <p className="mt-3 text-[15px] leading-relaxed text-muted">
            {gaveUp && copy.settling
              ? 'Your payment went through. The confirmation is taking longer than usual; nothing further is needed from you.'
              : copy.body}
          </p>

          <div className="mt-9 border border-edge">
            <Fact label="Item">{productName ?? `Sale #${order.product_id}`}</Fact>
            {/* Only a completed order may be described as paid. Rule 3. */}
            <Fact label={isPaid(order.status) ? 'Paid' : 'Total'}>
              <Money cents={order.total_in_cents} />
            </Fact>
            <Fact label="Status">
              <StatusBadge
                status={order.status}
                muted={['expired', 'cancelled', 'failed'].includes(order.status)}
              />
            </Fact>
            {order.payment_intent_id ? (
              <Fact label="Payment intent" last>
                <span className="text-muted">{order.payment_intent_id}</span>
              </Fact>
            ) : null}
          </div>

          {/* No action while the order is still settling: there is nothing
              useful to click, and offering one invites a double payment. */}
          {!copy.settling ? (
            <div className="mt-8 flex gap-3.5">
              {copy.action ? (
                <LinkButton to={copy.action.to(order)} variant="fill">
                  {copy.action.label}
                </LinkButton>
              ) : null}
              <LinkButton to="/orders">View all orders</LinkButton>
            </div>
          ) : null}
        </div>
      </div>
    </Panel>
  )
}

function Fact({
  label,
  children,
  last = false,
}: {
  label: string
  children: React.ReactNode
  last?: boolean
}) {
  return (
    <div
      className={`flex justify-between px-5 py-4 font-mono text-[13px] ${
        last ? '' : 'border-b border-rule'
      }`}
    >
      <span className="text-muted">{label}</span>
      <span>{children}</span>
    </div>
  )
}

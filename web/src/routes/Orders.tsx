import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, listOrders } from '../lib/api'
import type { Order } from '../lib/types'
import { errorCopy } from '../lib/errors'
import { relativeTime } from '../lib/time'
import { useAuth, useUnauthorizedHandler } from '../auth/AuthContext'
import { Countdown, ErrorState, LinkButton, Money, Panel, Spinner, StatusBadge } from '../components/ui'
import { Header } from '../components/Header'

const PAGE_SIZE = 20
const SETTLED = ['expired', 'cancelled', 'failed']

export function Orders() {
  const { token } = useAuth()
  const navigate = useNavigate()
  const handleUnauthorized = useUnauthorizedHandler()

  const [orders, setOrders] = useState<Order[] | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [limit, setLimit] = useState(PAGE_SIZE)

  const load = useCallback(
    (signal?: AbortSignal) => {
      if (!token) return
      listOrders(token, { limit }, signal)
        .then((rows) => {
          setOrders(rows)
          setError(null)
        })
        .catch((err: unknown) => {
          if (signal?.aborted) return
          if (handleUnauthorized(err)) return
          if (err instanceof ApiError) setError(err)
        })
    },
    [token, limit, handleUnauthorized],
  )

  useEffect(() => {
    if (!token) {
      navigate(`/signin?next=${encodeURIComponent('/orders')}`, { replace: true })
      return
    }
    const controller = new AbortController()
    load(controller.signal)
    return () => controller.abort()
  }, [token, navigate, load])

  if (error) {
    const copy = errorCopy(error.code, error.message)
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <ErrorState headline={copy.headline} body={copy.body} action={<LinkButton to="/">See what's live</LinkButton>} />
      </Panel>
    )
  }

  if (orders === null) {
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <Spinner />
      </Panel>
    )
  }

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />

      <div className="flex items-baseline justify-between px-8 pt-11 pb-5">
        <h1 className="text-[28px] font-bold tracking-[-0.02em]">Your orders</h1>
        <div className="font-mono text-[11.5px] text-muted">Newest first</div>
      </div>

      {/* An empty list is a 200 with [], never a 404. */}
      {orders.length === 0 ? (
        <ErrorState
          headline="No orders yet."
          body="Reserve something from a live sale and it will show up here."
          action={<LinkButton to="/" variant="fill">See what's live</LinkButton>}
        />
      ) : (
        <>
          <div className="grid grid-cols-[110px_1fr_130px_170px_160px] gap-5 border-b border-ink px-8 py-3 font-mono text-[11px] tracking-[0.12em] uppercase text-muted">
            <div>Order</div>
            <div>Item</div>
            <div>Total</div>
            <div>Status</div>
            <div />
          </div>

          {orders.map((order) => (
            <OrderRow key={order.id} order={order} />
          ))}

          <div className="flex justify-between px-8 py-5 font-mono text-[11.5px] text-muted">
            <span>
              {orders.length} {orders.length === 1 ? 'order' : 'orders'}
            </span>
            {orders.length >= limit ? (
              <button onClick={() => setLimit((n) => n + PAGE_SIZE)} className="hover:text-ink">
                Load more →
              </button>
            ) : null}
          </div>
        </>
      )}
    </Panel>
  )
}

function OrderRow({ order }: { order: Order }) {
  const settled = SETTLED.includes(order.status)

  return (
    <div
      className={`grid grid-cols-[110px_1fr_130px_170px_160px] items-center gap-5 border-b border-rule px-8 py-5 text-[14px] ${
        settled ? 'text-faint' : ''
      }`}
    >
      <Link to={`/orders/${order.id}`} className="font-mono text-[13px] hover:text-muted">
        #{order.id}
      </Link>

      <Link
        to={`/orders/${order.id}`}
        state={{ productName: order.product_name }}
        className="hover:text-muted"
      >
        {/* product_name is present on list rows and absent on GET /orders/{id},
            which is why it is passed forward in route state. */}
        {order.product_name ?? `Sale #${order.product_id}`}
      </Link>

      <Money cents={order.total_in_cents} className="text-[13px]" />

      <div className="flex items-center gap-2.5">
        <StatusBadge status={order.status} muted={settled} />
        {order.status === 'pending' ? (
          <span className="font-mono text-[12px] text-muted">
            <Countdown expiresAt={order.expires_at} />
          </span>
        ) : null}
      </div>

      {/* Row action by status: only a pending order has anywhere to go. */}
      {order.status === 'pending' ? (
        <LinkButton to={`/checkout/${order.id}`}>Pay now</LinkButton>
      ) : order.status === 'awaiting_payment' ? (
        <span className="font-mono text-[12px] text-muted">confirming…</span>
      ) : (
        <span className="font-mono text-[12px] text-muted">{relativeTime(order.created_at)}</span>
      )}
    </div>
  )
}

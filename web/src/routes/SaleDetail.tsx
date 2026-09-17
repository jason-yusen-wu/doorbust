import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ApiError, createOrder, getProduct, newIdempotencyKey } from '../lib/api'
import type { Product } from '../lib/types'
import { errorCopy } from '../lib/errors'
import { useAuth, useUnauthorizedHandler } from '../auth/AuthContext'
import { Button, ErrorState, Label, LinkButton, Money, Panel, Spinner, StockBar } from '../components/ui'
import { Header } from '../components/Header'

export function SaleDetail() {
  const { id } = useParams()
  const productId = Number(id)
  const navigate = useNavigate()
  const { token } = useAuth()
  const handleUnauthorized = useUnauthorizedHandler()

  const [product, setProduct] = useState<Product | null>(null)
  const [loadError, setLoadError] = useState<ApiError | null>(null)
  const [reserveError, setReserveError] = useState<ApiError | null>(null)
  const [reserving, setReserving] = useState(false)

  // Rule 1, amended: POST /orders is now idempotent *per key*, so a duplicate
  // submit can no longer reserve a second unit. This ref is therefore no longer
  // the thing standing between a double-click and a double charge — it just
  // saves a pointless round trip. Still a ref, not state: state updates are
  // async and two fast clicks can both observe `reserving === false` before
  // either re-render lands.
  const inFlight = useRef(false)

  // The key identifying one reserve attempt, held across retries of that same
  // attempt so retrying is safe. Generating a fresh key per click would defeat
  // the mechanism entirely, so it is cleared only when the attempt is genuinely
  // over — see the catch below.
  const idempotencyKey = useRef<string | null>(null)

  const load = useCallback(
    (signal?: AbortSignal) =>
      getProduct(productId, signal)
        .then((p) => {
          setProduct(p)
          setLoadError(null)
        })
        .catch((err: unknown) => {
          if (signal?.aborted) return
          if (err instanceof ApiError) setLoadError(err)
        }),
    [productId],
  )

  useEffect(() => {
    if (!Number.isFinite(productId)) return

    const controller = new AbortController()
    load(controller.signal)

    // Poll while the tab is visible. The number that matters is `available`,
    // and on a doorbuster it moves in seconds.
    const id = setInterval(() => {
      if (document.visibilityState === 'visible') load()
    }, 5000)

    return () => {
      controller.abort()
      clearInterval(id)
    }
  }, [productId, load])

  const reserve = async () => {
    if (inFlight.current) return
    if (!token) {
      navigate(`/signin?next=${encodeURIComponent(`/sale/${productId}`)}`)
      return
    }

    inFlight.current = true
    setReserving(true)
    setReserveError(null)

    idempotencyKey.current ??= newIdempotencyKey()

    try {
      const order = await createOrder(productId, token, idempotencyKey.current)
      // Carry the name forward: GET /orders/{id} does not return product_name.
      navigate(`/checkout/${order.id}`, { state: { productName: product?.name } })
    } catch (err) {
      if (handleUnauthorized(err)) return
      if (err instanceof ApiError) {
        setReserveError(err)

        // Whether to keep the key is the whole design.
        //
        // A definitive answer — sold out, not found — ends this attempt, so the
        // next click is a NEW intent and must carry a new key; reusing it would
        // replay a decision the user has already seen. A transient failure
        // (5xx, rate limited, or the first attempt still running) says nothing
        // about whether the reserve happened, and that is exactly the case the
        // key exists for: retrying with it is safe, retrying without it is how
        // a second unit gets taken.
        const attemptIsOver =
          err.code === 'out_of_stock' || err.code === 'not_found' || err.code === 'forbidden'
        if (attemptIsOver) idempotencyKey.current = null

        // 409 out_of_stock means someone else won the row. Re-read so the
        // count on screen matches what the server just told us.
        if (err.code === 'out_of_stock') void load()
      }
    } finally {
      inFlight.current = false
      setReserving(false)
    }
  }

  if (!Number.isFinite(productId) || loadError) {
    const copy = errorCopy(loadError?.code ?? 'not_found', loadError?.message)
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

  if (!product) {
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <Spinner />
      </Panel>
    )
  }

  const soldOut = product.available <= 0
  const live = Date.parse(product.start_at) <= Date.now()

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />

      <div className="border-b border-rule px-8 py-5">
        <Link to="/" className="font-mono text-[11.5px] text-muted hover:text-ink">
          ← All sales
        </Link>
      </div>

      <div className="grid grid-cols-[1fr_380px]">
        <div className="border-r border-rule px-8 pt-14 pb-16">
          <Label>
            {live ? 'Live now' : 'Upcoming'} · #{product.id}
          </Label>
          <h1 className="mt-4 text-[40px] leading-[1.1] font-bold tracking-[-0.025em]">
            {product.name}
          </h1>
          <p className="mt-4 max-w-[520px] text-[15px] leading-relaxed text-muted text-pretty">
            One unit per order. Reserving holds your unit while you pay; if you don't pay in that
            window it goes back on sale automatically.
          </p>

          <div className="mt-11 flex gap-14 font-mono">
            <Stat label="Price">{<Money cents={product.price_in_cents} />}</Stat>
            <Stat label="Remaining">{product.available}</Stat>
            <Stat label="Run size">{product.quantity}</Stat>
          </div>

          <div className="mt-9 max-w-[520px]">
            <StockBar available={product.available} quantity={product.quantity} />
          </div>
        </div>

        <div className="px-8 py-14">
          <div className="border border-ink p-7">
            <div className="flex justify-between font-mono text-[13px]">
              <span>1 × {product.name}</span>
              <Money cents={product.price_in_cents} />
            </div>
            <div className="mt-3 flex justify-between border-t border-rule pt-3 font-mono text-[13px]">
              <span>Total</span>
              <Money cents={product.price_in_cents} />
            </div>

            {/* Quantity is fixed at one: the API reserves exactly one unit per
                order, so there is no quantity control to render. */}
            <Button
              variant="fill"
              className="mt-6 w-full"
              onClick={reserve}
              disabled={reserving || soldOut || !live}
            >
              {reserving ? 'Reserving…' : soldOut ? 'Sold out' : 'Reserve my unit'}
            </Button>

            {reserveError ? (
              <p className="mt-4 border border-ink px-3 py-2 font-mono text-[11.5px] leading-relaxed">
                {errorCopy(reserveError.code, reserveError.message).headline}
              </p>
            ) : (
              <p className="mt-4 font-mono text-[11.5px] leading-loose text-muted">
                Card is charged on the next step.
              </p>
            )}
          </div>
        </div>
      </div>
    </Panel>
  )
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <Label>{label}</Label>
      <div className="mt-2 text-[26px]">{children}</div>
    </div>
  )
}

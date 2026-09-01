import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, listProducts } from '../lib/api'
import type { Product } from '../lib/types'
import { errorCopy } from '../lib/errors'
import { useAuth } from '../auth/AuthContext'
import { Button, ErrorState, Label, Money, Panel, StockBar } from '../components/ui'
import { Header } from '../components/Header'

const PAGE_SIZE = 20
/** No websocket in the API, so the list is polled. */
const POLL_MS = 5000

export function Landing() {
  const [products, setProducts] = useState<Product[] | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [limit, setLimit] = useState(PAGE_SIZE)
  const navigate = useNavigate()
  const { token } = useAuth()

  useEffect(() => {
    let cancelled = false
    // One controller per in-flight request: a slow poll must not still be
    // writing state after the next tick has already replaced it.
    let controller = new AbortController()

    const load = () => {
      controller.abort()
      controller = new AbortController()

      listProducts({ limit }, controller.signal)
        .then((rows) => {
          if (cancelled) return
          setProducts(rows)
          setError(null)
        })
        .catch((err: unknown) => {
          if (cancelled || controller.signal.aborted) return
          // Keep the last good list on screen through a blip; a flash of an
          // error page during a flash sale is worse than slightly stale stock.
          if (err instanceof ApiError) setError(err)
        })
    }

    load()
    const id = setInterval(load, POLL_MS)
    return () => {
      cancelled = true
      controller.abort()
      clearInterval(id)
    }
  }, [limit])

  // start_at is the only thing separating a live sale from an upcoming one;
  // the API does no filtering, so the split is ours.
  const { live, upcoming } = useMemo(() => {
    const now = Date.now()
    const live: Product[] = []
    const upcoming: Product[] = []
    for (const p of products ?? []) {
      ;(Date.parse(p.start_at) <= now ? live : upcoming).push(p)
    }
    return { live, upcoming }
  }, [products])

  const reserve = (product: Product) => {
    // No token means no reserve. Send them to the gate with the destination so
    // they land back on the sale, not the home page.
    if (!token) {
      navigate(`/signin?next=${encodeURIComponent(`/sale/${product.id}`)}`)
      return
    }
    navigate(`/sale/${product.id}`)
  }

  if (products === null && error) {
    const copy = errorCopy(error.code, error.message)
    return (
      <Panel className="mx-auto max-w-[1120px]">
        <Header />
        <ErrorState
          headline={copy.headline}
          body={copy.body}
          action={<Button onClick={() => setLimit((n) => n)}>Retry</Button>}
        />
      </Panel>
    )
  }

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />

      <div className="flex items-end justify-between border-b border-rule px-8 pt-14 pb-10">
        <div className="max-w-[620px]">
          <h1 className="text-[44px] leading-[1.05] font-bold tracking-[-0.025em] text-pretty">
            Limited runs. Sold in minutes.
          </h1>
          <p className="mt-3.5 text-[15px] leading-relaxed text-muted">
            Stock is reserved the moment you claim it and held while you pay.
          </p>
        </div>
        <div className="text-right font-mono text-[11.5px] leading-loose text-muted">
          {live.length} {live.length === 1 ? 'sale' : 'sales'} live
          <br />
          {upcoming.length} upcoming
        </div>
      </div>

      <div className="flex items-baseline justify-between px-8 pt-6 pb-2">
        <Label className="text-ink">Live now</Label>
        <div className="font-mono text-[11px] text-muted">Refreshing every 5s</div>
      </div>

      {products === null ? (
        <div className="px-8 py-16 text-center font-mono text-[11.5px] text-muted">Loading…</div>
      ) : live.length === 0 ? (
        <ErrorState
          headline="Nothing live right now."
          body={
            upcoming.length > 0
              ? 'Check the upcoming drops below.'
              : 'Check back shortly for the next drop.'
          }
        />
      ) : (
        live.map((p) => <LiveRow key={p.id} product={p} onReserve={() => reserve(p)} />)
      )}

      {upcoming.length > 0 && (
        <>
          <Label className="px-8 pt-8 pb-2 text-ink">Upcoming</Label>
          {upcoming.map((p) => (
            <UpcomingRow key={p.id} product={p} />
          ))}
        </>
      )}

      <div className="flex justify-between border-t border-rule px-8 py-5 font-mono text-[11.5px] text-muted">
        <span>
          Showing {products?.length ?? 0} {products?.length === 1 ? 'sale' : 'sales'}
        </span>
        {/* Pagination is limit/offset only and the API returns no total, so
            "more" is inferred from a full page. */}
        {products && products.length >= limit ? (
          <button onClick={() => setLimit((n) => n + PAGE_SIZE)} className="hover:text-ink">
            Load more →
          </button>
        ) : null}
      </div>
    </Panel>
  )
}

const ROW = 'grid grid-cols-[1fr_130px_200px_150px] items-center gap-6 border-b border-rule px-8 py-5'

function LiveRow({ product, onReserve }: { product: Product; onReserve: () => void }) {
  const soldOut = product.available <= 0

  return (
    <div className={`${ROW} ${soldOut ? 'text-faint' : ''}`}>
      <div>
        <Link to={`/sale/${product.id}`} className="text-[17px] font-medium hover:text-muted">
          {product.name}
        </Link>
        <div className="mt-1 font-mono text-[11.5px] text-muted">#{product.id}</div>
      </div>

      <Money cents={product.price_in_cents} className="text-[17px]" />

      <div>
        <div className="mb-1.5 font-mono text-[11.5px] text-muted">
          {product.available} of {product.quantity} left
        </div>
        <StockBar available={product.available} quantity={product.quantity} />
      </div>

      {/* available === 0 disables the button — the reserve would 409 anyway,
          and the guarded UPDATE server-side is the real guarantee. */}
      <Button onClick={onReserve} disabled={soldOut}>
        {soldOut ? 'Sold out' : 'Reserve'}
      </Button>
    </div>
  )
}

function UpcomingRow({ product }: { product: Product }) {
  return (
    <div className={ROW}>
      <div>
        <div className="text-[17px] font-medium">{product.name}</div>
        <div className="mt-1 font-mono text-[11.5px] text-muted">#{product.id}</div>
      </div>
      <Money cents={product.price_in_cents} className="text-[17px]" />
      <div className="font-mono text-[11.5px] text-muted">{product.quantity} units</div>
      <div className="text-center font-mono text-[13px]">
        Opens {new Date(product.start_at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
      </div>
    </div>
  )
}

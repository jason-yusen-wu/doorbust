import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'

/**
 * The 64px header from every screen: wordmark left, Orders and identity right.
 *
 * `is_vendor` comes from GET /me so a client can decide what to render instead
 * of probing for a 403. There is no vendor screen in this build — the design
 * scopes POST /products out — so the flag only labels the account.
 */
export function Header() {
  const { token, me } = useAuth()
  const { pathname } = useLocation()

  return (
    <header className="flex h-16 items-center justify-between border-b border-rule px-8">
      <Link to="/" className="text-[15px] font-bold tracking-[0.18em]">
        DOORBUST
      </Link>

      <div className="flex items-center gap-7 text-[13px]">
        {token ? (
          <>
            <Link
              to="/orders"
              className={
                pathname.startsWith('/orders') ? 'border-b border-ink pb-0.5' : 'text-muted'
              }
            >
              Orders
            </Link>
            <span className="font-mono text-[12px] text-muted">
              {me?.email ?? '…'}
              {me?.is_vendor ? ' · vendor' : ''}
            </span>
          </>
        ) : (
          <>
            <Link to="/orders" className="text-muted">
              Orders
            </Link>
            <Link
              to={`/signin?next=${encodeURIComponent(pathname)}`}
              className="border border-ink px-4 py-2 text-[12.5px]"
            >
              Sign in
            </Link>
          </>
        )}
      </div>
    </header>
  )
}

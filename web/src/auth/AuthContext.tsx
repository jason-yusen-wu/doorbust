import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { ApiError, getMe } from '../lib/api'
import type { Me } from '../lib/types'
import { clearToken, readToken, writeToken } from './session'

interface AuthState {
  token: string | null
  me: Me | null
  /** True until the first /me call for a restored token settles. */
  loading: boolean
  signIn: (token: string) => void
  signOut: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() => readToken())
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState<boolean>(() => readToken() !== null)

  const signOut = useCallback(() => {
    clearToken()
    setToken(null)
    setMe(null)
    setLoading(false)
  }, [])

  const signIn = useCallback((next: string) => {
    writeToken(next)
    setToken(next)
    setLoading(true)
  }, [])

  // GET /me is what provisions the customers row: a Hosted UI signup creates a
  // user in the Cognito pool, not a row in our database, and LinkCustomer is an
  // upsert, so calling it on every session start is both safe and necessary.
  useEffect(() => {
    if (!token) return

    const controller = new AbortController()
    let cancelled = false

    getMe(token, controller.signal)
      .then((profile) => {
        if (!cancelled) {
          setMe(profile)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (cancelled || controller.signal.aborted) return
        // A token the API will not accept is not a session. Dropping it here
        // means a stale tab lands on the sign-in gate rather than failing every
        // subsequent call one at a time.
        if (err instanceof ApiError && err.isUnauthorized) {
          signOut()
          return
        }
        setLoading(false)
      })

    return () => {
      cancelled = true
      controller.abort()
    }
  }, [token, signOut])

  const value = useMemo(
    () => ({ token, me, loading, signIn, signOut }),
    [token, me, loading, signIn, signOut],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside an AuthProvider')
  return ctx
}

/**
 * Handles an ApiError from any authenticated call: a 401 anywhere means the
 * session is over, so drop the token and send the caller to the gate with
 * enough context to come back.
 *
 * Returns true when it handled the error, so callers can `if (handle(err))
 * return` rather than duplicating the check.
 */
export function useUnauthorizedHandler() {
  const { signOut } = useAuth()
  const signOutRef = useRef(signOut)
  signOutRef.current = signOut

  return useCallback((err: unknown): boolean => {
    if (err instanceof ApiError && err.isUnauthorized) {
      signOutRef.current()
      const next = window.location.pathname + window.location.search
      window.location.assign(`/signin?next=${encodeURIComponent(next)}`)
      return true
    }
    return false
  }, [])
}

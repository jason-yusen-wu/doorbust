import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { exchangeCode, takePendingSignIn } from '../auth/pkce'
import { useAuth } from '../auth/AuthContext'
import { ErrorState, LinkButton, Panel, Spinner } from '../components/ui'
import { Header } from '../components/Header'

/**
 * The Hosted UI redirect target. Exchanges the authorization code for an ID
 * token, then hands control to AuthProvider — whose GET /me call is what
 * actually provisions the customer row for a user who has never ordered.
 */
export function Callback() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const { signIn } = useAuth()
  const [error, setError] = useState<string | null>(null)

  // React 18+ mounts effects twice in StrictMode, and takePendingSignIn is
  // single-use — a second run would find no verifier and report a bogus
  // failure. The ref makes the exchange happen exactly once.
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true

    const code = params.get('code')
    const returnedState = params.get('state')

    const denied = params.get('error')
    if (denied) {
      setError(params.get('error_description') || denied)
      return
    }
    if (!code) {
      setError('the sign-in provider returned no authorization code')
      return
    }

    const pending = takePendingSignIn()
    if (!pending) {
      setError('no sign-in was in progress in this tab')
      return
    }
    // The state parameter is the CSRF defence: without this check an attacker
    // could feed us a code they obtained, logging the victim into their account.
    if (returnedState !== pending.state) {
      setError('sign-in state did not match')
      return
    }

    exchangeCode(code, pending.verifier)
      .then((idToken) => {
        signIn(idToken)
        navigate(pending.next, { replace: true })
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : 'token exchange failed')
      })
  }, [params, navigate, signIn])

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />
      {error ? (
        <ErrorState
          headline="Sign-in didn't complete."
          body={error}
          action={<LinkButton to="/signin">Try again</LinkButton>}
        />
      ) : (
        <Spinner label="Signing you in" />
      )}
    </Panel>
  )
}

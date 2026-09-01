import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { beginSignIn } from '../auth/pkce'
import { useAuth } from '../auth/AuthContext'
import { devTokenLoginEnabled, hostedUIConfigured } from '../lib/config'
import { Button, Panel } from '../components/ui'
import { Header } from '../components/Header'

/**
 * The sign-in gate. No credential fields by design: Cognito's Hosted UI owns
 * login and signup, and "Continue" is a redirect to it.
 */
export function SignIn() {
  const [params] = useSearchParams()
  const next = params.get('next') || '/'
  const [error, setError] = useState<string | null>(null)

  const goToHostedUI = async () => {
    try {
      window.location.assign(await beginSignIn(next))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not start sign-in')
    }
  }

  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />

      <div className="flex justify-center px-8 py-28">
        <div className="w-[420px]">
          <h1 className="text-[30px] font-bold tracking-[-0.02em]">Sign in to reserve</h1>
          <p className="mt-3 text-[14.5px] leading-relaxed text-muted">
            You'll be sent to our secure sign-in, then straight back to where you left off. New
            here? The same page creates your account.
          </p>

          {hostedUIConfigured ? (
            <Button variant="fill" className="mt-8 w-full" onClick={goToHostedUI}>
              Continue
            </Button>
          ) : (
            <p className="mt-8 border border-edge px-4 py-3 font-mono text-[11.5px] leading-relaxed text-muted">
              The Cognito Hosted UI is not configured for this build. Set VITE_COGNITO_DOMAIN and
              VITE_COGNITO_CLIENT_ID to enable it.
            </p>
          )}

          {error ? <p className="mt-4 font-mono text-[11.5px] text-ink">{error}</p> : null}

          <p className="mt-4 font-mono text-[11.5px] leading-loose text-muted">
            No stock is held until you claim it.
          </p>

          {devTokenLoginEnabled ? <DevTokenLogin next={next} /> : null}
        </div>
      </div>
    </Panel>
  )
}

/**
 * Development-only: paste an ID token from scripts/get-test-token.sh.
 *
 * `devTokenLoginEnabled` folds to a constant false in a production build, so
 * this component and its markup are dropped by the bundler rather than shipped
 * and merely hidden.
 */
function DevTokenLogin({ next }: { next: string }) {
  const { signIn } = useAuth()
  const navigate = useNavigate()
  const [value, setValue] = useState('')

  return (
    <form
      className="mt-10 border-t border-rule pt-6"
      onSubmit={(e) => {
        e.preventDefault()
        const token = value.trim()
        if (!token) return
        signIn(token)
        navigate(next, { replace: true })
      }}
    >
      <div className="font-mono text-[11px] tracking-[0.14em] uppercase text-muted">
        Dev sign-in
      </div>
      <p className="mt-2 font-mono text-[11px] leading-relaxed text-faint">
        Paste an ID token from scripts/get-test-token.sh. Development builds only.
      </p>
      <textarea
        value={value}
        onChange={(e) => setValue(e.target.value)}
        rows={4}
        spellCheck={false}
        placeholder="eyJraWQiOi…"
        className="mt-3 w-full border border-edge bg-page px-3 py-2 font-mono text-[11px] break-all"
      />
      <Button type="submit" className="mt-3 w-full" disabled={value.trim() === ''}>
        Use this token
      </Button>
    </form>
  )
}

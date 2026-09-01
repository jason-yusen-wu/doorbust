/**
 * Authorization code + PKCE against the Cognito Hosted UI.
 *
 * doorbust is a resource server, not an OAuth2 client: the Hosted UI owns
 * login and signup, and hands us an ID token. PKCE (not a client secret) is
 * what makes that safe from a public SPA — the app client in Cognito must be
 * created *without* a secret, or the token exchange below is rejected.
 */

import { COGNITO_CLIENT_ID, COGNITO_DOMAIN, COGNITO_REDIRECT_URI } from '../lib/config'

const VERIFIER_KEY = 'doorbust.pkce.verifier'
const STATE_KEY = 'doorbust.pkce.state'
const NEXT_KEY = 'doorbust.pkce.next'

/** RFC 7636 code verifier: 43–128 chars of unreserved ASCII. */
export function createVerifier(bytes: Uint8Array = crypto.getRandomValues(new Uint8Array(32))): string {
  return base64url(bytes)
}

export async function challengeFor(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return base64url(new Uint8Array(digest))
}

export function base64url(bytes: Uint8Array): string {
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * Builds the Hosted UI URL and stashes the verifier, the CSRF state and where
 * to land afterwards.
 *
 * sessionStorage rather than localStorage: this is single-tab, single-attempt
 * state, and it should not outlive the tab that started the sign-in.
 */
export async function beginSignIn(next: string): Promise<string> {
  const verifier = createVerifier()
  const state = createVerifier()

  sessionStorage.setItem(VERIFIER_KEY, verifier)
  sessionStorage.setItem(STATE_KEY, state)
  sessionStorage.setItem(NEXT_KEY, next)

  const params = new URLSearchParams({
    response_type: 'code',
    client_id: COGNITO_CLIENT_ID,
    redirect_uri: COGNITO_REDIRECT_URI,
    scope: 'openid email',
    state,
    code_challenge: await challengeFor(verifier),
    code_challenge_method: 'S256',
  })

  return `${normaliseDomain(COGNITO_DOMAIN)}/oauth2/authorize?${params}`
}

export interface PendingSignIn {
  verifier: string
  state: string
  next: string
}

export function takePendingSignIn(): PendingSignIn | null {
  const verifier = sessionStorage.getItem(VERIFIER_KEY)
  const state = sessionStorage.getItem(STATE_KEY)
  if (!verifier || !state) return null

  const next = sessionStorage.getItem(NEXT_KEY) ?? '/'
  // Single-use: clearing here means a replayed callback URL cannot re-run the
  // exchange with the same verifier.
  sessionStorage.removeItem(VERIFIER_KEY)
  sessionStorage.removeItem(STATE_KEY)
  sessionStorage.removeItem(NEXT_KEY)

  return { verifier, state, next }
}

/** Exchanges the authorization code for tokens. Returns the ID token. */
export async function exchangeCode(code: string, verifier: string): Promise<string> {
  const response = await fetch(`${normaliseDomain(COGNITO_DOMAIN)}/oauth2/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: COGNITO_CLIENT_ID,
      redirect_uri: COGNITO_REDIRECT_URI,
      code,
      code_verifier: verifier,
    }),
  })

  if (!response.ok) {
    throw new Error(`token exchange failed (${response.status})`)
  }

  const body = (await response.json()) as { id_token?: string }
  if (!body.id_token) {
    // The API verifies an *ID* token (aud = client id), not an access token.
    // Cognito omits it when "openid" is missing from the requested scopes.
    throw new Error('token response carried no id_token')
  }
  return body.id_token
}

export function signOutUrl(): string {
  const params = new URLSearchParams({
    client_id: COGNITO_CLIENT_ID,
    logout_uri: new URL('/', COGNITO_REDIRECT_URI).toString(),
  })
  return `${normaliseDomain(COGNITO_DOMAIN)}/logout?${params}`
}

/** Accepts a bare domain or a full URL, so .env can hold either. */
function normaliseDomain(domain: string): string {
  const trimmed = domain.replace(/\/+$/, '')
  return /^https?:\/\//.test(trimmed) ? trimmed : `https://${trimmed}`
}

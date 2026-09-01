/**
 * Where the ID token lives.
 *
 * There is no BFF in front of this app, so an httpOnly refresh cookie is not
 * available; sessionStorage is the honest option, and the design's own
 * annotation says so. It is per-tab and dies with it, which is the closest
 * thing to a session boundary we can get client-side.
 */

const TOKEN_KEY = 'doorbust.id_token'

export function readToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_KEY)
  } catch {
    // Private mode and blocked site data both throw on access rather than
    // returning null. An unauthenticated app is better than a blank page.
    return null
  }
}

export function writeToken(token: string): void {
  try {
    sessionStorage.setItem(TOKEN_KEY, token)
  } catch {
    // Storage is a convenience — the token is already held in memory, so the
    // session works for this page load either way.
  }
}

export function clearToken(): void {
  try {
    sessionStorage.removeItem(TOKEN_KEY)
  } catch {
    // ignore
  }
}

/**
 * Build-time configuration.
 *
 * In production every one of these is baked into the bundle by the Dockerfile's
 * VITE_* build args. None of them is a secret: a Cognito domain, a public
 * (no-secret) client id and a Stripe *publishable* key are all values the
 * browser has to hold anyway. The Stripe secret key and the database DSN stay
 * in SSM and never come near this file.
 */

/**
 * Empty in production: the Go server serves this bundle, so the API is on the
 * same origin and every request is a relative path. Set it to
 * http://localhost:8080 for `npm run dev`, where Vite serves on :5173 and the
 * API needs CORS_ALLOWED_ORIGINS set to match.
 */
export const API_BASE_URL: string = import.meta.env.VITE_API_BASE_URL ?? ''

export const COGNITO_DOMAIN: string = import.meta.env.VITE_COGNITO_DOMAIN ?? ''
export const COGNITO_CLIENT_ID: string = import.meta.env.VITE_COGNITO_CLIENT_ID ?? ''
export const COGNITO_REDIRECT_URI: string =
  import.meta.env.VITE_COGNITO_REDIRECT_URI ?? `${window.location.origin}/auth/callback`

export const STRIPE_PUBLISHABLE_KEY: string = import.meta.env.VITE_STRIPE_PUBLISHABLE_KEY ?? ''

/** Whether the Hosted UI is configured enough to redirect to. */
export const hostedUIConfigured = Boolean(COGNITO_DOMAIN && COGNITO_CLIENT_ID)

/**
 * The paste-an-ID-token escape hatch, for running against a live API before the
 * Cognito Hosted UI domain and public app client exist.
 *
 * Guarded by import.meta.env.PROD as well as the flag: the constant folds to
 * `false` in a production build, so the branch and the component behind it are
 * removed by the bundler rather than merely hidden.
 */
export const devTokenLoginEnabled =
  !import.meta.env.PROD && import.meta.env.VITE_ENABLE_DEV_TOKEN_LOGIN === 'true'

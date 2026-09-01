import { loadStripe, type Stripe } from '@stripe/stripe-js'
import { STRIPE_PUBLISHABLE_KEY } from './config'

/**
 * Loaded once, at module scope, as Stripe requires — calling loadStripe inside
 * a component would re-fetch Stripe.js on every render.
 *
 * This is the *publishable* key. The secret key stays in SSM and never reaches
 * a browser or a build arg.
 */
export const stripePromise: Promise<Stripe | null> = STRIPE_PUBLISHABLE_KEY
  ? loadStripe(STRIPE_PUBLISHABLE_KEY)
  : Promise.resolve(null)

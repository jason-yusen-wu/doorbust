/**
 * The error envelope from internal/json/json.go.
 *
 * Every non-2xx on this API is `{"error": {"code", "message"}}`. `code` is the
 * stable half of the contract and is what we branch on; `message` is
 * human-readable and may change freely, so it is shown only for
 * invalid_request — the one case where the server knows something specific
 * about the request that our own copy cannot express.
 */

export const ERROR_CODES = [
  'invalid_request',
  'unauthorized',
  'forbidden',
  'not_found',
  'conflict',
  'out_of_stock',
  'order_not_pending',
  'internal_error',
] as const

export type ErrorCode = (typeof ERROR_CODES)[number]

export interface ErrorCopy {
  headline: string
  body: string
}

const COPY: Record<ErrorCode, ErrorCopy> = {
  out_of_stock: {
    headline: 'Gone. That run sold out.',
    body: "Nothing was reserved and you weren't charged.",
  },
  unauthorized: {
    headline: 'Session ended.',
    body: 'Sign in again — any pending hold is still counting down.',
  },
  // Deliberately identical to not_found. Distinguishing them would confirm
  // that someone else's order exists, which is exactly what the 403 is
  // refusing to reveal.
  forbidden: {
    headline: 'Not found.',
    body: "We couldn't find that. It may have been removed.",
  },
  not_found: {
    headline: 'Not found.',
    body: "We couldn't find that. It may have been removed.",
  },
  order_not_pending: {
    headline: 'This order has already moved on.',
    body: 'Its hold was resolved — check the order for where it stands.',
  },
  conflict: {
    headline: "That didn't go through.",
    body: 'Something changed while you were working. Try again.',
  },
  internal_error: {
    headline: 'Something broke on our side.',
    body: 'Nothing was charged. Retry in a moment.',
  },
  invalid_request: {
    headline: "That request wasn't valid.",
    body: '',
  },
}

export function isErrorCode(value: string): value is ErrorCode {
  return (ERROR_CODES as readonly string[]).includes(value)
}

export function errorCopy(code: string, message = ''): ErrorCopy {
  if (!isErrorCode(code)) {
    return COPY.internal_error
  }

  // The only code whose server message is safe and useful to surface: it
  // describes the caller's own input ("invalid limit", "product_id is
  // required"). Every other code gets our copy, because the server's message
  // is either generic or something we would rather phrase ourselves.
  if (code === 'invalid_request') {
    return { headline: COPY.invalid_request.headline, body: message }
  }

  return COPY[code]
}

/**
 * The order status copy map from artboard 05.
 *
 * These six strings are the API contract — they are triple-coupled to the
 * CHECK constraint in migration 00006 and the constants in
 * internal/orders/status.go, so renaming one is a migration plus a change
 * here. Branch on them, never on a message.
 */

export const ORDER_STATUSES = [
  'pending',
  'awaiting_payment',
  'completed',
  'failed',
  'expired',
  'cancelled',
] as const

export type OrderStatus = (typeof ORDER_STATUSES)[number]

export function isOrderStatus(value: string): value is OrderStatus {
  return (ORDER_STATUSES as readonly string[]).includes(value)
}

export interface StatusCopy {
  headline: string
  body: string
  /** The one action offered, if any. */
  action?: { label: string; to: (order: { id: number; product_id: number }) => string }
  /** Whether the order is still moving. Drives the 2s poll on /orders/:id. */
  settling: boolean
}

const COPY: Record<OrderStatus, StatusCopy> = {
  pending: {
    headline: 'Held, not paid yet.',
    body: 'Your unit is reserved until the hold runs out. Pay to keep it.',
    action: { label: 'Pay now', to: (o) => `/checkout/${o.id}` },
    settling: false,
  },
  // Rule 3: a successful Stripe confirm is NOT completion. The webhook worker
  // calls CompleteOrder, and until it has, this is all we may honestly say.
  awaiting_payment: {
    headline: 'Payment received — confirming your unit…',
    body: 'This usually takes a couple of seconds.',
    settling: true,
  },
  completed: {
    headline: "Paid. It's yours.",
    body: 'The vendor ships within 3 business days.',
    action: { label: 'Back to sales', to: () => '/' },
    settling: false,
  },
  failed: {
    headline: 'Payment declined.',
    body: 'Your unit was released. Reserving again starts a new order.',
    action: { label: 'Try the sale again', to: (o) => `/sale/${o.product_id}` },
    settling: false,
  },
  expired: {
    headline: 'Hold ran out.',
    body: 'The unit went back on sale. Try again if stock remains.',
    action: { label: 'Back to the sale', to: (o) => `/sale/${o.product_id}` },
    settling: false,
  },
  cancelled: {
    headline: 'You released it.',
    body: 'The unit went back on sale. Nothing was charged.',
    action: { label: 'Back to the sale', to: (o) => `/sale/${o.product_id}` },
    settling: false,
  },
}

export function statusCopy(status: string): StatusCopy {
  if (isOrderStatus(status)) return COPY[status]

  // A status the API grew that this build does not know. Saying nothing is
  // safer than guessing: never claim "paid" for an unrecognised value.
  return {
    headline: 'Order updated.',
    body: `This order is currently "${status}".`,
    settling: false,
  }
}

/** Only this may render as paid. Rule 3, in one place. */
export function isPaid(status: string): boolean {
  return status === 'completed'
}

/** Rule 4: DELETE /orders/:id is valid on a pending order and nothing else. */
export function isReleasable(status: string): boolean {
  return status === 'pending'
}

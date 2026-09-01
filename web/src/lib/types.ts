/** Wire shapes, mirroring the Go response structs exactly. */

/** internal/products/handler.go — productResponse. */
export interface Product {
  id: number
  name: string
  price_in_cents: number
  created_at: string
  start_at: string
  quantity: number
  num_reserved: number
  /** Computed server-side as quantity - num_reserved. Never re-derive it. */
  available: number
}

/**
 * internal/orders/handler.go — orderResponse.
 *
 * product_name is present on GET /orders rows and absent on GET /orders/{id};
 * both are `omitempty` in Go, so treat it as optional everywhere.
 */
export interface Order {
  id: number
  product_id: number
  product_name?: string
  status: string
  total_in_cents: number
  created_at: string
  expires_at: string | null
  payment_intent_id?: string
}

/** POST /orders/{id}/checkout — checkoutResponse. */
export interface CheckoutResult {
  order: Order
  client_secret: string
}

/** GET /me — meResponse. */
export interface Me {
  id: number
  email: string
  subject: string
  groups: string[]
  is_vendor: boolean
}

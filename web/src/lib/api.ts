import { API_BASE_URL } from './config'
import type { CheckoutResult, Me, Order, Product } from './types'

/**
 * A non-2xx response from the API, carrying the stable `code` from the error
 * envelope. Callers branch on `code`, never on `message`.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }

  /** Every 401 anywhere in the app means the same thing: sign in again. */
  get isUnauthorized(): boolean {
    return this.status === 401 || this.code === 'unauthorized'
  }

  /**
   * 403 and 404 are rendered identically for orders, so that a rejection
   * cannot be used to discover whether someone else's order exists.
   */
  get isMissing(): boolean {
    return this.status === 404 || this.status === 403
  }
}

export interface RequestOptions {
  token?: string | null
  signal?: AbortSignal
  body?: unknown
  /** Sent as the Idempotency-Key header. See createOrder. */
  idempotencyKey?: string
}

async function request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {}
  if (options.token) {
    headers.Authorization = `Bearer ${options.token}`
  }
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }
  if (options.idempotencyKey) {
    headers['Idempotency-Key'] = options.idempotencyKey
  }

  const response = await fetch(`${API_BASE_URL}${path}`, {
    method,
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  })

  if (!response.ok) {
    throw await toApiError(response)
  }

  // 204 and an empty body are not JSON. No endpoint returns one today, but a
  // parse error here would surface as a confusing failure on a successful call.
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

/**
 * Turns any failure response into an ApiError.
 *
 * A 5xx from something in front of the app — a proxy, a crash before the
 * handler ran — is not JSON at all. Falling back to internal_error keeps every
 * caller on one error type instead of making each one handle a parse failure.
 */
async function toApiError(response: Response): Promise<ApiError> {
  try {
    const body = (await response.json()) as { error?: { code?: string; message?: string } }
    const code = body?.error?.code
    if (typeof code === 'string' && code !== '') {
      return new ApiError(response.status, code, body.error?.message ?? '')
    }
  } catch {
    // fall through
  }
  return new ApiError(response.status, 'internal_error', response.statusText || 'request failed')
}

/* ---- Endpoints. One function per route in cmd/api.go. ---- */

export interface Page {
  limit?: number
  offset?: number
}

function pageQuery({ limit = 20, offset = 0 }: Page): string {
  return `?limit=${limit}&offset=${offset}`
}

export function listProducts(page: Page = {}, signal?: AbortSignal): Promise<Product[]> {
  return request<Product[]>('GET', `/products${pageQuery(page)}`, { signal })
}

export function getProduct(id: number, signal?: AbortSignal): Promise<Product> {
  return request<Product>('GET', `/products/${id}`, { signal })
}

export function getMe(token: string, signal?: AbortSignal): Promise<Me> {
  return request<Me>('GET', '/me', { token, signal })
}

export function listOrders(token: string, page: Page = {}, signal?: AbortSignal): Promise<Order[]> {
  return request<Order[]>('GET', `/orders${pageQuery(page)}`, { token, signal })
}

export function getOrder(id: number, token: string, signal?: AbortSignal): Promise<Order> {
  return request<Order>('GET', `/orders/${id}`, { token, signal })
}

/**
 * Reserve one unit.
 *
 * Rule 1, amended: POST /orders is idempotent *per key*. Without an
 * Idempotency-Key a retry still reserves a second unit — the server keeps the
 * original behaviour for callers that send no key — so callers must pass one
 * and must reuse the SAME key for what the user experienced as one attempt.
 *
 * Generating a fresh key per call would defeat the entire mechanism, which is
 * why the key is an explicit parameter rather than something this function
 * invents: only the caller knows where one user intent begins and ends.
 */
export function createOrder(
  productId: number,
  token: string,
  idempotencyKey: string,
): Promise<Order> {
  return request<Order>('POST', '/orders', {
    token,
    body: { product_id: productId },
    idempotencyKey,
  })
}

/**
 * A key identifying one reserve attempt.
 *
 * crypto.randomUUID is available in every browser the app targets and in
 * jsdom; the fallback keeps the function total rather than throwing in an
 * exotic environment, since a weaker key is still vastly better than none.
 */
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`
}

/**
 * Rule 2: valid only while the order is `pending`, and it flips the order to
 * `awaiting_payment`. Fire once and keep the client_secret; calling it again
 * after the flip returns 409 order_not_pending.
 */
export function checkout(id: number, token: string): Promise<CheckoutResult> {
  return request<CheckoutResult>('POST', `/orders/${id}/checkout`, { token })
}

/** Rule 4: pending only. 409 order_not_pending once checkout has run. */
export function cancelOrder(id: number, token: string): Promise<Order> {
  return request<Order>('DELETE', `/orders/${id}`, { token })
}

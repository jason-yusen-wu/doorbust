import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, cancelOrder, checkout, createOrder, listOrders, listProducts } from './api'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('error envelope', () => {
  it('surfaces the code, which is what callers branch on', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(409, { error: { code: 'out_of_stock', message: 'product is out of stock' } }),
    )

    const err = await createOrder(1041, 'tok').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('out_of_stock')
    expect((err as ApiError).status).toBe(409)
  })

  it('distinguishes order_not_pending from out_of_stock', async () => {
    // Both are 409. Branching on the status alone would conflate "sold out"
    // with "you already checked this order out".
    fetchMock.mockResolvedValue(
      jsonResponse(409, { error: { code: 'order_not_pending', message: 'order is not pending' } }),
    )
    const err = (await checkout(4192, 'tok').catch((e: unknown) => e)) as ApiError
    expect(err.code).toBe('order_not_pending')
  })

  it('falls back to internal_error when the body is not the envelope', async () => {
    // A 502 from something in front of the app is HTML, not JSON. Every caller
    // must still receive an ApiError rather than a parse failure.
    fetchMock.mockResolvedValue(new Response('<html>502 Bad Gateway</html>', { status: 502 }))

    const err = (await listProducts().catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe('internal_error')
    expect(err.status).toBe(502)
  })

  it('treats any 401 as the end of the session', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(401, { error: { code: 'unauthorized', message: 'invalid bearer token' } }),
    )
    const err = (await listOrders('tok').catch((e: unknown) => e)) as ApiError
    expect(err.isUnauthorized).toBe(true)
  })

  it('treats 403 and 404 alike, so a rejection reveals nothing', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(403, { error: { code: 'forbidden', message: 'not your order' } }),
    )
    const forbidden = (await cancelOrder(1, 'tok').catch((e: unknown) => e)) as ApiError

    fetchMock.mockResolvedValue(
      jsonResponse(404, { error: { code: 'not_found', message: 'order not found' } }),
    )
    const missing = (await cancelOrder(1, 'tok').catch((e: unknown) => e)) as ApiError

    expect(forbidden.isMissing).toBe(true)
    expect(missing.isMissing).toBe(true)
  })
})

describe('requests', () => {
  it('sends the bearer token on authenticated calls and omits it otherwise', async () => {
    // A fresh Response per call: a body can only be read once, so a shared
    // instance fails the second time it is returned.
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, [])))

    await listOrders('tok-123')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      headers: { Authorization: 'Bearer tok-123' },
    })

    fetchMock.mockClear()
    await listProducts()
    expect(fetchMock.mock.calls[0]?.[1]?.headers).not.toHaveProperty('Authorization')
  })

  it('uses the method each route is actually mounted with', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, {})))

    await cancelOrder(4192, 'tok')
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/orders/4192')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: 'DELETE' })

    fetchMock.mockClear()
    await checkout(4192, 'tok')
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/orders/4192/checkout')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: 'POST' })
  })

  it('sends product_id in the body, never the caller identity', async () => {
    // The order's owner comes from the verified token server-side. A
    // client-supplied email would be ignored at best; DisallowUnknownFields
    // makes it a 400.
    fetchMock.mockResolvedValue(jsonResponse(201, {}))

    await createOrder(1041, 'tok')
    expect(fetchMock.mock.calls[0]?.[1]?.body).toBe(JSON.stringify({ product_id: 1041 }))
  })

  it('caps pagination the way the handlers expect', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, []))

    await listProducts({ limit: 50, offset: 20 })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/products?limit=50&offset=20')
  })
})

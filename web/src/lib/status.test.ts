import { describe, expect, it } from 'vitest'
import { ORDER_STATUSES, isPaid, isReleasable, statusCopy } from './status'

describe('statusCopy', () => {
  // These six are the CHECK constraint in migration 00006 and the constants in
  // internal/orders/status.go. A status with no copy would render blank.
  it.each(ORDER_STATUSES)('has copy for %s', (status) => {
    const copy = statusCopy(status)
    expect(copy.headline).not.toBe('')
    expect(copy.body).not.toBe('')
  })

  it('only marks awaiting_payment as still settling', () => {
    // `settling` is what drives the 2s poll on /orders/:id. Marking a terminal
    // status as settling would poll forever; missing awaiting_payment would
    // leave the buyer on "confirming…" with no update.
    const settling = ORDER_STATUSES.filter((s) => statusCopy(s).settling)
    expect(settling).toEqual(['awaiting_payment'])
  })

  it('offers no action while an order is settling', () => {
    // There is nothing useful to click mid-confirmation, and offering an
    // action here invites a second payment attempt.
    expect(statusCopy('awaiting_payment').action).toBeUndefined()
  })

  it('routes each terminal status somewhere useful', () => {
    const order = { id: 4192, product_id: 1041 }
    expect(statusCopy('pending').action?.to(order)).toBe('/checkout/4192')
    expect(statusCopy('completed').action?.to(order)).toBe('/')
    expect(statusCopy('failed').action?.to(order)).toBe('/sale/1041')
    expect(statusCopy('expired').action?.to(order)).toBe('/sale/1041')
    expect(statusCopy('cancelled').action?.to(order)).toBe('/sale/1041')
  })

  it('does not invent copy for an unknown status', () => {
    // If the API grows a seventh status, this build must not guess — and above
    // all must not imply the order is paid.
    const copy = statusCopy('refunded')
    expect(copy.headline).not.toMatch(/paid/i)
    expect(copy.settling).toBe(false)
  })
})

describe('isPaid', () => {
  // Rule 3: only `completed` from GET /orders/:id may say paid. A successful
  // Stripe confirm leaves the order at awaiting_payment until the worker runs.
  it('is true only for completed', () => {
    expect(ORDER_STATUSES.filter(isPaid)).toEqual(['completed'])
  })
})

describe('isReleasable', () => {
  // Rule 4: DELETE /orders/:id works on pending only; anything else 409s.
  it('is true only for pending', () => {
    expect(ORDER_STATUSES.filter(isReleasable)).toEqual(['pending'])
  })
})

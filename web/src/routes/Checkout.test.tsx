import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from '../auth/AuthContext'
import { Checkout } from './Checkout'

/**
 * Rule 2 of the handoff, which is the easiest rule here to break by accident:
 * POST /orders/{id}/checkout is valid only while the order is `pending` and
 * flips it to `awaiting_payment`. A second call returns 409, and React
 * StrictMode mounts every effect twice in development — so a guard that lives
 * in state rather than a ref lets a duplicate through.
 */

// The Payment Element needs Stripe.js, which cannot load in jsdom. The guard
// under test runs before any of that.
vi.mock('@stripe/react-stripe-js', () => ({
  Elements: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  PaymentElement: () => <div data-testid="payment-element" />,
  useStripe: () => null,
  useElements: () => null,
}))
vi.mock('../lib/stripe', () => ({ stripePromise: Promise.resolve(null) }))

const PENDING_ORDER = {
  id: 4192,
  product_id: 1041,
  status: 'pending',
  total_in_cents: 2400,
  created_at: '2026-09-01T12:00:00Z',
  expires_at: '2026-09-01T12:15:00Z',
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  sessionStorage.setItem('doorbust.id_token', 'test-token')

  fetchMock = vi.fn((input: string) => {
    if (input.startsWith('/me')) {
      return Promise.resolve(
        jsonResponse(200, {
          id: 1,
          email: 'dana@studio.co',
          subject: 'sub-1',
          groups: [],
          is_vendor: false,
        }),
      )
    }
    if (input.includes('/checkout')) {
      return Promise.resolve(jsonResponse(200, { order: { ...PENDING_ORDER, status: 'awaiting_payment' }, client_secret: 'pi_1_secret_x' }))
    }
    if (input.startsWith('/orders/')) {
      return Promise.resolve(jsonResponse(200, PENDING_ORDER))
    }
    return Promise.resolve(jsonResponse(404, { error: { code: 'not_found', message: '' } }))
  })

  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
})

function renderCheckout() {
  return render(
    <StrictMode>
      <MemoryRouter initialEntries={['/checkout/4192']}>
        <AuthProvider>
          <Routes>
            <Route path="/checkout/:orderId" element={<Checkout />} />
            <Route path="/orders/:id" element={<div>order status</div>} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>
    </StrictMode>,
  )
}

function checkoutCalls(): number {
  return fetchMock.mock.calls.filter(([url]) => String(url).includes('/checkout')).length
}

describe('Checkout', () => {
  it('fires POST /checkout exactly once, even under StrictMode double-mount', async () => {
    renderCheckout()

    await waitFor(() => expect(screen.getByTestId('payment-element')).toBeInTheDocument())
    expect(checkoutCalls()).toBe(1)
  })

  it('does not re-fire POST /checkout when the component re-renders', async () => {
    const { rerender } = renderCheckout()
    await waitFor(() => expect(checkoutCalls()).toBe(1))

    rerender(
      <StrictMode>
        <MemoryRouter initialEntries={['/checkout/4192']}>
          <AuthProvider>
            <Routes>
              <Route path="/checkout/:orderId" element={<Checkout />} />
            </Routes>
          </AuthProvider>
        </MemoryRouter>
      </StrictMode>,
    )

    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(checkoutCalls()).toBe(1)
  })

  it('hides "Release my unit" once a client_secret exists', async () => {
    // Rule 4: DELETE is pending-only, and checkout has already moved the order
    // to awaiting_payment — leaving the control visible would offer a 409.
    renderCheckout()

    await waitFor(() => expect(screen.getByTestId('payment-element')).toBeInTheDocument())
    expect(screen.queryByText('Release my unit')).not.toBeInTheDocument()
  })

  it('sends an already-resolved order to the status screen without calling checkout', async () => {
    fetchMock.mockImplementation((input: string) => {
      if (input.startsWith('/me')) {
        return Promise.resolve(
          jsonResponse(200, { id: 1, email: 'd@e.co', subject: 's', groups: [], is_vendor: false }),
        )
      }
      if (input.startsWith('/orders/')) {
        return Promise.resolve(jsonResponse(200, { ...PENDING_ORDER, status: 'completed' }))
      }
      return Promise.resolve(jsonResponse(404, { error: { code: 'not_found', message: '' } }))
    })

    renderCheckout()

    await waitFor(() => expect(screen.getByText('order status')).toBeInTheDocument())
    // Calling checkout on a completed order would 409; it must not be attempted.
    expect(checkoutCalls()).toBe(0)
  })
})

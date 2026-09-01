import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { AuthProvider } from './auth/AuthContext'
import { Landing } from './routes/Landing'
import { SignIn } from './routes/SignIn'
import { Callback } from './routes/Callback'
import { SaleDetail } from './routes/SaleDetail'
import { Checkout } from './routes/Checkout'
import { OrderStatus } from './routes/OrderStatus'
import { Orders } from './routes/Orders'
import { ErrorState, LinkButton, Panel } from './components/ui'
import { Header } from './components/Header'

/**
 * Routes map one-to-one to the screens on the handoff canvas. POST /products
 * is deliberately absent: creating a sale is vendor-only and the design scopes
 * it out. GET /me still reports is_vendor, so the nav hook exists.
 */
export function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <div className="px-6 py-10">
          <Routes>
            <Route path="/" element={<Landing />} />
            <Route path="/signin" element={<SignIn />} />
            <Route path="/auth/callback" element={<Callback />} />
            <Route path="/sale/:id" element={<SaleDetail />} />
            <Route path="/checkout/:orderId" element={<Checkout />} />
            <Route path="/orders" element={<Orders />} />
            <Route path="/orders/:id" element={<OrderStatus />} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </div>
      </AuthProvider>
    </BrowserRouter>
  )
}

function NotFound() {
  return (
    <Panel className="mx-auto max-w-[1120px]">
      <Header />
      <ErrorState
        headline="Not found."
        body="We couldn't find that page."
        action={<LinkButton to="/" variant="fill">See what's live</LinkButton>}
      />
    </Panel>
  )
}

import { describe, expect, it } from 'vitest'
import { ERROR_CODES, errorCopy } from './errors'

describe('errorCopy', () => {
  // These eight are the constants in internal/json/json.go. A code with no
  // copy would render an empty error page.
  it.each(ERROR_CODES)('has copy for %s', (code) => {
    expect(errorCopy(code, 'server message').headline).not.toBe('')
  })

  it('shows the server message only for invalid_request', () => {
    // invalid_request is the one code whose message describes the caller's own
    // input ("invalid limit", "product_id is required") and is safe to surface.
    expect(errorCopy('invalid_request', 'invalid limit').body).toBe('invalid limit')

    for (const code of ERROR_CODES.filter((c) => c !== 'invalid_request')) {
      expect(errorCopy(code, 'failed to connect to user=admin database=doorbust').body).not.toContain(
        'database=doorbust',
      )
    }
  })

  it('renders forbidden identically to not_found', () => {
    // Distinguishing them would confirm that someone else's order exists,
    // which is exactly what the 403 refuses to reveal.
    expect(errorCopy('forbidden')).toEqual(errorCopy('not_found'))
  })

  it('falls back to internal_error for an unknown code', () => {
    expect(errorCopy('teapot')).toEqual(errorCopy('internal_error'))
  })

  it('never claims a charge was made when none was', () => {
    expect(errorCopy('out_of_stock').body).toMatch(/weren't charged/i)
    expect(errorCopy('internal_error').body).toMatch(/nothing was charged/i)
  })
})

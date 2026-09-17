import { describe, expect, it } from 'vitest'
import { callbackOriginUsable } from './config'

/**
 * These cases encode a rule enforced by Cognito, not by us: it refuses to
 * register a callback URL that is not HTTPS, exempting only the loopback hosts.
 * Both `localhost` and `127.0.0.1` were confirmed accepted against the real
 * pool, and a bare-IP HTTP callback confirmed rejected with
 * "cannot use the HTTP protocol".
 *
 * Getting this wrong is not cosmetic: an origin that returns true here renders
 * a "Continue" button that can only ever land the buyer on a Cognito error page
 * reading `error=redirect_mismatch`.
 */
describe('callbackOriginUsable', () => {
  it.each([
    ['https:', 'doorbust.example.com'],
    ['https:', 'localhost'],
    ['http:', 'localhost'],
    ['http:', '127.0.0.1'],
  ])('allows %s//%s', (protocol, hostname) => {
    expect(callbackOriginUsable({ protocol, hostname })).toBe(true)
  })

  it.each([
    // The deployed box: plain HTTP on a bare IP. This is the case that shipped
    // a permanently broken sign-in button.
    ['http:', '18.191.71.225'],
    ['http:', 'doorbust.example.com'],
    ['http:', 'sub.domain.example'],
    // Not a loopback host despite the prefix — a lookalike domain must not
    // slip through a naive substring check.
    ['http:', 'localhost.evil.example'],
    ['http:', '127.0.0.1.evil.example'],
  ])('refuses %s//%s', (protocol, hostname) => {
    expect(callbackOriginUsable({ protocol, hostname })).toBe(false)
  })
})

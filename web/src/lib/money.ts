/**
 * Money is integer cents everywhere in the API (`price_in_cents`,
 * `total_in_cents`). Rule 5 of the handoff: format at the edge, never store as
 * a float. Nothing in this app should divide by 100 outside this file.
 */

const formatter = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
})

export function formatCents(cents: number): string {
  // Intl would happily render 12.5 as "$0.13" after the divide. A non-integer
  // here means a float crept into the money path upstream, which is worth
  // failing loudly on rather than rounding away.
  if (!Number.isInteger(cents)) {
    throw new TypeError(`money must be integer cents, got ${cents}`)
  }
  return formatter.format(cents / 100)
}

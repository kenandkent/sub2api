import { describe, expect, it } from 'vitest'
import { compareMoney, validPayoutAmount } from '../money'
describe('exact payout amounts', () => {
  it('compares arbitrarily large decimals without floating point', () => {
    expect(compareMoney('9007199254740993.01', '9007199254740993.00')).toBe(1)
    expect(compareMoney('1.0', '1.00')).toBe(0)
    expect(compareMoney('0.09', '0.1')).toBe(-1)
  })
  it('requires a positive amount with at most two decimal places and the published minimum', () => {
    expect(validPayoutAmount('10.00', '10.00')).toBe(true)
    expect(validPayoutAmount('9.99', '10.00')).toBe(false)
    expect(validPayoutAmount('10.001', '10.00')).toBe(false)
    expect(validPayoutAmount('0', '0')).toBe(false)
    expect(validPayoutAmount('10', '')).toBe(false)
  })
})

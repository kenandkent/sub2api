import { describe, expect, it } from 'vitest'
import { canApprove, payoutActions, validPrice, sameMoney, createOperation } from '../adminState'

describe('marketplace admin safeguards', () => {
  it('requires every price while permitting explicit zero and eight decimal places', () => {
    expect(validPrice('')).toBe(false)
    expect(validPrice('0')).toBe(true)
    expect(validPrice('0.00000001')).toBe(true)
    expect(validPrice('0.000000001')).toBe(false)
  })
  it('prevents self approval and offers no release or start for unknown processing', () => {
    expect(canApprove(12, 12)).toBe(false)
    expect(canApprove(12, 13)).toBe(true)
    expect(payoutActions('processing')).toEqual(['confirm', 'fail'])
  })
  it('compares payment amounts without floating point conversion', () => {
    expect(sameMoney('12345678901234567890.10', '12345678901234567890.1')).toBe(true)
    expect(sameMoney('1.01', '1.10')).toBe(false)
  })
  it('keeps a key across retries and replaces it only after completion', () => {
    const op = createOperation(() => 'key-' + ++counter)
    let counter = 0
    expect(op.key()).toBe('key-1')
    expect(op.key()).toBe('key-1')
    op.complete()
    expect(op.key()).toBe('key-2')
  })
})

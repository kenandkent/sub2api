import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/adminMarketplace'
import zh from '@/i18n/locales/zh/adminMarketplace'

describe('admin marketplace translations', () => {
  it('provides matching English and Chinese keys', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
    expect(Object.values(en).every(Boolean)).toBe(true)
    expect(Object.values(zh).every(Boolean)).toBe(true)
  })
})

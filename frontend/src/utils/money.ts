/** Compare non-negative decimal strings without converting them to floating point. */
export function compareMoney(a: string, b: string): -1 | 0 | 1 {
  const parse = (s: string) => {
    if (!/^\d+(?:\.\d+)?$/.test(s)) throw new Error('Invalid monetary amount')
    const [integer, fraction = ''] = s.split('.')
    return [integer.replace(/^0+(?=\d)/, ''), fraction] as const
  }
  const [ai, af] = parse(a); const [bi, bf] = parse(b)
  if (ai.length !== bi.length) return ai.length > bi.length ? 1 : -1
  if (ai !== bi) return ai > bi ? 1 : -1
  const width = Math.max(af.length, bf.length)
  const av = af.padEnd(width, '0'); const bv = bf.padEnd(width, '0')
  return av === bv ? 0 : av > bv ? 1 : -1
}
export function validPayoutAmount(amount: string, minimum: string): boolean {
  if (!/^\d+(?:\.\d{1,2})?$/.test(amount) || !/^\d+(?:\.\d+)?$/.test(minimum)) return false
  return compareMoney(amount, '0') > 0 && compareMoney(amount, minimum) >= 0
}

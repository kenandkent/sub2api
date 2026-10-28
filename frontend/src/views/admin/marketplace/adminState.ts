export function validPrice(value: string): boolean {
  return /^(?:0|[1-9]\d*)(?:\.\d{1,8})?$/.test(value)
}
export function canApprove(adminId: number | undefined, supplierId: number): boolean {
  return adminId !== undefined && adminId !== supplierId
}
export function payoutActions(status: string): string[] {
  if (status === 'requested') return ['approve', 'reject']
  if (status === 'approved') return ['start', 'reject']
  if (status === 'processing') return ['confirm', 'fail']
  return []
}
export function sameMoney(a: string, b: string): boolean {
  if (!validPrice(a) || !validPrice(b)) return false
  const normalize = (s: string) => { const [whole, fraction = ''] = s.split('.'); return `${whole}.${fraction.padEnd(8, '0')}` }
  return normalize(a) === normalize(b)
}
export function createOperation(makeKey: () => string = () => crypto.randomUUID()) {
  let current: string | undefined
  return { key: () => current ??= makeKey(), complete: () => { current = undefined } }
}

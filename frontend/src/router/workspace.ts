import type { UserCapabilities } from '@/types/marketplace'
import type { Router } from 'vue-router'

export const DEFAULT_WORKSPACE_PATH = '/workspace'

export function supplierRedirect(path: string, capabilities: UserCapabilities | null): string | null {
  if (path === '/supplier/onboarding') return null
  if (capabilities?.allowed_workspaces.includes('supplier') &&
      (capabilities.supplier_status === 'active' || capabilities.supplier_status === 'suspended')) return null
  return '/supplier/onboarding'
}

export function isAdminContext(isAdmin: boolean, path: string): boolean {
  return isAdmin && (path === '/admin' || path.startsWith('/admin/'))
}

export function validWorkspaceTarget(router: Pick<Router, 'resolve'>, target: string, fallback: string): string {
  const resolved = router.resolve(target)
  return resolved.matched.length && resolved.name !== 'NotFound' ? target : fallback
}

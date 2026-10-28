export type WorkMode = 'consumer' | 'supplier'
export type SupplierStatus = 'not_applied' | 'pending' | 'active' | 'rejected' | 'suspended'
export interface UserCapabilities {
  consumer: boolean
  supplier_status: SupplierStatus
  marketplace_enabled: boolean
  allowed_workspaces: WorkMode[]
}

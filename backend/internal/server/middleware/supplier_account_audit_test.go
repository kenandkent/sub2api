//go:build unit

package middleware

import "testing"

func TestSupplierAccountAuditOmitsCredentialBodies(t *testing.T) {
	if _,ok:=auditBodyOmittedRoutes["POST /api/v1/supplier/authorizations/:id/complete"];!ok {t.Fatal("OAuth callback credentials must be omitted")}
	for _, route := range []string{"POST /api/v1/supplier/accounts", "POST /api/v1/supplier/accounts/:id/credentials"} {
		if _, ok := auditBodyOmittedRoutes[route]; !ok {
			t.Fatalf("credential body is not omitted for %s", route)
		}
	}
}

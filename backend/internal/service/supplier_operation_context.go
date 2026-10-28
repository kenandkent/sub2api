package service

import "context"

type supplierAuthorizationOperationKey struct{}

// WithSupplierAuthorizationOperation binds a server-generated authorization operation to the exchange.
func WithSupplierAuthorizationOperation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, supplierAuthorizationOperationKey{}, id)
}

func SupplierAuthorizationOperationID(ctx context.Context) string {
	v, _ := ctx.Value(supplierAuthorizationOperationKey{}).(string)
	return v
}

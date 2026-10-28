package service

import (
    "context"
    "testing"
)

type payoutOperationTestStore struct { SupplierPayoutStore; owner int64; kind, key string }
func (s *payoutOperationTestStore) LookupOperation(_ context.Context, owner int64, kind, key string) (*PayoutOperationStatus, error) {
    s.owner, s.kind, s.key = owner, kind, key
    return &PayoutOperationStatus{OperationStatus: "not_found"}, nil
}
func TestPayoutOperationIsScopedToOwnerAndKind(t *testing.T) {
    store := &payoutOperationTestStore{}
    svc := NewSupplierPayoutService(store, nil, nil)
    result, err := svc.Operation(context.Background(), 12, "request", "key")
    if err != nil || result.OperationStatus != "not_found" || store.owner != 12 || store.kind != "request" || store.key != "key" { t.Fatalf("unexpected lookup: %#v %v %#v", result, err, store) }
    if _, err = svc.Operation(context.Background(), 12, "approve", "key"); err == nil { t.Fatal("unsupported kind accepted") }
}

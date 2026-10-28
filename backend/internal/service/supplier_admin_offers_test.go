package service

import (
	"context"
	"testing"
)

type offerListTestStore struct {
	SupplierAccountStore
	filter SupplierAccountFilter
}

func (s *offerListTestStore) ListAdminOffers(_ context.Context, f SupplierAccountFilter) ([]AdminSupplierOffer, int64, error) {
	s.filter = f
	return []AdminSupplierOffer{{ID: 9, AccountID: 4}}, 1, nil
}
func TestAdminOfferListUsesOfferIdentityAndNormalizesPagination(t *testing.T) {
	store := &offerListTestStore{}
	svc := &SupplierAccountService{store: store}
	page, err := svc.AdminListOffers(context.Background(), 7, SupplierAccountFilter{})
	if err != nil || page.Items[0].ID != 9 || page.Items[0].AccountID != 4 || store.filter.Page != 1 || store.filter.PageSize != 20 {
		t.Fatalf("unexpected result: %#v %v", page, err)
	}
}

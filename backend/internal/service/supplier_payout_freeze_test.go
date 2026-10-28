package service

import (
	"context"
	"testing"
)

type freezeTestStore struct {
	SupplierPayoutStore
	called bool
}

func (s *freezeTestStore) SetPayoutFrozen(_ context.Context, cmd PayoutFreezeCommand) (*PayoutFreezeResult, error) {
	s.called = true
	return &PayoutFreezeResult{SupplierID: cmd.SupplierID, Frozen: cmd.Frozen, Version: cmd.ExpectedVersion + 1}, nil
}
func TestPayoutFreezeRequiresReasonVersionAndOperation(t *testing.T) {
	s := &freezeTestStore{}
	svc := NewSupplierPayoutService(s, nil, nil)
	cmd := PayoutFreezeCommand{SupplierID: 1, ActorID: 2, ExpectedVersion: 3, Frozen: true, Reason: "risk", OperationID: "op"}
	if _, err := svc.SetFrozen(context.Background(), cmd); err != nil || !s.called {
		t.Fatalf("valid freeze rejected: %v", err)
	}
	cmd.Reason = " "
	s.called = false
	if _, err := svc.SetFrozen(context.Background(), cmd); err == nil || s.called {
		t.Fatal("empty reason accepted")
	}
}

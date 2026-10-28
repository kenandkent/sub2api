package handler

import (
	"bytes"
	"context"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

type handlerFreezeStore struct {
	service.SupplierPayoutStore
	command service.PayoutFreezeCommand
}

func (s *handlerFreezeStore) SetPayoutFrozen(_ context.Context, c service.PayoutFreezeCommand) (*service.PayoutFreezeResult, error) {
	s.command = c
	return &service.PayoutFreezeResult{SupplierID: c.SupplierID, Frozen: c.Frozen, Version: c.ExpectedVersion + 1}, nil
}
func TestPayoutFreezeHandlerRequiresStepUpAndPassesCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, grant := range []bool{false, true} {
		store := &handlerFreezeStore{}
		users := payoutTestUsers{}
		h := NewSupplierPayoutHandler(service.NewSupplierPayoutService(store, nil, nil), service.NewTotpService(users, nil, payoutTestGrant{granted: grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
			c.Set("auth_method", "jwt")
			c.Next()
		})
		r.POST("/suppliers/:id/payout-freeze", h.Freeze)
		q := httptest.NewRequest(http.MethodPost, "/suppliers/8/payout-freeze", bytes.NewBufferString(`{"expected_version":3,"frozen":true,"reason":"risk"}`))
		q.Header.Set("Content-Type", "application/json")
		q.Header.Set("Idempotency-Key", "freeze-op")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if grant {
			require.Equal(t, 200, w.Code)
			require.Equal(t, service.PayoutFreezeCommand{SupplierID: 8, ActorID: 7, ExpectedVersion: 3, Frozen: true, Reason: "risk", OperationID: "freeze-op"}, store.command)
		} else {
			require.Equal(t, 403, w.Code)
			require.Zero(t, store.command.SupplierID)
		}
	}
}

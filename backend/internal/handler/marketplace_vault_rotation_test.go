package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type vaultHandlerStore struct {
	calls   int
	failure error
}

func (s *vaultHandlerStore) Rotate(_ context.Context, actor int64, operation, reason string, limit int) (*service.VaultRotationResult, error) {
	s.calls++
	if s.failure != nil {
		return nil, s.failure
	}
	return &service.VaultRotationResult{OperationID: operation, Processed: 1, Remaining: 0, Completed: true, TargetKeyID: "current"}, nil
}

func TestVaultRotationHandlerRequiresJWTAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		role, method string
		grant        bool
		status       int
		calls        int
	}{
		{"user", "jwt", true, http.StatusForbidden, 0},
		{"admin", "admin_api_key", true, http.StatusForbidden, 0},
		{"admin", "jwt", false, http.StatusForbidden, 0},
		{"admin", "jwt", true, http.StatusOK, 1},
	} {
		t.Run(fmt.Sprintf("%s-%s-%t", tc.role, tc.method, tc.grant), func(t *testing.T) {
			users := payoutTestUsers{}
			store := &vaultHandlerStore{}
			h := NewMarketplaceVaultRotationHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: tc.grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
				c.Set("auth_method", tc.method)
				c.Next()
			})
			r.POST("/admin/marketplace/vault/rotate", h.Rotate)
			req := httptest.NewRequest(http.MethodPost, "/admin/marketplace/vault/rotate", bytes.NewBufferString(`{"operation_id":"batch-1","limit":5,"reason":"routine rotation"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, tc.calls, store.calls)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		})
	}
}

func TestVaultRotationHandlerRejectsSecretOrUnboundedInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"operation_id":"a","limit":201,"reason":"rotation"}`,
		`{"operation_id":"a","limit":1,"reason":" "}`,
		`{"operation_id":"a","limit":1,"reason":"rotation","key":"secret"}`,
	} {
		users := payoutTestUsers{}
		store := &vaultHandlerStore{}
		h := NewMarketplaceVaultRotationHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
			c.Set("auth_method", service.AuditAuthMethodJWT)
			c.Next()
		})
		r.POST("/admin/marketplace/vault/rotate", h.Rotate)
		req := httptest.NewRequest(http.MethodPost, "/admin/marketplace/vault/rotate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Zero(t, store.calls)
	}
}

func TestVaultRotationErrorsPreserveAmbiguousOperations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		failure error
		status  int
		reason  string
	}{{service.ErrVaultRotationConflict, 409, "VAULT_OPERATION_CONFLICT"}, {service.ErrVaultRotationInvalid, 503, "VAULT_OPERATION_UNKNOWN"}, {fmt.Errorf("commit unknown"), 503, "VAULT_OPERATION_UNKNOWN"}} {
		users := payoutTestUsers{}
		store := &vaultHandlerStore{failure: tc.failure}
		h := NewMarketplaceVaultRotationHandler(store, service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
			c.Set("auth_method", service.AuditAuthMethodJWT)
			c.Next()
		})
		r.POST("/rotate", h.Rotate)
		q := httptest.NewRequest(http.MethodPost, "/rotate", bytes.NewBufferString(`{"operation_id":"op","limit":1,"reason":"verified"}`))
		q.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		require.Equal(t, tc.status, w.Code)
		require.Contains(t, w.Body.String(), tc.reason)
	}
}

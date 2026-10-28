package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type payoutTestUsers struct{ service.UserRepository }

func (payoutTestUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, TotpEnabled: true}, nil
}

func (payoutTestUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type payoutTestGrant struct {
	service.TotpCache
	granted bool
}

func (p payoutTestGrant) HasStepUpGrant(context.Context, int64, string) (bool, error) {
	return p.granted, nil
}

type payoutTestStore struct{ service.SupplierPayoutStore }

func (payoutTestStore) RevealDestination(_ context.Context, id, actor int64, reason, operation string, _ *service.CredentialEnvelope) ([]byte, error) {
	return []byte("reveal-canary-secret"), nil
}

func (payoutTestStore) ListAdminDestinations(context.Context, int64) ([]service.AdminPayoutDestination, error) {
	return []service.AdminPayoutDestination{}, nil
}

func (payoutTestStore) CreateDestination(_ context.Context, actor int64, mask string, secret []byte, op string, _ *service.CredentialEnvelope) (*service.PayoutDestination, error) {
	if string(secret) != "create-canary-secret" {
		panic("unexpected credential")
	}
	return &service.PayoutDestination{ID: 4, SupplierID: actor, MaskedLabel: mask}, nil
}

type payoutTestAudit struct {
	service.AuditLogRepository
	mu   sync.Mutex
	logs []*service.AuditLog
}

func (r *payoutTestAudit) BatchInsert(_ context.Context, logs []*service.AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, logs...)
	return int64(len(logs)), nil
}
func (r *payoutTestAudit) Insert(_ context.Context, log *service.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return nil
}
func TestPayoutRevealRequiresStepUpAndNeverAuditsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		grant  bool
		auth   string
		status int
	}{{"granted", true, "jwt", http.StatusOK}, {"no grant", false, "jwt", http.StatusForbidden}, {"machine key", true, service.AuditAuthMethodAdminAPIKey, http.StatusForbidden}} {
		t.Run(tc.name, func(t *testing.T) {
			users := payoutTestUsers{}
			userSvc := service.NewUserService(users, nil, nil, nil)
			totp := service.NewTotpService(users, nil, payoutTestGrant{granted: tc.grant}, nil, nil, nil)
			h := NewSupplierPayoutHandler(service.NewSupplierPayoutService(payoutTestStore{}, nil, env), totp, userSvc)
			repo := &payoutTestAudit{}
			audit := service.NewAuditLogService(repo, nil)
			audit.Start()
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set("auth_method", tc.auth)
				c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
				c.Next()
			})
			router.Use(gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)))
			router.POST("/api/v1/admin/marketplace/payout-destinations/:id/reveal", h.RevealDestination)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/marketplace/payout-destinations/4/reveal", bytes.NewBufferString(`{"reason":"verify recipient"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "reveal-1")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			audit.Stop()
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			if tc.status == http.StatusOK {
				require.Contains(t, rec.Body.String(), "reveal-canary-secret")
			}
			repo.mu.Lock()
			defer repo.mu.Unlock()
			require.Len(t, repo.logs, 1)
			raw, err := json.Marshal(repo.logs)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "reveal-canary-secret")
		})
	}
}

func TestPayoutAdminDestinationListRejectsMachineKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewSupplierPayoutHandler(service.NewSupplierPayoutService(payoutTestStore{}, nil, nil), nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)
		c.Next()
	})
	router.GET("/api/v1/admin/marketplace/payout-destinations", h.AdminListDestinations)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/marketplace/payout-destinations", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestPayoutCreateHandlerNeverAuditsCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	env, err := service.NewCredentialEnvelope("k", map[string][]byte{"k": make([]byte, 32)})
	require.NoError(t, err)
	users := payoutTestUsers{}
	h := NewSupplierPayoutHandler(service.NewSupplierPayoutService(payoutTestStore{}, nil, env), service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	repo := &payoutTestAudit{}
	audit := service.NewAuditLogService(repo, nil)
	audit.Start()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set("auth_method", service.AuditAuthMethodJWT)
		c.Next()
	})
	router.Use(gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)))
	router.POST("/api/v1/supplier/payout-destinations", h.CreateDestination)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/supplier/payout-destinations", bytes.NewBufferString(`{"masked_label":"ending 1234","credential":"create-canary-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "create-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	audit.Stop()
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "create-canary-secret")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.logs, 1)
	raw, err := json.Marshal(repo.logs)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "create-canary-secret")
}

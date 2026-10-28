package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type incidentHandlerStore struct {
	calls                                 int
	actor, attempt                        int64
	receipt, release, evidence, operation string
}

func (*incidentHandlerStore) Resolve(context.Context, int64, string, string, string, string) error {
	return nil
}
func (s *incidentHandlerStore) RegisterExecutionIncident(_ context.Context, actor int64, receipt string, attempt int64, release, evidence, operation string) (*service.MarketplaceExecutionIncident, error) {
	s.calls++
	s.actor = actor
	s.receipt = receipt
	s.attempt = attempt
	s.release = release
	s.evidence = evidence
	s.operation = operation
	return &service.MarketplaceExecutionIncident{OperationID: operation, ReceiptID: receipt, AttemptID: attempt, ReleaseOperationID: release, ActorUserID: actor, Evidence: evidence, Status: "recorded_manual_review"}, nil
}
func (s *incidentHandlerStore) ListExecutionIncidents(_ context.Context, actor int64, receipt string) ([]service.MarketplaceExecutionIncident, error) {
	s.calls++
	s.actor = actor
	s.receipt = receipt
	return []service.MarketplaceExecutionIncident{{Status: "recorded_manual_review"}}, nil
}
func TestExecutionIncidentHandlerRequiresAdminJWTAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		grant      bool
		role, auth string
		want       int
	}{
		{"grant", true, service.RoleAdmin, "jwt", http.StatusOK},
		{"no grant", false, service.RoleAdmin, "jwt", http.StatusForbidden},
		{"machine key", true, service.RoleAdmin, service.AuditAuthMethodAdminAPIKey, http.StatusForbidden},
		{"nonadmin", true, service.RoleUser, "jwt", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &incidentHandlerStore{}
			users := payoutTestUsers{}
			h := NewSupplierFinanceHandler(nil, service.NewTotpService(users, nil, payoutTestGrant{granted: tc.grant}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
			h.SetRecoveryResolver(store)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				c.Set(string(middleware.ContextKeyUserRole), tc.role)
				c.Set("auth_method", tc.auth)
				c.Next()
			})
			r.POST("/receipts/:id/execution-incidents", h.AdminRegisterExecutionIncident)
			r.GET("/receipts/:id/execution-incidents", h.AdminListExecutionIncidents)
			for _, method := range []string{http.MethodPost, http.MethodGet} {
				req := httptest.NewRequest(method, "/receipts/receipt-id/execution-incidents", bytes.NewBufferString(`{"attempt_id":42,"release_operation_id":"release","evidence":"verified externally"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "incident-op")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, tc.want, w.Code)
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				if tc.want == http.StatusOK {
					var body map[string]any
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
					require.Contains(t, w.Body.String(), "recorded_manual_review")
					require.EqualValues(t, 7, store.actor)
					require.Equal(t, "receipt-id", store.receipt)
				}
			}
			if tc.want == http.StatusOK {
				require.Equal(t, 2, store.calls)
				require.EqualValues(t, 42, store.attempt)
				require.Equal(t, "release", store.release)
				require.Equal(t, "verified externally", store.evidence)
				require.Equal(t, "incident-op", store.operation)
			} else {
				require.Zero(t, store.calls)
			}
		})
	}
}
func TestExecutionIncidentHandlerRejectsExtraOrTrailingJSON(t *testing.T) {
	users := payoutTestUsers{}
	store := &incidentHandlerStore{}
	h := NewSupplierFinanceHandler(nil, service.NewTotpService(users, nil, payoutTestGrant{granted: true}, nil, nil, nil), service.NewUserService(users, nil, nil, nil))
	h.SetRecoveryResolver(store)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", "jwt")
		c.Next()
	})
	r.POST("/receipts/:id/execution-incidents", h.AdminRegisterExecutionIncident)
	for _, body := range []string{`{"attempt_id":1,"release_operation_id":"r","evidence":"e","amount":5}`, `{"attempt_id":1,"release_operation_id":"r","evidence":"e"} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/receipts/id/execution-incidents", bytes.NewBufferString(body))
		req.Header.Set("Idempotency-Key", "x")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
	require.Zero(t, store.calls)
}

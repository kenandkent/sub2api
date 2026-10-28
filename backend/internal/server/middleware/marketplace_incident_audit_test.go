package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMarketplaceIncidentAndChargebackAuditOmitEvidenceAndAuditReads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &auditCaptureRepository{}
	audit := service.NewAuditLogService(repo, nil)
	audit.Start()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 77})
		c.Set(string(ContextKeyUserRole), "admin")
		c.Next()
	})
	r.Use(gin.HandlerFunc(NewAuditLogMiddleware(audit)))
	handler := func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		if c.Request.Method == http.MethodPost {
			require.Contains(t, string(body), "private-evidence-canary")
		}
		c.Status(http.StatusOK)
	}
	r.POST("/api/v1/admin/marketplace/receipts/:id/execution-incidents", handler)
	r.GET("/api/v1/admin/marketplace/receipts/:id/execution-incidents", handler)
	r.POST("/api/v1/admin/marketplace/funding-chargebacks", handler)
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/api/v1/admin/marketplace/receipts/123/execution-incidents"}, {http.MethodGet, "/api/v1/admin/marketplace/receipts/123/execution-incidents"}, {http.MethodPost, "/api/v1/admin/marketplace/funding-chargebacks"}} {
		req := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(`{"evidence":"private-evidence-canary"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}
	audit.Stop()
	repo.mu.Lock()
	logs := append([]*service.AuditLog(nil), repo.logs...)
	repo.mu.Unlock()
	require.Len(t, logs, 3)
	seenRead := false
	for _, entry := range logs {
		require.NotContains(t, entry.RequestBody, "private-evidence-canary")
		if entry.Method == http.MethodPost {
			require.Equal(t, "<credential-bearing body omitted>", entry.RequestBody)
		}
		if entry.Action == "admin.marketplace.execution_incidents.read" {
			seenRead = true
		}
	}
	require.True(t, seenRead)
}

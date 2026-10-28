package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestVaultRotationAuditOmitsRejectedSecretBearingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &auditCaptureRepository{}
	audit := service.NewAuditLogService(repo, nil)
	audit.Start()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 77})
		c.Set(string(ContextKeyUserRole), "admin")
		c.Next()
	})
	router.Use(gin.HandlerFunc(NewAuditLogMiddleware(audit)))
	router.POST("/api/v1/admin/marketplace/vault/rotate", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/marketplace/vault/rotate", bytes.NewBufferString(`{"operation_id":"op","limit":1,"reason":"rotate","data":"private-vault-canary"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	audit.Stop()
	require.Equal(t, http.StatusBadRequest, rec.Code)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.logs, 1)
	require.NotContains(t, repo.logs[0].RequestBody, "private-vault-canary")
	require.Equal(t, "<credential-bearing body omitted>", repo.logs[0].RequestBody)
}

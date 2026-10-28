//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type handlerAuthCache struct{}

func (handlerAuthCache) Put(context.Context, string, service.SupplierAuthorizationSession) error {
	return nil
}
func (handlerAuthCache) Consume(context.Context, string, int64) (service.SupplierAuthorizationSession, error) {
	return service.SupplierAuthorizationSession{}, nil
}

type handlerAuthExchange struct{}

func (handlerAuthExchange) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return nil, nil
}

type handlerAuthWriter struct{}

func (handlerAuthWriter) SaveSupplierAuthorization(context.Context, int64, string, int64, string, *openai.TokenResponse) (int64, error) {
	return 0, nil
}

type handlerAuthOperations struct{}

func (handlerAuthOperations) Create(context.Context, service.SupplierAuthorizationOperation) error {
	return nil
}
func (handlerAuthOperations) Claim(context.Context, int64, string, string) (bool, error) {
	return false, nil
}
func (handlerAuthOperations) Fail(context.Context, int64, string, string, string) error { return nil }
func (handlerAuthOperations) Get(_ context.Context, user int64, id string) (*service.SupplierAuthorizationOperation, error) {
	if user != 7 || id != "session" {
		return nil, nil
	}
	account := int64(42)
	return &service.SupplierAuthorizationOperation{UserID: 7, ID: id, Status: "completed", AccountID: &account, StateDigest: "secret-state-hash", CallbackDigest: "secret-code-hash"}, nil
}
func TestSupplierAuthorizationStatusDoesNotExposeBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _ := service.NewSupplierAuthorizationService(handlerAuthCache{}, handlerAuthExchange{}, handlerAuthWriter{}, service.SupplierAuthorizationConfig{Provider: "openai", RedirectURI: "https://example.test/callback"})
	svc.SetOperationStore(handlerAuthOperations{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Next()
	})
	h := NewSupplierAuthorizationHandler(svc)
	router.GET("/authorizations/:id", h.Status)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/authorizations/session", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"account_id":42`) || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "session") {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/authorizations/other", nil))
	if w.Code != 404 {
		t.Fatalf("missing operation: %d", w.Code)
	}
}
func TestSupplierAuthorizationHandlerRequiresSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewSupplierAuthorizationHandler(nil)
	router.POST("/authorizations", h.Start)
	router.POST("/authorizations/:id/complete", h.Complete)
	router.GET("/authorizations/:id", h.Status)
	for _, tc := range []struct{ method, path string }{{"POST", "/authorizations"}, {"POST", "/authorizations/id/complete"}, {"GET", "/authorizations/id"}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != 401 {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}

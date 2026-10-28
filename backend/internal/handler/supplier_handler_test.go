//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func TestSupplierHandlerRequiresAuthenticatedSubject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSupplierHandler(nil)
	r.GET("/capabilities", h.Capabilities)
	r.POST("/profile", h.Apply)
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/capabilities"}, {http.MethodPost, "/profile"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}

func TestSupplierAdminRejectsOrdinaryUsersAndSharedAdminKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ role, method string }{{"user", "jwt"}, {"admin", "admin_api_key"}} {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 8})
			c.Set(string(middleware.ContextKeyUserRole), tc.role)
			c.Set("auth_method", tc.method)
			c.Next()
		})
		h := NewSupplierHandler(nil)
		r.POST("/suppliers/:id/review", h.AdminReview)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/suppliers/3/review", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d", tc.role, tc.method, w.Code)
		}
	}
}

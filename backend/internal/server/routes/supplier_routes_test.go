//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func TestSupplierRoutesInheritUserGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	chains := map[string]string{}
	auth := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		names := c.HandlerNames()
		chains[c.FullPath()] = strings.Join(names[:len(names)-1], "\n")
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	RegisterUserRoutes(r.Group("/api/v1"), &handler.Handlers{Supplier: handler.NewSupplierHandler(nil)}, auth, audit, nil, nil)
	for _, path := range []string{"/api/v1/user/profile", "/api/v1/user/capabilities", "/api/v1/supplier/options", "/api/v1/supplier/profile", "/api/v1/supplier/accounts", "/api/v1/supplier/accounts/5"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	baseline := chains["/api/v1/user/profile"]
	if baseline == "" {
		t.Fatal("no user chain")
	}
	for _, path := range []string{"/api/v1/user/capabilities", "/api/v1/supplier/options", "/api/v1/supplier/profile", "/api/v1/supplier/accounts", "/api/v1/supplier/accounts/:id"} {
		if chains[path] != baseline {
			t.Fatalf("%s missing user guard chain", path)
		}
	}
}
func TestSupplierAdminRoutesRequireAdminAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	chains := map[string]string{}
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		names := c.HandlerNames()
		chains[c.FullPath()] = strings.Join(names[:len(names)-1], "\n")
		if c.GetHeader("Authorization") == "" {
			c.AbortWithStatus(401)
		} else {
			c.AbortWithStatus(403)
		}
	})
	audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	step := middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	h := &handler.Handlers{Supplier: handler.NewSupplierHandler(nil), Admin: &handler.AdminHandlers{Account: &adminhandler.AccountHandler{}}}
	RegisterAdminRoutes(r.Group("/api/v1"), h, auth, audit, step, nil, nil)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/v1/admin/marketplace/suppliers"}, {"POST", "/api/v1/admin/marketplace/suppliers/2/review"}} {
		for _, bearer := range []string{"", "Bearer user"} {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", bearer)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			want := 401
			if bearer != "" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("%s: %d", tc.path, w.Code)
			}
		}
	}
	if chains["/api/v1/admin/marketplace/suppliers"] == "" || chains["/api/v1/admin/marketplace/suppliers"] != chains["/api/v1/admin/marketplace/suppliers/:id/review"] {
		t.Fatal("admin chain mismatch")
	}
}

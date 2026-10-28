package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFundingRecoveryFailsClosedWithoutAdminSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewPaymentFundingRecoveryHandler(nil, nil, nil)
	r.GET("/review", h.List)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/review", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", w.Code)
	}
}

package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerSupplierRoutes(authenticated *gin.RouterGroup, h *handler.Handlers) {
	authenticated.GET("/user/capabilities", h.Supplier.Capabilities)
	supplier := authenticated.Group("/supplier")
	supplier.GET("/options", h.Supplier.Options)
	supplier.GET("/profile", h.Supplier.Profile)
	supplier.POST("/profile", h.Supplier.Apply)
	supplier.GET("/accounts", h.Supplier.Accounts)
	supplier.GET("/accounts/:id", h.Supplier.Account)
}
func registerAdminSupplierRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	suppliers := admin.Group("/marketplace/suppliers")
	suppliers.GET("", h.Supplier.AdminList)
	suppliers.POST("/:id/review", h.Supplier.AdminReview)
}

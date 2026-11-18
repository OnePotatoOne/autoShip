// Package product 挂载产品档案与子配置管理端路由。
package product

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/product/handler"
)

// RegisterAdminRoutes 将产品档案相关 CRUD 与沙箱一键放行接口挂载到管理端受保护路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为产品控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/products", h.List)
	rg.POST("/products", h.Create)
	rg.GET("/products/:id", h.Get)
	rg.PUT("/products/:id", h.Update)
	rg.DELETE("/products/:id", h.Delete)
	rg.PUT("/products/:id/modules", h.UpdateModules)
	rg.PUT("/products/:id/scope-rules", h.UpdateScopeRules)
	rg.PUT("/products/:id/permission-profile", h.UpdatePermissionProfile)
	rg.POST("/products/:id/permission-profile/allow-path", h.AllowPath)
	rg.PUT("/products/:id/knowledge", h.UpdateKnowledge)
}

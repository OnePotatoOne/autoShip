// Package integration 挂载接入方管理路由。
package integration

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/integration/handler"
)

// RegisterAdminRoutes 将接入方管理与对外接口文档获取路由注册到管理端受保护路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为接入方控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/integrations", h.List)
	rg.GET("/integrations/open-api-doc", h.OpenAPIDoc)
	rg.POST("/integrations", h.Create)
	rg.PUT("/integrations/:id", h.Update)
	rg.DELETE("/integrations/:id", h.Delete)
}

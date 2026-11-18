// Package requirement 挂载需求池管理端路由。
package requirement

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/requirement/handler"
)

// RegisterAdminRoutes 将需求池检索、手动录入、分拣、关联、关闭与开工接口注册到管理端受保护路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为需求池控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/requirements", h.List)
	rg.POST("/requirements", h.Create)
	rg.GET("/requirements/:id", h.Get)
	rg.POST("/requirements/:id/triage", h.Triage)
	rg.POST("/requirements/:id/link", h.Link)
	rg.POST("/requirements/:id/close", h.Close)
	rg.POST("/requirements/:id/start-work", h.StartWork)
}

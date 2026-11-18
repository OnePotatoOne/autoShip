// Package work 挂载管理后台开发任务路由。
package work

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/work/handler"
)

// RegisterAdminRoutes 将开发任务查询、方案确认、答复与验收审批接口注册到管理端受保护路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为开发任务控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/work-items", h.List)
	rg.GET("/work-items/:id", h.Get)
	rg.POST("/work-items/:id/confirm-plan", h.ConfirmPlan)
	rg.POST("/work-items/:id/answer", h.Answer)
	rg.POST("/work-items/:id/accept", h.Accept)
	rg.POST("/work-items/:id/reject", h.Reject)
	rg.POST("/work-items/:id/abandon", h.Abandon)
}

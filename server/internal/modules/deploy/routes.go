// Package deploy 挂载部署中心管理端路由与更新 Agent 报告路由。
package deploy

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/deploy/handler"
)

// RegisterAdminRoutes 将部署目标、部署记录、手动回滚与手动备份接口注册到管理后台路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为部署控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/deploy-targets", h.ListTargets)
	rg.POST("/deploy-targets", h.CreateTarget)
	rg.PUT("/deploy-targets/:id", h.UpdateTarget)
	rg.DELETE("/deploy-targets/:id", h.DeleteTarget)
	rg.POST("/deploy-targets/:id/backup", h.BackupTarget)
	rg.GET("/deployments", h.ListDeployments)
	rg.GET("/deployments/:id", h.GetDeployment)
	rg.POST("/deployments/:id/rollback", h.Rollback)
	rg.GET("/backups", h.ListBackups)
}

// RegisterUpdateRoutes 将部署状态上报接口注册到公网已鉴权更新 Agent 路由组。
// 参数 rg 为 `/agent/v1/deploy` 路由组，h 为部署控制器；无返回值。
func RegisterUpdateRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.POST("/deployments/:deploymentId/report", h.ReportFromUpdateAgent)
}

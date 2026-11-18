// Package audit 挂载审计与全局设置路由。
package audit

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/audit/handler"
)

// RegisterAdminRoutes 将审计查询、全局配置与根证书下载接口挂载到已鉴权的管理端路由组。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为审计 Handler；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/audit-logs", h.ListAuditLogs)
	rg.GET("/settings", h.GetSettings)
	rg.PUT("/settings", h.UpdateSettings)
	rg.GET("/pki/ca.crt", h.DownloadCACert)
}

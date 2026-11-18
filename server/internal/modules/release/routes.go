// Package release 挂载版本管理端路由、Tailnet 开发 Agent 分片上传路由及公网更新 Agent 下载路由。
package release

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/release/handler"
)

// RegisterAdminRoutes 注册管理后台版本查询、确认部署与新建修复任务接口。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为版本控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/releases", h.List)
	rg.GET("/releases/:id", h.Get)
	rg.POST("/releases/:id/create-fix-work", h.CreateFixWork)
	rg.POST("/releases/:id/confirm-deploy", h.ConfirmDeploy)
}

// RegisterDevRoutes 注册 Tailnet 开发 Agent 的分片续传上传与失败上报接口。
// 参数 rg 为已鉴权的 `/agent/v1/dev` 路由组，h 为版本控制器；无返回值。
func RegisterDevRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.POST("/releases/:releaseId/upload/init", h.InitUpload)
	rg.PUT("/releases/:releaseId/upload/:uploadId/chunk/:index", h.UploadChunk)
	rg.POST("/releases/:releaseId/upload/:uploadId/complete", h.CompleteUpload)
	rg.POST("/releases/:releaseId/fail", h.ReportPublishFailed)
}

// RegisterUpdateRoutes 注册公网更新 Agent 的制品包断点续传下载接口。
// 参数 rg 为已鉴权的 `/agent/v1/deploy` 路由组，h 为版本控制器；无返回值。
func RegisterUpdateRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/artifacts/:releaseId/download", h.DownloadArtifactForUpdateAgent)
}

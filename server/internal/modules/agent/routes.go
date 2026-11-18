// Package agent 挂载 Agent 管理路由、Tailnet 开发 Agent 路由及公网更新 Agent 路由。
package agent

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/agent/handler"
)

// RegisterAdminRoutes 注册管理后台 Agent 列表、令牌生成与吊销接口。
// 参数 rg 为 `/api/v1/admin` 路由组，h 为 Agent 控制器；无返回值。
func RegisterAdminRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/agents", h.ListAgents)
	rg.POST("/agents/tokens", h.CreateToken)
	rg.POST("/agents/:id/revoke", h.Revoke)
}

// RegisterDevRoutes 注册 Tailnet 监听器上的开发 Agent 注册（免签名令牌）与已签名通信接口。
// 参数 publicDevGroup 为未签名注册组，authedDevGroup 为通过 Ed25519 校验的路由组，h 为控制器；无返回值。
func RegisterDevRoutes(publicDevGroup, authedDevGroup *gin.RouterGroup, h *handler.Handler) {
	publicDevGroup.POST("/register", h.RegisterDev)
	authedDevGroup.POST("/cert/renew", h.RenewDevCert)
	authedDevGroup.GET("/ws", h.HandleDevWSS)
	authedDevGroup.POST("/events", h.SyncDevEvents)
}

// RegisterUpdateRoutes 注册公网监听器上的更新 Agent 注册（免签名令牌）、心跳与 30 秒长轮询接口。
// 参数 publicUpdateGroup 为未签名注册组，authedUpdateGroup 为通过 Ed25519 校验的路由组，h 为控制器；无返回值。
func RegisterUpdateRoutes(publicUpdateGroup, authedUpdateGroup *gin.RouterGroup, h *handler.Handler) {
	publicUpdateGroup.POST("/register", h.RegisterUpdate)
	authedUpdateGroup.POST("/heartbeat", h.UpdateHeartbeat)
	authedUpdateGroup.GET("/poll", h.PollUpdateCommands)
}

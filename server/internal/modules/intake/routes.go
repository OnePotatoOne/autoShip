// Package intake 挂载公网 Open API v1 标准路由。
package intake

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/intake/handler"
)

// RegisterOpenV1Routes 在独立外部端口已通过 HMAC 验签与来源白名单的 `/open/v1` 路由组上注册全部 9 个对外标准接口。
// 参数 rg 为 `/open/v1` 路由组，h 为 Open API 控制器；无返回值。
func RegisterOpenV1Routes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/ping", h.Ping)
	rg.POST("/issues", h.SubmitDirectIssue)
	rg.POST("/conversations", h.CreateConversation)
	rg.POST("/conversations/:id/messages", h.SendMessage)
	rg.POST("/attachments", h.UploadAttachment)
	rg.POST("/conversations/:id/confirm", h.ConfirmSummary)
	rg.GET("/reporters/:rid/issues", h.ListReporterIssues)
	rg.GET("/issues/:id", h.GetIssueDetail)
	rg.GET("/events", h.PullEvents)
}

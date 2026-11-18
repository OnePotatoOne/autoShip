// Package auth 挂载管理端认证公开路由与需登录路由。
package auth

import (
	"github.com/gin-gonic/gin"

	"autoship/internal/modules/auth/handler"
)

// RegisterPublicRoutes 注册无需登录即可访问的认证状态、首次初始化与双因素登录接口。
// 参数 rg 为 `/api/v1/admin` 公开组，h 为认证控制器；无返回值。
func RegisterPublicRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.GET("/auth/status", h.Status)
	rg.POST("/auth/db-setup", h.SetupDatabase)
	rg.GET("/auth/ca.crt", h.PublicDownloadCACert)
	rg.POST("/auth/setup", h.Setup)
	rg.POST("/auth/login", h.Login)
}

// RegisterProtectedRoutes 注册需要有效管理员会话与 CSRF 校验的 OTP 绑定与登出接口。
// 参数 rg 为 `/api/v1/admin` 受保护组，h 为认证控制器；无返回值。
func RegisterProtectedRoutes(rg *gin.RouterGroup, h *handler.Handler) {
	rg.POST("/auth/otp/setup", h.SetupOTP)
	rg.POST("/auth/otp/enable", h.EnableOTP)
	rg.POST("/auth/logout", h.Logout)
}

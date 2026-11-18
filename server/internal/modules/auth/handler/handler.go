// Package handler 实现管理后台认证状态查询、首次初始化、双因素登录、OTP 绑定激活与登出接口。
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"autoship/internal/config"
	"autoship/internal/database"
	"autoship/internal/modules/auth/service"
	"autoship/pkg/crypto"
	"autoship/pkg/pki"
	"autoship/pkg/response"
)

// Handler 封装管理端认证 HTTP 接口方法。
type Handler struct {
	svc     *service.Service
	cfg     *config.ServerConfig
	cfgPath string
	encKey  []byte
	ca      *pki.Bundle
}

// New 创建认证模块 HTTP 控制器。
// 参数 svc 为认证服务实例，cfg/cfgPath/encKey 为全局配置及持久化路径，ca 为根 CA Bundle；返回 *Handler。
func New(svc *service.Service, cfg *config.ServerConfig, cfgPath string, encKey []byte, ca *pki.Bundle) *Handler {
	return &Handler{svc: svc, cfg: cfg, cfgPath: cfgPath, encKey: encKey, ca: ca}
}

// SessionCookieName 返回带平台专属前缀的 HttpOnly 会话 Cookie 名称。
// 无入参；返回形如 "autoship_sid" 的 Cookie 名。
func (h *Handler) SessionCookieName() string {
	return h.cfg.CookiePrefix + "sid"
}

// CSRFCookieName 返回带平台专属前缀的前端可读 CSRF 辅助 Cookie 名称。
// 无入参；返回形如 "autoship_csrf" 的 Cookie 名。
func (h *Handler) CSRFCookieName() string {
	return h.cfg.CookiePrefix + "csrf"
}

// Status 返回平台数据库连通态、管理员初始化状态、根 CA 指纹及当前浏览器是否已持有有效管理员会话。
// 参数 c 为 Gin 请求上下文；输出包含 dbReady、initialized、authenticated、caFingerprint、user、csrfToken 的 JSON。
func (h *Handler) Status(c *gin.Context) {
	caFP := ""
	if h.ca != nil {
		caFP = h.ca.CAFingerprint
	}
	if database.Get() == nil {
		response.OK(c, gin.H{
			"dbReady":       false,
			"initialized":   false,
			"authenticated": false,
			"caFingerprint": caFP,
			"dbConfig": gin.H{
				"host":   h.cfg.Database.Host,
				"port":   h.cfg.Database.Port,
				"user":   h.cfg.Database.User,
				"dbName": h.cfg.Database.DBName,
			},
		})
		return
	}
	initialized, err := h.svc.IsInitialized()
	if err != nil {
		response.ServiceUnavailable(c, "数据库状态读取异常，请检查服务数据库配置")
		return
	}
	rawToken, _ := c.Cookie(h.SessionCookieName())
	sess, user, _ := h.svc.AuthenticateSession(rawToken)
	if sess == nil || user == nil {
		response.OK(c, gin.H{
			"dbReady":       true,
			"initialized":   initialized,
			"authenticated": false,
			"caFingerprint": caFP,
		})
		return
	}
	response.OK(c, gin.H{
		"dbReady":       true,
		"initialized":   initialized,
		"authenticated": true,
		"caFingerprint": caFP,
		"user":          user,
		"csrfToken":     sess.CSRFToken,
	})
}

// SetupDatabase 在首次启动且数据库尚未连通时，接收管理端向导填写的 MySQL 8 连接参数，加密口令、自动建库迁移全部表与注释并保存配置。
// 参数 c 为 Gin 请求上下文；连通迁移成功返回 dbReady=true，已连通状态下拒绝未授权重配。
func (h *Handler) SetupDatabase(c *gin.Context) {
	if database.Get() != nil {
		response.Forbidden(c, "数据库已完成连接初始化，禁止未登录状态修改")
		return
	}
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		DBName   string `json:"dbName"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "数据库连接参数格式非法")
		return
	}
	if strings.TrimSpace(req.Host) == "" || strings.TrimSpace(req.User) == "" || strings.TrimSpace(req.DBName) == "" {
		response.BadRequest(c, "数据库主机、用户名和库名均不能为空")
		return
	}
	if req.Port <= 0 {
		req.Port = 3306
	}
	encPass, err := crypto.EncryptField(req.Password, h.encKey)
	if err != nil {
		response.ServerError(c, "加密数据库口令失败")
		return
	}
	candidate := h.cfg.Database
	candidate.Host = strings.TrimSpace(req.Host)
	candidate.Port = req.Port
	candidate.User = strings.TrimSpace(req.User)
	candidate.Password = encPass
	candidate.DBName = strings.TrimSpace(req.DBName)

	if _, err := database.Init(candidate, h.encKey); err != nil {
		response.BadRequest(c, "连接或初始化 MySQL 8 失败："+err.Error())
		return
	}
	h.cfg.Database = candidate
	if h.cfgPath != "" {
		_ = config.Save(h.cfgPath, h.cfg)
	}
	initialized, _ := h.svc.IsInitialized()
	response.OK(c, gin.H{
		"dbReady":     true,
		"initialized": initialized,
	})
}

// PublicDownloadCACert 在 Tailnet 管理后台向导页提供免登录下载自建根 CA 公开证书，方便管理员首次安装系统信任。
// 参数 c 为 Gin 请求上下文；直接输出 PEM 证书附件流。
func (h *Handler) PublicDownloadCACert(c *gin.Context) {
	if h.ca == nil || len(h.ca.CACertPEM) == 0 {
		response.NotFound(c, "根 CA 证书尚未就绪")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="autoship-ca.crt"`)
	c.Data(http.StatusOK, "application/x-x509-ca-cert", h.ca.CACertPEM)
}

// Setup 在首次启动未创建任何用户时初始化管理员并下发会话 Cookie。
// 参数 c 为 Gin 请求上下文；输出初始管理员信息、OTP 绑定二维码 URI、一次性恢复码及 csrfToken。
func (h *Handler) Setup(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数格式非法")
		return
	}
	res, err := h.svc.InitialSetup(req.Username, req.Password, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.writeAuthCookies(c, res.SessionToken, res.CSRFToken)
	response.OK(c, res)
}

// Login 处理管理员账号密码 + OTP 动态口令 / 恢复码登录。
// 参数 c 为 Gin 请求上下文；验证成功后种下安全 HttpOnly Cookie 并返回 csrfToken。
func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		OTPCode      string `json:"otpCode"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数格式非法")
		return
	}
	res, err := h.svc.Login(req.Username, req.Password, req.OTPCode, req.RecoveryCode, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		switch {
		case errors.Is(err, service.ErrRateLimited):
			response.TooManyRequests(c, err.Error())
		case errors.Is(err, service.ErrAccountLocked):
			response.Forbidden(c, err.Error())
		case errors.Is(err, service.ErrOTPRequired):
			response.FailWith(c, http.StatusUnauthorized, 40101, err.Error(), gin.H{"otpRequired": true})
		default:
			response.Unauthorized(c, "用户名、密码或动态验证码错误")
		}
		return
	}
	h.writeAuthCookies(c, res.SessionToken, res.CSRFToken)
	response.OK(c, res)
}

// SetupOTP 为当前已登录管理员生成新的 OTP 绑定密钥与恢复码。
// 参数 c 为 Gin 请求上下文；返回 secret、uri、recoveryCodes。
func (h *Handler) SetupOTP(c *gin.Context) {
	uid := c.GetUint64("autoship.admin_user_id")
	secret, uri, codes, err := h.svc.PrepareOTP(uid)
	if err != nil {
		response.ServerError(c, "生成 OTP 密钥失败")
		return
	}
	response.OK(c, gin.H{
		"secret":        secret,
		"uri":           uri,
		"recoveryCodes": codes,
	})
}

// EnableOTP 校验 6 位验证码并开启当前管理员账号的强制 OTP 认证。
// 参数 c 为 Gin 请求上下文；成功返回 enabled=true。
func (h *Handler) EnableOTP(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请输入 6 位动态口令")
		return
	}
	uid := c.GetUint64("autoship.admin_user_id")
	if err := h.svc.EnableOTP(uid, req.Code); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"enabled": true})
}

// Logout 注销当前服务端会话并清空浏览器 Cookie。
// 参数 c 为 Gin 请求上下文；输出成功响应。
func (h *Handler) Logout(c *gin.Context) {
	rawToken, _ := c.Cookie(h.SessionCookieName())
	_ = h.svc.Logout(rawToken)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(h.SessionCookieName(), "", -1, "/", "", true, true)
	c.SetCookie(h.CSRFCookieName(), "", -1, "/", "", true, false)
	response.OK(c, gin.H{"loggedOut": true})
}

// writeAuthCookies 向客户端写入带平台专属前缀的 HttpOnly 会话 Cookie 与前端可读的 CSRF 辅助 Cookie；参数 c 为 Gin 上下文，sessionToken/csrfToken 为令牌值，由 Setup 与 Login 调用。
func (h *Handler) writeAuthCookies(c *gin.Context, sessionToken, csrfToken string) {
	isTLS := c.Request.TLS != nil
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(h.SessionCookieName(), sessionToken, 86400, "/", "", isTLS, true)
	c.SetCookie(h.CSRFCookieName(), csrfToken, 86400, "/", "", isTLS, false)
}

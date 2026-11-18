// Package middleware 实现 autoShip Server 的请求追踪、管理后台会话与 Origin+CSRF 防护、公网来源 IP 白名单、Open API HMAC 验签、Agent Ed25519 验签与全量审计中间件。
package middleware

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"autoship/internal/config"
	agentSvc "autoship/internal/modules/agent/service"
	auditModel "autoship/internal/modules/audit/model"
	auditSvc "autoship/internal/modules/audit/service"
	authSvc "autoship/internal/modules/auth/service"
	deploySvc "autoship/internal/modules/deploy/service"
	integSvc "autoship/internal/modules/integration/service"
	"autoship/pkg/response"
)

// RequestID 为每个进入系统的请求分配唯一关联编号并写回响应头 `X-Request-Id`。
// 无入参；返回 Gin 中间件函数。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := strings.TrimSpace(c.GetHeader("X-Request-Id"))
		if rid == "" {
			rid = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		c.Set(response.ContextKeyRequestID, rid)
		c.Header("X-Request-Id", rid)
		c.Next()
	}
}

// SecurityHeaders 为管理后台与 API 附加严格安全响应头（禁止外站 iframe 嵌套、强制 HTTPS HSTS、禁止 MIME 嗅探）。
// 无入参；返回 Gin 中间件函数。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

// AdminAuth 校验管理后台请求的 HttpOnly 会话 Cookie，并对所有写方法（POST/PUT/DELETE/PATCH）强制校验同源 Origin 与会话绑定的 CSRF 令牌（10 节安全要求）。
// 参数 cfg 为全局配置，auth 为认证服务；返回 Gin 中间件函数。
func AdminAuth(cfg *config.ServerConfig, auth *authSvc.Service) gin.HandlerFunc {
	cookieName := cfg.CookiePrefix + "sid"
	return func(c *gin.Context) {
		rawToken, err := c.Cookie(cookieName)
		if err != nil || strings.TrimSpace(rawToken) == "" {
			response.Unauthorized(c, "请先登录管理后台")
			c.Abort()
			return
		}
		sess, user, err := auth.AuthenticateSession(rawToken)
		if err != nil || sess == nil || user == nil {
			response.Unauthorized(c, "登录会话已过期或无效，请重新登录")
			c.Abort()
			return
		}

		// 对一切非只读请求强制执行 Origin 同主机端口检查 + 会话级 CSRF 令牌恒定时间比对
		if isMutatingMethod(c.Request.Method) {
			if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
				parsedOrigin, parseErr := url.Parse(origin)
				if parseErr != nil || !strings.EqualFold(parsedOrigin.Host, c.Request.Host) {
					response.Forbidden(c, "跨源写请求被拒绝（Origin 不匹配）")
					c.Abort()
					return
				}
			}
			csrfHeader := strings.TrimSpace(c.GetHeader("X-AutoShip-CSRF"))
			if csrfHeader == "" || subtle.ConstantTimeCompare([]byte(csrfHeader), []byte(sess.CSRFToken)) != 1 {
				response.Forbidden(c, "CSRF 安全令牌缺失或无效")
				c.Abort()
				return
			}
		}

		c.Set("autoship.admin_user_id", user.ID)
		c.Set("autoship.admin_username", user.Username)
		c.Next()
	}
}

// PublicIPAllowlist 在公网监听器的最外层执行应用级固定来源 IP 白名单拦截（聚合全局放行 IP、已启用接入方白名单、更新 Agent 白名单及部署目标服务器 IP）。
// 参数 cfg 为全局配置，integs 为接入方服务，agents 为 Agent 服务，deploys 为部署服务；返回 Gin 中间件函数。
func PublicIPAllowlist(
	cfg *config.ServerConfig,
	integs *integSvc.Service,
	agents *agentSvc.Service,
	deploys *deploySvc.Service,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		var rules []string
		rules = append(rules, cfg.PublicAllowIPs...)
		if integs != nil {
			rules = append(rules, integs.AllWhitelistedIPs()...)
		}
		if agents != nil {
			rules = append(rules, agents.AllWhitelistedIPs()...)
		}
		if deploys != nil {
			if targets, err := deploys.ListTargets(0); err == nil {
				for _, t := range targets {
					if t.Enabled && strings.TrimSpace(t.ServerIP) != "" {
						rules = append(rules, strings.TrimSpace(t.ServerIP))
					}
				}
			}
		}

		if !integSvc.MatchAllowedIPs(clientIP, strings.Join(rules, ",")) {
			response.Forbidden(c, "来源 IP 未列入 autoShip 公网白名单")
			c.Abort()
			return
		}
		c.Next()
	}
}

// OpenAPIAuth 校验 `/open/v1/*` 请求的 AppID、5 分钟时间窗、Nonce 防重放与 HMAC-SHA256 签名。
// 参数 integs 为接入方服务；返回 Gin 中间件函数。
func OpenAPIAuth(integs *integSvc.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		appID := c.GetHeader("X-AutoShip-App-Id")
		ts := c.GetHeader("X-AutoShip-Timestamp")
		nonce := c.GetHeader("X-AutoShip-Nonce")
		sig := c.GetHeader("X-AutoShip-Signature")
		if appID == "" || ts == "" || nonce == "" || sig == "" {
			response.Unauthorized(c, "缺少 Open API 签名认证请求头")
			c.Abort()
			return
		}

		bodyBytes, err := readAndRestoreBody(c, 12*1024*1024)
		if err != nil {
			response.BadRequest(c, "读取请求体失败或体积超限")
			c.Abort()
			return
		}

		integ, err := integs.VerifyOpenRequest(appID, ts, nonce, sig, c.Request.Method, requestPathWithQuery(c), bodyBytes, c.ClientIP())
		if err != nil {
			response.Unauthorized(c, err.Error())
			c.Abort()
			return
		}
		c.Set("autoship.integration", integ)
		c.Next()
	}
}

// AgentAuth 校验开发 Agent 或更新 Agent 请求的 Ed25519 私钥签名、时间窗、Nonce 防重放及角色隔离（7.3 节）。
// 参数 agents 为 Agent 服务，expectedType 为 "dev" 或 "update"；返回 Gin 中间件函数。
func AgentAuth(agents *agentSvc.Service, expectedType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.GetHeader("X-AutoShip-Agent-Id")
		ts := c.GetHeader("X-AutoShip-Timestamp")
		nonce := c.GetHeader("X-AutoShip-Nonce")
		sig := c.GetHeader("X-AutoShip-Signature")
		agentID, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 64)
		if err != nil || agentID == 0 || ts == "" || nonce == "" || sig == "" {
			response.Unauthorized(c, "缺少有效的 Agent Ed25519 签名请求头")
			c.Abort()
			return
		}

		bodyBytes, err := readAndRestoreBody(c, 36*1024*1024)
		if err != nil {
			response.BadRequest(c, "读取 Agent 请求载荷失败或超限")
			c.Abort()
			return
		}

		ag, err := agents.VerifyAgentRequest(agentID, expectedType, ts, nonce, sig, c.Request.Method, requestPathWithQuery(c), bodyBytes, c.ClientIP())
		if err != nil {
			response.Unauthorized(c, err.Error())
			c.Abort()
			return
		}
		c.Set("autoship.agent", ag)
		c.Set("autoship.agent_id", ag.ID)
		c.Next()
	}
}

// AuditLogger 自动拦截并记录所有非只读操作请求到 `audit_logs` 表（5.1 & 10 节全量审计）。
// 参数 auditor 为审计服务，actorType 为通道角色标识；返回 Gin 中间件函数。
func AuditLogger(auditor *auditSvc.Service, actorType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if auditor == nil || !isMutatingMethod(c.Request.Method) {
			return
		}
		actorID := c.GetString("autoship.admin_username")
		if actorID == "" {
			if aid := c.GetUint64("autoship.agent_id"); aid > 0 {
				actorID = strconv.FormatUint(aid, 10)
			} else if appID := c.GetHeader("X-AutoShip-App-Id"); appID != "" {
				actorID = appID
			} else {
				actorID = "anonymous"
			}
		}

		parts := strings.Split(strings.Trim(c.Request.URL.Path, "/"), "/")
		mod := "platform"
		if len(parts) >= 4 && parts[0] == "api" {
			mod = parts[3]
		} else if len(parts) >= 3 {
			mod = parts[2]
		}

		auditor.Record(auditModel.AuditLog{
			ActorType:  actorType,
			ActorID:    actorID,
			Module:     mod,
			Action:     c.Request.Method + " " + c.FullPath(),
			Method:     c.Request.Method,
			Path:       c.Request.URL.Path,
			ClientIP:   c.ClientIP(),
			StatusCode: c.Writer.Status(),
			ErrorMsg:   response.AuditError(c),
		}, nil)
	}
}

// isMutatingMethod 判断 HTTP 方法是否为会产生状态副作用的写方法；参数 m 为 HTTP 方法名；写方法返回 true，否则返回 false，供 CSRF 与审计中间件调用。
func isMutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

// requestPathWithQuery 拼接请求路径与原始 Query 查询串；参数 c 为 Gin 上下文；返回规范化路径文本，供签名验签中间件调用。
func requestPathWithQuery(c *gin.Context) string {
	if c.Request.URL.RawQuery != "" {
		return c.Request.URL.Path + "?" + c.Request.URL.RawQuery
	}
	return c.Request.URL.Path
}

// readAndRestoreBody 按字节上限读取请求体并重新填回 NopCloser 以便后续控制器继续绑定；参数 c 为 Gin 上下文，maxBytes 为允许上限；返回原始字节与读取错误。
func readAndRestoreBody(c *gin.Context, maxBytes int64) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	limited := http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}

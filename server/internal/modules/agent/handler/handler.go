// Package handler 实现 Agent 管理端令牌签发/吊销接口、开发 Agent 注册/CSR 续期/WSS 通道，以及更新 Agent 注册/心跳/30 秒长轮询接口。
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"autoship/internal/config"
	"autoship/internal/modules/agent/model"
	"autoship/internal/modules/agent/service"
	"autoship/pkg/envelope"
	"autoship/pkg/pki"
	"autoship/pkg/response"
)

// DevEventConsumer 抽象接收开发 Agent 同步回来的任务状态、方案、门禁、活动流与提问事件的业务处理器。
type DevEventConsumer interface {
	// HandleDevEnvelope 处理开发 Agent 经 WSS 或 HTTP 上报的一条事件信封。
	HandleDevEnvelope(agentID uint64, msg envelope.Message) error
}

// Handler 封装 Agent 模块的所有管理接口与机器通信接口。
type Handler struct {
	svc      *service.Service
	cfg      *config.ServerConfig
	ca       *pki.Bundle
	consumer DevEventConsumer
	upgrader websocket.Upgrader
}

// New 创建 Agent 模块 HTTP/WSS 控制器。
// 参数 svc 为 Agent 服务，cfg 为配置，ca 为根 CA Bundle；返回 *Handler。
func New(svc *service.Service, cfg *config.ServerConfig, ca *pki.Bundle) *Handler {
	return &Handler{
		svc: svc,
		cfg: cfg,
		ca:  ca,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  16384,
			WriteBufferSize: 16384,
			CheckOrigin:     func(_ *http.Request) bool { return true },
		},
	}
}

// SetDevEventConsumer 注入开发任务与运行记录同步处理器。
// 参数 c 为实现 DevEventConsumer 的服务适配器；无返回值。
func (h *Handler) SetDevEventConsumer(c DevEventConsumer) {
	h.consumer = c
}

// ListAgents 管理端查询全部 Agent 及其在线状态。
// 参数 c 为 Gin 请求上下文；输出 Agent 数组 JSON。
func (h *Handler) ListAgents(c *gin.Context) {
	list, err := h.svc.ListWithOnlineStatus()
	if err != nil {
		response.ServerError(c, "查询 Agent 列表失败")
		return
	}
	response.OK(c, list)
}

// CreateToken 管理端新建 Agent 并生成 15 分钟有效的一次性注册令牌。
// 参数 c 为 Gin 请求上下文；输出 agent、registrationToken、expiresAt、caFingerprint。
func (h *Handler) CreateToken(c *gin.Context) {
	var in service.CreateTokenInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "参数格式非法")
		return
	}
	ag, token, exp, err := h.svc.CreateRegistrationToken(in)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	fp := ""
	if h.ca != nil {
		fp = h.ca.CAFingerprint
	}
	response.OK(c, gin.H{
		"agent":             ag,
		"registrationToken": token,
		"expiresAt":         exp,
		"caFingerprint":     fp,
	})
}

// Revoke 管理端立即吊销指定 Agent 的访问权限。
// 参数 c 为 Gin 请求上下文；输出吊销成功状态。
func (h *Handler) Revoke(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.Revoke(id); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"revoked": true})
}

// RegisterDev 处理开发 Agent（Mac）首次启动时提交一次性令牌、Ed25519 公钥及 CSR 的注册请求。
// 参数 c 为 Gin 请求上下文；返回 agentId、serverSignPubB64、certChainPEM、caCertPEM。
func (h *Handler) RegisterDev(c *gin.Context) {
	var in service.RegisterDevInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "开发 Agent 注册参数格式非法")
		return
	}
	ag, certChain, caPEM, err := h.svc.RegisterDevAgent(in)
	if err != nil {
		response.Unauthorized(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"agentId":          ag.ID,
		"serverSignPubB64": h.cfg.ServerSignPubB64,
		"certChainPEM":     certChain,
		"caCertPEM":        caPEM,
	})
}

// RenewDevCert 处理已认证开发 Agent 的 TLS 证书 CSR 续期请求。
// 参数 c 为 Gin 请求上下文；返回新签发的 certChainPEM。
func (h *Handler) RenewDevCert(c *gin.Context) {
	ag := currentAgent(c)
	if ag == nil {
		response.Unauthorized(c, "未认证的开发 Agent")
		return
	}
	var req struct {
		TailnetIP string `json:"tailnetIP"`
		CSRPEM    string `json:"csrPEM"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CSRPEM == "" {
		response.BadRequest(c, "缺少有效的 CSR 内容")
		return
	}
	chain, err := h.svc.RenewDevCert(ag, req.TailnetIP, req.CSRPEM)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"certChainPEM": chain})
}

// HandleDevWSS 将已验签的开发 Agent 连接升级为单流 WSS 长连接（7.2 节：15 秒心跳，不做多路复用，断线按 ack 续传）。
// 参数 c 为 Gin 请求上下文；持续接收进度与心跳事件，直至对端断开。
func (h *Handler) HandleDevWSS(c *gin.Context) {
	ag := currentAgent(c)
	if ag == nil {
		response.Unauthorized(c, "未认证的开发 Agent")
		return
	}
	ackSeq, _ := strconv.ParseUint(c.DefaultQuery("ack", "0"), 10, 64)
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	detach, _ := h.svc.AttachDevWSS(ag, ackSeq, conn)
	defer detach()

	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		return nil
	})

	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		var msg envelope.Message
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		h.dispatchDevIncoming(ag, msg)
	}
}

// SyncDevEvents 允许开发 Agent 通过批量签名 POST 请求补传离线缓存发件箱事件。
// 参数 c 为 Gin 请求上下文；逐条幂等消费并返回最新确认序号。
func (h *Handler) SyncDevEvents(c *gin.Context) {
	ag := currentAgent(c)
	if ag == nil {
		response.Unauthorized(c, "未认证的开发 Agent")
		return
	}
	var req struct {
		Events []envelope.Message `json:"events"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "事件批量载荷格式非法")
		return
	}
	var maxSeq uint64
	for _, ev := range req.Events {
		h.dispatchDevIncoming(ag, ev)
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	response.OK(c, gin.H{"ackedSeq": maxSeq})
}

// RegisterUpdate 处理产品服务器更新 Agent 首次启动登记公钥请求。
// 参数 c 为 Gin 请求上下文；返回 agentId、targetId、productCode、serverSignPubB64。
func (h *Handler) RegisterUpdate(c *gin.Context) {
	var in service.RegisterUpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "更新 Agent 注册参数非法")
		return
	}
	ag, err := h.svc.RegisterUpdateAgent(in, c.ClientIP())
	if err != nil {
		response.Unauthorized(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"agentId":          ag.ID,
		"targetId":         ag.DeployTargetID,
		"productCode":      ag.ProductCode,
		"serverSignPubB64": h.cfg.ServerSignPubB64,
	})
}

// UpdateHeartbeat 处理更新 Agent 每 30 秒发送的状态与备份空间心跳。
// 参数 c 为 Gin 请求上下文；返回 ok=true。
func (h *Handler) UpdateHeartbeat(c *gin.Context) {
	ag := currentAgent(c)
	if ag == nil {
		response.Unauthorized(c, "未认证的更新 Agent")
		return
	}
	var in service.HeartbeatInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.BadRequest(c, "心跳参数格式非法")
		return
	}
	if err := h.svc.RecordHeartbeat(ag, in); err != nil {
		response.ServerError(c, "记录心跳失败")
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// PollUpdateCommands 处理更新 Agent 挂起 30 秒的 HTTPS 长轮询拉取请求。
// 参数 c 为 Gin 请求上下文；返回待执行的统一信封指令数组。
func (h *Handler) PollUpdateCommands(c *gin.Context) {
	ag := currentAgent(c)
	if ag == nil {
		response.Unauthorized(c, "未认证的更新 Agent")
		return
	}
	ackSeq, _ := strconv.ParseUint(c.DefaultQuery("ack", "0"), 10, 64)
	waitSec := 30
	if customWait, err := strconv.Atoi(c.Query("waitSec")); err == nil && customWait >= 0 && customWait <= 30 {
		waitSec = customWait
	}
	cmds, err := h.svc.PollCommands(ag.ID, ackSeq, time.Duration(waitSec)*time.Second)
	if err != nil {
		response.ServerError(c, "拉取指令失败")
		return
	}
	response.OK(c, gin.H{"commands": cmds})
}

// dispatchDevIncoming 分发开发 Agent 经 WSS 或批量同步发来的单条信封消息（区分心跳、指令回执与业务研发进度事件）；参数 ag 为开发 Agent，msg 为信封对象。
func (h *Handler) dispatchDevIncoming(ag *model.Agent, msg envelope.Message) {
	switch msg.Type {
	case "evt.heartbeat":
		var hb service.HeartbeatInput
		_ = json.Unmarshal(msg.Payload, &hb)
		_ = h.svc.RecordHeartbeat(ag, hb)
	case "evt.command_ack":
		var ack struct {
			CommandID string `json:"commandId"`
			Status    string `json:"status"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(msg.Payload, &ack) == nil && ack.CommandID != "" {
			_ = h.svc.MarkCommandStatus(ack.CommandID, ack.Status, ack.Error)
		}
	default:
		now := time.Now()
		ag.LastHeartbeatAt = &now
		_ = h.svc.RecordHeartbeat(ag, service.HeartbeatInput{})
		if h.consumer != nil {
			_ = h.consumer.HandleDevEnvelope(ag.ID, msg)
		}
	}
}

// currentAgent 从 Gin 上下文读取已通过 Ed25519 鉴权中间件校验的 Agent 实体指针；参数 c 为请求上下文；返回 *model.Agent 或 nil。
func currentAgent(c *gin.Context) *model.Agent {
	v, ok := c.Get("autoship.agent")
	if !ok {
		return nil
	}
	ag, _ := v.(*model.Agent)
	return ag
}

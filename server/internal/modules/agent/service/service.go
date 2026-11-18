// Package service 实现 Agent 15 分钟一次性注册令牌、Ed25519 请求鉴权与范围控制、CSR 证书签发、WSS 实时会话与 30 秒长轮询唤醒中心。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"autoship/internal/config"
	"autoship/internal/modules/agent/model"
	"autoship/internal/modules/agent/repository"
	integsvc "autoship/internal/modules/integration/service"
	notifySvc "autoship/internal/modules/notify/service"
	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/pki"
)

// TargetLookup 抽象部署目标信息读取接口，避免与 deploy 模块循环引用。
type TargetLookup interface {
	// GetTargetBasic 读取指定部署目标的产品 ID、产品编码、备份配额字节上限及当前备份超限状态。
	GetTargetBasic(targetID uint64) (productID uint64, productCode string, backupQuotaBytes int64, exists bool, err error)
}

// Service 封装开发 Agent 与更新 Agent 的全生命周期认证、在线判定与指令投递中枢。
type Service struct {
	repo         *repository.Repository
	cfg          *config.ServerConfig
	ca           *pki.Bundle
	notifier     *notifySvc.Notifier
	targetLookup TargetLookup

	mu             sync.RWMutex
	seenNonces     map[string]time.Time
	devWSSConns    map[uint64]*wsClient
	pollWaiters    map[uint64][]chan struct{}
	backupAlertMap map[uint64]time.Time
}

// wsClient 封装带并发写互斥锁的单条开发 Agent WSS 连接。
type wsClient struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

// writeJSON 以线程安全且带 10 秒写超时的方式向开发 Agent WSS 连接写入 JSON 帧；参数 v 为待序列化结构体；返回网络写入错误。
func (w *wsClient) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteJSON(v)
}

// New 创建 Agent 管理与通信中枢服务。
// 参数 repo 为 Agent 仓储，cfg 为配置，ca 为根 CA Bundle，notifier 为飞书通知器；返回 *Service。
func New(repo *repository.Repository, cfg *config.ServerConfig, ca *pki.Bundle, notifier *notifySvc.Notifier) *Service {
	return &Service{
		repo:           repo,
		cfg:            cfg,
		ca:             ca,
		notifier:       notifier,
		seenNonces:     make(map[string]time.Time),
		devWSSConns:    make(map[uint64]*wsClient),
		pollWaiters:    make(map[uint64][]chan struct{}),
		backupAlertMap: make(map[uint64]time.Time),
	}
}

// SetTargetLookup 注入部署目标查询适配器（在组装路由阶段调用）。
// 参数 tl 为实现 TargetLookup 的服务；无返回值。
func (s *Service) SetTargetLookup(tl TargetLookup) {
	s.targetLookup = tl
}

// CreateTokenInput 定义后台新建待注册 Agent 的输入参数。
type CreateTokenInput struct {
	Name           string `json:"name"`
	AgentType      string `json:"agentType"` // dev | update
	DeployTargetID uint64 `json:"deployTargetId"`
	AllowedIPsCSV  string `json:"allowedIpsCsv"`
}

// CreateRegistrationToken 创建一条待注册 Agent 记录并生成 15 分钟有效的一次性注册令牌（7.3 第 1 步）。
// 参数 in 为创建参数；返回新建的 Agent 对象、一次性明文令牌、过期时间及错误。
func (s *Service) CreateRegistrationToken(in CreateTokenInput) (*model.Agent, string, time.Time, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", time.Time{}, errors.New("Agent 名称不能为空")
	}
	if in.AgentType != "dev" && in.AgentType != "update" {
		return nil, "", time.Time{}, errors.New("agentType 必须是 dev 或 update")
	}
	var prodID uint64
	var prodCode string
	if in.AgentType == "update" {
		if in.DeployTargetID == 0 {
			return nil, "", time.Time{}, errors.New("更新 Agent 必须绑定一个具体的部署目标")
		}
		if s.targetLookup != nil {
			pid, pcode, _, ok, err := s.targetLookup.GetTargetBasic(in.DeployTargetID)
			if err != nil || !ok {
				return nil, "", time.Time{}, errors.New("指定的部署目标不存在")
			}
			prodID = pid
			prodCode = pcode
		}
	}

	plainToken, err := crypto.GenerateRandomHex(24)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	exp := time.Now().Add(15 * time.Minute)
	ag := &model.Agent{
		Name:              name,
		AgentType:         in.AgentType,
		DeployTargetID:    in.DeployTargetID,
		ProductID:         prodID,
		ProductCode:       prodCode,
		AllowedIPsCSV:     strings.TrimSpace(in.AllowedIPsCSV),
		RegTokenHash:      hashRegToken(plainToken),
		RegTokenExpiresAt: &exp,
		Status:            "pending",
	}
	if err := s.repo.CreateAgent(ag); err != nil {
		return nil, "", time.Time{}, err
	}
	return ag, "asreg_" + plainToken, exp, nil
}

// RegisterDevInput 定义开发 Agent 首次登记公钥并申请 CSR 证书的载荷。
type RegisterDevInput struct {
	Token           string `json:"token"`
	PublicKeyB64    string `json:"publicKeyB64"`
	AgentVersion    string `json:"agentVersion"`
	TailnetIP       string `json:"tailnetIP"`
	RelayListenAddr string `json:"relayListenAddr"`
	CSRPEM          string `json:"csrPEM"`
}

// RegisterDevAgent 校验 15 分钟一次性令牌，登记开发 Agent 的 Ed25519 公钥，并用根 CA 签发其 CSR 证书（供模型中继与本地预览 HTTPS 使用）。
// 参数 in 为注册请求；返回激活后的 Agent、签发的证书链 PEM、根 CA PEM 及错误。
func (s *Service) RegisterDevAgent(in RegisterDevInput) (*model.Agent, string, string, error) {
	ag, err := s.consumeRegistrationToken(in.Token, "dev")
	if err != nil {
		return nil, "", "", err
	}
	if _, err := crypto.ParseEd25519PublicKey(in.PublicKeyB64); err != nil {
		return nil, "", "", err
	}
	now := time.Now()
	ag.PublicKeyB64 = strings.TrimSpace(in.PublicKeyB64)
	ag.AgentVersion = strings.TrimSpace(in.AgentVersion)
	ag.TailnetIP = strings.TrimSpace(in.TailnetIP)
	ag.RelayListenAddr = strings.TrimSpace(in.RelayListenAddr)
	ag.RegTokenHash = ""
	ag.RegTokenExpiresAt = nil
	ag.Status = "active"
	ag.RegisteredAt = &now
	ag.LastHeartbeatAt = &now

	var signedChain string
	if strings.TrimSpace(in.CSRPEM) != "" && s.ca != nil {
		sanIPs := []string{"127.0.0.1"}
		if ag.TailnetIP != "" {
			sanIPs = append(sanIPs, ag.TailnetIP)
		}
		chainBytes, signErr := s.ca.SignCSR([]byte(in.CSRPEM), sanIPs, 365)
		if signErr != nil {
			return nil, "", "", signErr
		}
		signedChain = string(chainBytes)
	}
	if err := s.repo.SaveAgent(ag); err != nil {
		return nil, "", "", err
	}
	caPEM := ""
	if s.ca != nil {
		caPEM = string(s.ca.CACertPEM)
	}
	return ag, signedChain, caPEM, nil
}

// RenewDevCert 为已鉴权的开发 Agent 续签 CSR 证书链。
// 参数 ag 为已通过 Ed25519 鉴权的开发 Agent，tailnetIP 为最新 tailnet IP，csrPEM 为 PEM 编码 CSR；返回完整证书链字符串与错误。
func (s *Service) RenewDevCert(ag *model.Agent, tailnetIP, csrPEM string) (string, error) {
	if s.ca == nil {
		return "", errors.New("PKI 尚未初始化")
	}
	sanIPs := []string{"127.0.0.1"}
	if strings.TrimSpace(tailnetIP) != "" {
		ag.TailnetIP = strings.TrimSpace(tailnetIP)
		_ = s.repo.SaveAgent(ag)
	}
	if ag.TailnetIP != "" {
		sanIPs = append(sanIPs, ag.TailnetIP)
	}
	chain, err := s.ca.SignCSR([]byte(csrPEM), sanIPs, 365)
	if err != nil {
		return "", err
	}
	return string(chain), nil
}

// RegisterUpdateInput 定义产品服务器更新 Agent 首次登记公钥的载荷。
type RegisterUpdateInput struct {
	Token        string `json:"token"`
	PublicKeyB64 string `json:"publicKeyB64"`
	AgentVersion string `json:"agentVersion"`
}

// RegisterUpdateAgent 校验一次性令牌并绑定更新 Agent 的 Ed25519 公钥（严格限定在其关联的 deployTargetId 范围内）。
// 参数 in 为注册请求，clientIP 为来源公网 IP；成功返回激活的 *model.Agent 及错误。
func (s *Service) RegisterUpdateAgent(in RegisterUpdateInput, clientIP string) (*model.Agent, error) {
	ag, err := s.consumeRegistrationToken(in.Token, "update")
	if err != nil {
		return nil, err
	}
	if ag.AllowedIPsCSV != "" && !integsvc.MatchAllowedIPs(clientIP, ag.AllowedIPsCSV) {
		return nil, errors.New("来源 IP 不在该更新 Agent 允许白名单内")
	}
	if _, err := crypto.ParseEd25519PublicKey(in.PublicKeyB64); err != nil {
		return nil, err
	}
	now := time.Now()
	ag.PublicKeyB64 = strings.TrimSpace(in.PublicKeyB64)
	ag.AgentVersion = strings.TrimSpace(in.AgentVersion)
	ag.RegTokenHash = ""
	ag.RegTokenExpiresAt = nil
	ag.Status = "active"
	ag.RegisteredAt = &now
	ag.LastHeartbeatAt = &now
	if err := s.repo.SaveAgent(ag); err != nil {
		return nil, err
	}
	return ag, nil
}

// VerifyAgentRequest 校验 Agent 请求的 Ed25519 签名、5 分钟时间窗、Nonce 防重放、吊销状态及角色类型边界。
// 参数 agentID 为请求头声明 ID，expectedType 为期望类型（dev 或 update），ts/nonce/sigB64 为签名头，method/pathWithQuery/body 为请求原文，clientIP 为来源 IP；通过返回 *model.Agent，失败返回错误。
func (s *Service) VerifyAgentRequest(agentID uint64, expectedType, ts, nonce, sigB64, method, pathWithQuery string, body []byte, clientIP string) (*model.Agent, error) {
	ag, err := s.repo.GetByID(agentID)
	if err != nil {
		return nil, err
	}
	if ag == nil || ag.Status != "active" || ag.AgentType != expectedType {
		return nil, errors.New("Agent 未注册、类型不匹配或已被吊销")
	}
	if expectedType == "update" && ag.AllowedIPsCSV != "" && !integsvc.MatchAllowedIPs(clientIP, ag.AllowedIPsCSV) {
		return nil, errors.New("更新 Agent 来源 IP 不在白名单内")
	}
	now := time.Now()
	if err := crypto.VerifyHTTPRequestEd25519(ag.PublicKeyB64, method, pathWithQuery, ts, nonce, body, sigB64, now, 5*time.Minute); err != nil {
		return nil, err
	}
	if !s.consumeNonce(fmt.Sprintf("%d:%s", ag.ID, nonce), now) {
		return nil, errors.New("检测到重复 Agent Nonce 重放")
	}
	return ag, nil
}

// HeartbeatInput 定义 Agent 心跳上报载荷。
type HeartbeatInput struct {
	AgentVersion    string `json:"agentVersion"`
	ProductVersion  string `json:"productVersion"`
	ServiceStatus   string `json:"serviceStatus"`
	RelayListenAddr string `json:"relayListenAddr"`
	TailnetIP       string `json:"tailnetIP"`
	DiskFreeBytes   int64  `json:"diskFreeBytes"`
	DiskTotalBytes  int64  `json:"diskTotalBytes"`
	BackupUsedBytes int64  `json:"backupUsedBytes"`
}

// RecordHeartbeat 更新 Agent 在线心跳与资源状态；若更新 Agent 备份目录占用超过目标存储上限，触发节流后的飞书告警（9.5 节）。
// 参数 ag 为已鉴权 Agent，in 为心跳参数；返回保存错误。
func (s *Service) RecordHeartbeat(ag *model.Agent, in HeartbeatInput) error {
	now := time.Now()
	if in.AgentVersion != "" {
		ag.AgentVersion = in.AgentVersion
	}
	if in.ProductVersion != "" {
		ag.ProductVersion = in.ProductVersion
	}
	if in.ServiceStatus != "" {
		ag.ServiceStatus = in.ServiceStatus
	}
	if in.RelayListenAddr != "" {
		ag.RelayListenAddr = in.RelayListenAddr
	}
	if in.TailnetIP != "" {
		ag.TailnetIP = in.TailnetIP
	}
	ag.DiskFreeBytes = in.DiskFreeBytes
	ag.DiskTotalBytes = in.DiskTotalBytes
	ag.BackupUsedBytes = in.BackupUsedBytes
	ag.LastHeartbeatAt = &now
	if err := s.repo.SaveAgent(ag); err != nil {
		return err
	}

	if ag.AgentType == "update" && ag.DeployTargetID > 0 && s.targetLookup != nil && in.BackupUsedBytes > 0 {
		_, pcode, quotaBytes, ok, _ := s.targetLookup.GetTargetBasic(ag.DeployTargetID)
		if ok && quotaBytes > 0 && in.BackupUsedBytes > quotaBytes {
			s.maybeAlertBackupQuota(ag.DeployTargetID, pcode, in.BackupUsedBytes, quotaBytes)
		}
	}
	return nil
}

// ListWithOnlineStatus 查询全部 Agent 并根据最近心跳与活跃 WSS 连接计算实时在线标志（开发 Agent 45s 内心跳或 WSS 连接在线；更新 Agent 90s 内心跳在线）。
// 无入参；返回填充 Online 字段的 Agent 切片与错误。
func (s *Service) ListWithOnlineStatus() ([]model.Agent, error) {
	list, err := s.repo.ListAgents()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range list {
		list[i].Online = s.IsAgentOnline(&list[i], now)
	}
	return list, nil
}

// IsAgentOnline 判断单个 Agent 当前是否处于在线状态。
// 参数 ag 为 Agent 模型指针，now 为当前时间；在线返回 true，否则返回 false。
func (s *Service) IsAgentOnline(ag *model.Agent, now time.Time) bool {
	if ag == nil || ag.Status != "active" {
		return false
	}
	if ag.AgentType == "dev" {
		s.mu.RLock()
		_, hasWS := s.devWSSConns[ag.ID]
		s.mu.RUnlock()
		if hasWS {
			return true
		}
		return ag.LastHeartbeatAt != nil && now.Sub(*ag.LastHeartbeatAt) <= 45*time.Second
	}
	return ag.LastHeartbeatAt != nil && now.Sub(*ag.LastHeartbeatAt) <= 90*time.Second
}

// ActiveRelayEndpoint 查找当前在线且配置了模型中继地址的开发 Agent。
// 无入参；返回中继 HTTPS 基地址（如 "https://100.64.0.2:9445"）、是否找到在线中继。
func (s *Service) ActiveRelayEndpoint() (string, bool) {
	devs, err := s.repo.FindActiveDevAgents()
	if err != nil {
		return "", false
	}
	now := time.Now()
	for _, d := range devs {
		if s.IsAgentOnline(&d, now) && strings.TrimSpace(d.RelayListenAddr) != "" {
			addr := strings.TrimSpace(d.RelayListenAddr)
			if !strings.HasPrefix(addr, "https://") {
				addr = "https://" + addr
			}
			return strings.TrimRight(addr, "/"), true
		}
	}
	return "", false
}

// Revoke 立即吊销指定 Agent 的访问凭据并断开其活跃 WSS 连接（7.3 第 5 步）。
// 参数 agentID 为目标 Agent 主键；返回错误。
func (s *Service) Revoke(agentID uint64) error {
	ag, err := s.repo.GetByID(agentID)
	if err != nil || ag == nil {
		return errors.New("Agent 不存在")
	}
	ag.Status = "revoked"
	ag.RegTokenHash = ""
	if err := s.repo.SaveAgent(ag); err != nil {
		return err
	}
	s.mu.Lock()
	if ws, ok := s.devWSSConns[agentID]; ok {
		_ = ws.conn.Close()
		delete(s.devWSSConns, agentID)
	}
	s.mu.Unlock()
	return nil
}

// DispatchToDevAgent 将指令持久化入队到首个激活的开发 Agent，若其 WSS 在线则立即实时推送。
// 参数 cmdType 为消息类型，payload 为指令结构体，ttl 为有效时长；返回生成的指令 UID、目标 AgentID 及错误。
func (s *Service) DispatchToDevAgent(cmdType string, payload any, ttl time.Duration) (string, uint64, error) {
	devs, err := s.repo.FindActiveDevAgents()
	if err != nil {
		return "", 0, err
	}
	if len(devs) == 0 {
		return "", 0, errors.New("当前尚未注册任何可用的开发 Agent")
	}
	// 优先选择在线的开发 Agent；若暂时离线则入队到第一个激活的开发 Agent，待其上线续传
	chosen := devs[0]
	now := time.Now()
	for _, d := range devs {
		if s.IsAgentOnline(&d, now) {
			chosen = d
			break
		}
	}
	uid, err := s.EnqueueForAgent(chosen.ID, cmdType, payload, ttl)
	return uid, chosen.ID, err
}

// DispatchToTargetUpdateAgent 将部署/回滚/备份指令投递给绑定指定部署目标的激活更新 Agent，并立即唤醒其长轮询协程。
// 参数 targetID 为部署目标主键，cmdType 为指令类型，payload 为载荷，ttl 为有效期；返回指令 UID 及错误。
func (s *Service) DispatchToTargetUpdateAgent(targetID uint64, cmdType string, payload any, ttl time.Duration) (string, error) {
	ag, err := s.repo.FindActiveUpdateAgentByTarget(targetID)
	if err != nil {
		return "", err
	}
	if ag == nil {
		return "", errors.New("该部署目标尚未绑定已激活的更新 Agent")
	}
	return s.EnqueueForAgent(ag.ID, cmdType, payload, ttl)
}

// EnqueueForAgent 为指定 Agent 写入持久化指令并触发 WSS 实时发送或长轮询唤醒。
// 参数 agentID 为接收方 ID，cmdType 为类型，payload 为可序列化对象，ttl 为过期时长；返回指令 UID 与错误。
func (s *Service) EnqueueForAgent(agentID uint64, cmdType string, payload any, ttl time.Duration) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	uid := "cmd_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cmd := &model.AgentCommand{
		CommandUID:  uid,
		AgentID:     agentID,
		CommandType: cmdType,
		PayloadJSON: string(raw),
		Status:      "pending",
		ExpiresAt:   time.Now().Add(ttl),
	}
	if err := s.repo.EnqueueCommand(cmd); err != nil {
		return "", err
	}

	// 若是开发 Agent 且当前持有活跃 WSS 连接，立即推送统一信封帧
	s.mu.RLock()
	ws := s.devWSSConns[agentID]
	s.mu.RUnlock()
	if ws != nil {
		msg := envelope.Message{
			ID:      cmd.CommandUID,
			Type:    cmd.CommandType,
			Seq:     cmd.ID,
			TS:      time.Now().Unix(),
			Payload: json.RawMessage(cmd.PayloadJSON),
		}
		if writeErr := ws.writeJSON(msg); writeErr == nil {
			now := time.Now()
			cmd.Status = "delivered"
			cmd.DeliveredAt = &now
		}
	}

	// 唤醒所有正在挂起等待该 Agent 指令的 30 秒长轮询请求
	s.notifyPollers(agentID)
	return uid, nil
}

// PollCommands 为更新 Agent 执行最长 waitDuration（30 秒）的长轮询拉取：有待发指令立即返回，否则阻塞等待新指令信号或超时。
// 参数 agentID 为更新 Agent ID，ackSeq 为客户端最后确认的序号，waitDuration 为挂起时长；返回统一消息信封数组与错误。
func (s *Service) PollCommands(agentID, ackSeq uint64, waitDuration time.Duration) ([]envelope.Message, error) {
	cmds, err := s.repo.FetchPendingCommands(agentID, ackSeq, time.Now())
	if err != nil {
		return nil, err
	}
	if len(cmds) > 0 || waitDuration <= 0 {
		return toEnvelopeMessages(cmds), nil
	}

	ch := s.registerPoller(agentID)
	defer s.unregisterPoller(agentID, ch)

	timer := time.NewTimer(waitDuration)
	defer timer.Stop()

	select {
	case <-ch:
		cmds, err = s.repo.FetchPendingCommands(agentID, ackSeq, time.Now())
		if err != nil {
			return nil, err
		}
		return toEnvelopeMessages(cmds), nil
	case <-timer.C:
		return []envelope.Message{}, nil
	}
}

// MarkCommandStatus 更新指定指令 UID 的执行状态。
// 参数 commandUID 为唯一指令 ID，status 为 running/succeeded/failed，errMsg 为错误信息；返回错误。
func (s *Service) MarkCommandStatus(commandUID, status, errMsg string) error {
	return s.repo.UpdateCommandStatus(commandUID, status, errMsg)
}

// AttachDevWSS 登记开发 Agent 新建立的 WSS 连接，补发断线期间积压的指令，并返回解绑清理回调。
// 参数 ag 为开发 Agent，ackSeq 为断线前最后确认的指令序号，conn 为升级后的 WebSocket 连接；返回解绑函数与补发错误。
func (s *Service) AttachDevWSS(ag *model.Agent, ackSeq uint64, conn *websocket.Conn) (func(), error) {
	client := &wsClient{conn: conn}
	s.mu.Lock()
	if old, ok := s.devWSSConns[ag.ID]; ok {
		_ = old.conn.Close()
	}
	s.devWSSConns[ag.ID] = client
	s.mu.Unlock()

	pending, err := s.repo.FetchPendingCommands(ag.ID, ackSeq, time.Now())
	if err == nil {
		for _, m := range toEnvelopeMessages(pending) {
			_ = client.writeJSON(m)
		}
	}

	detach := func() {
		s.mu.Lock()
		if cur, ok := s.devWSSConns[ag.ID]; ok && cur == client {
			delete(s.devWSSConns, ag.ID)
		}
		s.mu.Unlock()
	}
	return detach, nil
}

// AllWhitelistedIPs 汇总全部已激活或待注册更新 Agent 登记的固定来源 IP/CIDR 列表（供公网监听器白名单校验）。
// 无入参；返回 IP/CIDR 字符串切片。
func (s *Service) AllWhitelistedIPs() []string {
	list, err := s.repo.ListAgents()
	if err != nil {
		return nil
	}
	var out []string
	for _, ag := range list {
		if ag.AgentType != "update" || ag.Status == "revoked" || strings.TrimSpace(ag.AllowedIPsCSV) == "" {
			continue
		}
		for _, seg := range strings.Split(ag.AllowedIPsCSV, ",") {
			if t := strings.TrimSpace(seg); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// consumeRegistrationToken 校验并匹配 15 分钟有效期内的一次性 Agent 注册令牌；参数 rawToken 为明文令牌，expectedType 为期望类型；返回待激活 *model.Agent 与错误。
func (s *Service) consumeRegistrationToken(rawToken, expectedType string) (*model.Agent, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(rawToken), "asreg_")
	if trimmed == "" {
		return nil, errors.New("注册令牌不能为空")
	}
	ag, err := s.repo.FindByRegTokenHash(hashRegToken(trimmed))
	if err != nil {
		return nil, err
	}
	if ag == nil || ag.AgentType != expectedType {
		return nil, errors.New("注册令牌无效或已被使用")
	}
	if ag.RegTokenExpiresAt == nil || time.Now().After(*ag.RegTokenExpiresAt) {
		return nil, errors.New("一次性注册令牌已超过 15 分钟有效期")
	}
	return ag, nil
}

// consumeNonce 原子记录并拦截 6 分钟内的重复 Agent 请求 Nonce；参数 key 为 AgentID 与 Nonce 复合键，now 为当前时刻；首次出现返回 true，重放返回 false。
func (s *Service) consumeNonce(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.seenNonces {
		if now.After(exp) {
			delete(s.seenNonces, k)
		}
	}
	if exp, exists := s.seenNonces[key]; exists && now.Before(exp) {
		return false
	}
	s.seenNonces[key] = now.Add(6 * time.Minute)
	return true
}

// registerPoller 为指定更新 Agent 注册一个长轮询唤醒缓冲通道；参数 agentID 为更新 Agent ID；返回唤醒只读信号通道。
func (s *Service) registerPoller(agentID uint64) chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.pollWaiters[agentID] = append(s.pollWaiters[agentID], ch)
	s.mu.Unlock()
	return ch
}

// unregisterPoller 在长轮询返回前注销指定等待通道；参数 agentID 为更新 Agent ID，target 为待移除通道。
func (s *Service) unregisterPoller(agentID uint64, target chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.pollWaiters[agentID]
	filtered := waiters[:0]
	for _, ch := range waiters {
		if ch != target {
			filtered = append(filtered, ch)
		}
	}
	s.pollWaiters[agentID] = filtered
}

// notifyPollers 非阻塞唤醒指定 Agent 下所有正在挂起等待的长轮询请求；参数 agentID 为目标 Agent ID。
func (s *Service) notifyPollers(agentID uint64) {
	s.mu.RLock()
	waiters := append([]chan struct{}(nil), s.pollWaiters[agentID]...)
	s.mu.RUnlock()
	for _, ch := range waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// maybeAlertBackupQuota 按每目标 6 小时节流间隔发送数据库备份空间超限的飞书通知；参数 targetID 为目标 ID，productCode 为产品编码，used/quota 为当前占用与上限字节数。
func (s *Service) maybeAlertBackupQuota(targetID uint64, productCode string, used, quota int64) {
	s.mu.Lock()
	last := s.backupAlertMap[targetID]
	if time.Since(last) < 6*time.Hour {
		s.mu.Unlock()
		return
	}
	s.backupAlertMap[targetID] = time.Now()
	s.mu.Unlock()

	if s.notifier != nil {
		s.notifier.NotifyAsync(notifySvc.EventBackupQuotaExceeded,
			fmt.Sprintf("产品 %s 部署目标 #%d 数据库备份超限", productCode, targetID),
			fmt.Sprintf("当前占用 %.2f MB，已超过配置上限 %.2f MB，请清理历史备份或调高存储上限。",
				float64(used)/(1024*1024), float64(quota)/(1024*1024)),
		)
	}
}

// toEnvelopeMessages 将数据库持久化指令实体切片转换为统一网络信封切片；参数 cmds 为指令列表；返回 []envelope.Message。
func toEnvelopeMessages(cmds []model.AgentCommand) []envelope.Message {
	out := make([]envelope.Message, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, envelope.Message{
			ID:      c.CommandUID,
			Type:    c.CommandType,
			Seq:     c.ID,
			TS:      c.CreatedAt.Unix(),
			Payload: json.RawMessage(c.PayloadJSON),
		})
	}
	return out
}

// hashRegToken 计算一次性注册令牌的带域分离 SHA-256 哈希；参数 plain 为令牌明文；返回 64 字符 hex 摘要。
func hashRegToken(plain string) string {
	sum := sha256.Sum256([]byte("autoship-reg-token-v1:" + plain))
	return hex.EncodeToString(sum[:])
}

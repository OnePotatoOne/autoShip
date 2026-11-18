// Package service 实现接入方密钥生成、来源 IP 白名单汇总校验、Open API 验签防重放与增量事件派发服务。
package service

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"autoship/internal/modules/integration/model"
	"autoship/internal/modules/integration/repository"
	"autoship/pkg/crypto"
)

// Service 封装接入方管理、Open API 鉴权、Nonce 防重放与幂等缓存逻辑。
type Service struct {
	repo       *repository.Repository
	encKey     []byte
	mu         sync.Mutex
	seenNonces map[string]time.Time
	limiters   map[string]*rate.Limiter
}

// New 创建接入方业务服务实例。
// 参数 repo 为接入方仓储，encKey 为 32 字节加密密钥；返回 *Service。
func New(repo *repository.Repository, encKey []byte) *Service {
	return &Service{
		repo:       repo,
		encKey:     encKey,
		seenNonces: make(map[string]time.Time),
		limiters:   make(map[string]*rate.Limiter),
	}
}

// List 查询全部接入方配置。
// 无入参；返回接入方列表与错误。
func (s *Service) List() ([]model.Integration, error) {
	return s.repo.ListIntegrations()
}

// CreateInput 定义创建接入方的请求参数。
type CreateInput struct {
	Name          string   `json:"name"`
	AllowedIPsCSV string   `json:"allowedIpsCsv"`
	RateLimitRPM  int      `json:"rateLimitRpm"`
	Enabled       bool     `json:"enabled"`
	ProductIDs    []uint64 `json:"productIds"`
}

// Create 生成高强度 AppID 与随机 AppSecret（仅本次返回明文一次，入库加密）并保存产品绑定。
// 参数 in 为创建参数；返回接入方实体、一次性明文密钥与错误。
func (s *Service) Create(in CreateInput) (*model.Integration, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", errors.New("接入方名称不能为空")
	}
	appIDHex, err := crypto.GenerateRandomHex(12)
	if err != nil {
		return nil, "", err
	}
	plainSecret, err := crypto.GenerateRandomHex(32)
	if err != nil {
		return nil, "", err
	}
	encSecret, err := crypto.EncryptField(plainSecret, s.encKey)
	if err != nil {
		return nil, "", err
	}
	rpm := in.RateLimitRPM
	if rpm <= 0 {
		rpm = 300
	}
	item := &model.Integration{
		Name:          name,
		AppID:         "as_" + appIDHex,
		AppSecretEnc:  encSecret,
		AllowedIPsCSV: strings.TrimSpace(in.AllowedIPsCSV),
		RateLimitRPM:  rpm,
		Enabled:       in.Enabled,
	}
	if err := s.repo.Create(item, in.ProductIDs); err != nil {
		return nil, "", err
	}
	return item, plainSecret, nil
}

// Update 更新接入方白名单、启停状态及绑定的产品列表。
// 参数 id 为接入方主键，in 为修改内容；返回更新后的 *model.Integration 与错误。
func (s *Service) Update(id uint64, in CreateInput) (*model.Integration, error) {
	existing, err := s.repo.GetByID(id)
	if err != nil || existing == nil {
		return nil, errors.New("接入方不存在")
	}
	if strings.TrimSpace(in.Name) != "" {
		existing.Name = strings.TrimSpace(in.Name)
	}
	existing.AllowedIPsCSV = strings.TrimSpace(in.AllowedIPsCSV)
	if in.RateLimitRPM > 0 {
		existing.RateLimitRPM = in.RateLimitRPM
	}
	existing.Enabled = in.Enabled
	if err := s.repo.Update(existing, in.ProductIDs); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete 删除指定接入方。
// 参数 id 为接入方主键；返回删除错误。
func (s *Service) Delete(id uint64) error {
	return s.repo.Delete(id)
}

// VerifyOpenRequest 对公网 Open API 请求执行来源 IP 白名单、5 分钟时间窗、Nonce 防重放、HMAC-SHA256 验签与速率限制校验。
// 参数 appID/ts/nonce/sigHex 为鉴权请求头，method/pathWithQuery/body 为请求原文，clientIP 为来源 IP；通过返回 *model.Integration，失败返回原因错误。
func (s *Service) VerifyOpenRequest(appID, ts, nonce, sigHex, method, pathWithQuery string, body []byte, clientIP string) (*model.Integration, error) {
	integ, err := s.repo.GetByAppID(strings.TrimSpace(appID))
	if err != nil {
		return nil, err
	}
	if integ == nil || !integ.Enabled {
		return nil, errors.New("接入方不存在或已禁用")
	}
	if !MatchAllowedIPs(clientIP, integ.AllowedIPsCSV) {
		return nil, errors.New("来源 IP 不在该接入方白名单内")
	}
	plainSecret, err := crypto.DecryptField(integ.AppSecretEnc, s.encKey)
	if err != nil {
		return nil, errors.New("读取接入方密钥失败")
	}
	now := time.Now()
	if err := crypto.VerifyOpenAPIRequest(plainSecret, method, pathWithQuery, ts, nonce, body, sigHex, now, 5*time.Minute); err != nil {
		return nil, err
	}
	if !s.consumeNonce(integ.AppID+":"+nonce, now) {
		return nil, errors.New("检测到重复 Nonce 请求重放")
	}
	if !s.allowRate("integ:"+integ.AppID, integ.RateLimitRPM) {
		return nil, errors.New("超出接入方请求频率限制")
	}
	return integ, nil
}

// AllowReporterRate 对单个外部提交人施加独立请求频率限制（默认每分钟 30 次，防止单用户刷爆模型预算）。
// 参数 integrationID 为接入方 ID，externalUID 为外部用户标识；允许返回 true，超限返回 false。
func (s *Service) AllowReporterRate(integrationID uint64, externalUID string) bool {
	if strings.TrimSpace(externalUID) == "" {
		return true
	}
	return s.allowRate("rep:"+string(rune(integrationID))+":"+externalUID, 30)
}

// IsProductBound 校验某产品是否已绑定授权给当前接入方。
// 参数 integ 为已鉴权的接入方对象，productID 为目标产品 ID；已授权返回 true，否则返回 false。
func (s *Service) IsProductBound(integ *model.Integration, productID uint64) bool {
	for _, pid := range integ.ProductIDs {
		if pid == productID {
			return true
		}
	}
	return false
}

// AllWhitelistedIPs 汇总当前所有已启用接入方的白名单 IP/CIDR 字符串列表（供公网监听器顶层白名单判断）。
// 无入参；返回 IP/CIDR 切片。
func (s *Service) AllWhitelistedIPs() []string {
	list, err := s.repo.ListIntegrations()
	if err != nil {
		return nil
	}
	var out []string
	for _, item := range list {
		if !item.Enabled || strings.TrimSpace(item.AllowedIPsCSV) == "" {
			continue
		}
		for _, seg := range strings.Split(item.AllowedIPsCSV, ",") {
			if t := strings.TrimSpace(seg); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// EmitEvent 生成一条增量事件写入 open_events 表。
// 参数 integrationID/productID/issueID 为关联主键，externalUID 为外部提交人 ID，eventType 为事件类型，payload 为任意结构化数据；无返回值。
func (s *Service) EmitEvent(integrationID, productID, issueID uint64, externalUID, eventType string, payload any) {
	if integrationID == 0 {
		return
	}
	raw, _ := json.Marshal(payload)
	_ = s.repo.AppendEvent(&model.OpenEvent{
		IntegrationID: integrationID,
		ProductID:     productID,
		EventType:     eventType,
		IssueID:       issueID,
		ExternalUID:   externalUID,
		PayloadJSON:   string(raw),
	})
}

// PullEvents 增量读取指定游标之后的开放事件。
// 参数 integrationID 为接入方 ID，cursor 为游标，limit 为条数；返回事件切片与错误。
func (s *Service) PullEvents(integrationID, cursor uint64, limit int) ([]model.OpenEvent, error) {
	return s.repo.ListEventsAfter(integrationID, cursor, limit)
}

// CheckIdempotency 查询幂等缓存记录。
// 参数 integrationID 为接入方 ID，idemKey 为请求头幂等键；返回命中的缓存指针或 nil。
func (s *Service) CheckIdempotency(integrationID uint64, idemKey string) (*model.IdempotencyRecord, error) {
	if strings.TrimSpace(idemKey) == "" {
		return nil, nil
	}
	return s.repo.FindIdempotency(integrationID, strings.TrimSpace(idemKey), time.Now())
}

// StoreIdempotency 写入 24 小时有效的写接口幂等响应缓存。
// 参数 integrationID 为接入方 ID，idemKey/method/path 为请求键，statusCode/respBody 为响应快照；无返回值。
func (s *Service) StoreIdempotency(integrationID uint64, idemKey, method, path string, statusCode int, respBody string) {
	if strings.TrimSpace(idemKey) == "" {
		return
	}
	_ = s.repo.SaveIdempotency(&model.IdempotencyRecord{
		IntegrationID: integrationID,
		IdemKey:       strings.TrimSpace(idemKey),
		Method:        method,
		Path:          path,
		StatusCode:    statusCode,
		ResponseJSON:  respBody,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	})
}

// MatchAllowedIPs 判断来源 IP 是否命中逗号分隔的 IP 或 CIDR 白名单（本机回环地址仅在白名单显式写明或为 "*" 开发模式时放行）。
// 参数 clientIP 为请求端 IP 字符串，allowedCSV 为逗号分隔的 IP/CIDR 配置；命中返回 true，未配置或未命中返回 false。
func MatchAllowedIPs(clientIP, allowedCSV string) bool {
	trimmedCSV := strings.TrimSpace(allowedCSV)
	if trimmedCSV == "" {
		return false
	}
	parsedClient := net.ParseIP(strings.TrimSpace(clientIP))
	if parsedClient == nil {
		return false
	}
	for _, item := range strings.Split(trimmedCSV, ",") {
		rule := strings.TrimSpace(item)
		if rule == "" {
			continue
		}
		if rule == "*" {
			return true
		}
		if strings.Contains(rule, "/") {
			if _, cidr, err := net.ParseCIDR(rule); err == nil && cidr.Contains(parsedClient) {
				return true
			}
			continue
		}
		if targetIP := net.ParseIP(rule); targetIP != nil && targetIP.Equal(parsedClient) {
			return true
		}
	}
	return false
}

// consumeNonce 原子记录并校验 Open API 防重放随机数（6 分钟时间窗内重复出现即拒绝）；参数 key 为接入方与 Nonce 复合键，now 为当前时间；首次消费返回 true，重放返回 false。
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

// allowRate 对指定限流桶按每分钟配额 rpm 执行令牌桶判定；参数 bucket 为桶名，rpm 为每分钟允许次数；放行返回 true，超速返回 false。
func (s *Service) allowRate(bucket string, rpm int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rpm <= 0 {
		rpm = 60
	}
	lim, ok := s.limiters[bucket]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(float64(rpm)/60.0), max(5, rpm/6))
		s.limiters[bucket] = lim
	}
	return lim.Allow()
}

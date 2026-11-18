// Package service 实现面向飞书自定义机器人 Webhook 的关键事件通知服务（5.1 节：固定几类事件推送，主动出站只走飞书）。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"autoship/internal/config"
	"autoship/pkg/desensitize"
)

// EventKind 枚举首版支持的固定飞书通知事件类别。
type EventKind string

const (
	// EventNewRequirement 新需求已提交待分拣/排期
	EventNewRequirement EventKind = "new_requirement"
	// EventPlanConfirm 开发任务技术方案与改动范围待确认
	EventPlanConfirm EventKind = "plan_confirm"
	// EventAskOwner 开发 Agent 遇到阻塞或依赖审批请求人工答复
	EventAskOwner EventKind = "ask_owner"
	// EventPendingAcceptance 验收包与预览实例已就绪待验收（附改动范围摘要）
	EventPendingAcceptance EventKind = "pending_acceptance"
	// EventReleaseReady 版本已签名上传，处于待确认部署状态
	EventReleaseReady EventKind = "release_ready"
	// EventPublishFailed 版本发布流水线失败
	EventPublishFailed EventKind = "publish_failed"
	// EventDeployFailed 部署失败但已自动回滚
	EventDeployFailed EventKind = "deploy_failed"
	// EventRollbackFailedUrgent 部署失败且自动回滚也失败（最高优紧急告警）
	EventRollbackFailedUrgent EventKind = "rollback_failed_urgent"
	// EventBackupQuotaExceeded 部署目标数据库备份目录占用超过配置上限
	EventBackupQuotaExceeded EventKind = "backup_quota_exceeded"
)

// Notifier 封装向飞书机器人推送脱敏文本消息的客户端。
type Notifier struct {
	cfg        *config.ServerConfig
	httpClient *http.Client
}

// New 创建绑定配置与超时 HTTP 客户端的飞书通知服务。
// 参数 cfg 为全局配置指针；返回 *Notifier。
func New(cfg *config.ServerConfig) *Notifier {
	return &Notifier{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 8 * time.Second,
		},
	}
}

// Notify 异步或同步发送一条飞书文本通知（当通知未启用或 Webhook 为空时安全跳过）。
// 参数 ctx 为上下文，kind 为事件类型，title 为通知标题，summary 为正文内容；返回发送错误（未启用返回 nil）。
func (n *Notifier) Notify(ctx context.Context, kind EventKind, title, summary string) error {
	if n == nil || n.cfg == nil || !n.cfg.Notify.Enabled {
		return nil
	}
	webhook := strings.TrimSpace(n.cfg.Notify.FeishuWebhookURL)
	if webhook == "" {
		return nil
	}
	prefix := "【autoShip 通知】"
	if kind == EventRollbackFailedUrgent {
		prefix = "🚨【autoShip 紧急告警 · 回滚失败】"
	}
	fullText := fmt.Sprintf("%s %s\n事件类型：%s\n时间：%s\n%s",
		prefix,
		strings.TrimSpace(title),
		string(kind),
		time.Now().Format("2006-01-02 15:04:05"),
		desensitize.MaskSecrets(strings.TrimSpace(summary)),
	)

	payload := map[string]any{
		"msg_type": "text",
		"content": map[string]string{
			"text": fullText,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("推送飞书 Webhook 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("飞书 Webhook 返回异常状态码: %d", resp.StatusCode)
	}
	return nil
}

// NotifyAsync 在独立后台协程中触发飞书推送，避免外部网络抖动阻塞主业务事务。
// 参数 kind 为事件类别，title 为标题，summary 为正文；无返回值。
func (n *Notifier) NotifyAsync(kind EventKind, title, summary string) {
	if n == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = n.Notify(ctx, kind, title, summary)
	}()
}

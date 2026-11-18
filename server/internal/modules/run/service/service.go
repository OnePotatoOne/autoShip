// Package service 提供开发运行活动流脱敏存储与会话引用读取服务。
package service

import (
	"autoship/internal/modules/run/model"
	"autoship/internal/modules/run/repository"
	"autoship/pkg/desensitize"
)

// Service 封装运行活动与会话记录业务方法。
type Service struct {
	repo *repository.Repository
}

// New 创建运行记录业务服务。
// 参数 repo 为运行记录仓储；返回 *Service。
func New(repo *repository.Repository) *Service {
	return &Service{repo: repo}
}

// RecordActivity 对活动详情脱敏后写入 run_activities 表。
// 参数 act 为活动模型指针；返回错误。
func (s *Service) RecordActivity(act *model.RunActivity) error {
	act.Summary = desensitize.MaskSecrets(act.Summary)
	act.DetailJSON = desensitize.MaskSecrets(act.DetailJSON)
	return s.repo.AppendActivity(act)
}

// ListActivities 查询某开发任务的活动流。
// 参数 workItemID 为任务主键；返回活动数组与错误。
func (s *Service) ListActivities(workItemID uint64) ([]model.RunActivity, error) {
	return s.repo.ListActivities(workItemID, 300)
}

// SaveTranscript 保存本机 JSONL 会话引用与 Token 统计。
// 参数 tr 为引用对象指针；返回错误。
func (s *Service) SaveTranscript(tr *model.RunTranscript) error {
	tr.Summary = desensitize.MaskSecrets(tr.Summary)
	return s.repo.SaveTranscript(tr)
}

// ListTranscripts 查询某开发任务的全部会话引用。
// 参数 workItemID 为任务主键；返回引用列表与错误。
func (s *Service) ListTranscripts(workItemID uint64) ([]model.RunTranscript, error) {
	return s.repo.ListTranscripts(workItemID)
}

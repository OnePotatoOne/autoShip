// Package repository 封装 Agent 注册身份与持久化指令队列的数据持久化操作。
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"autoship/internal/modules/agent/model"
)

// Repository 提供 agents 与 agent_commands 表的数据访问层。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建 Agent 模块仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// ListAgents 查询所有 Agent 注册记录。
// 无入参；返回 Agent 列表与查询错误。
func (r *Repository) ListAgents() ([]model.Agent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.Agent
	err := db.Order("id ASC").Find(&list).Error
	return list, err
}

// GetByID 根据主键读取单个 Agent。
// 参数 id 为 Agent 主键；未找到返回 (nil, nil)。
func (r *Repository) GetByID(id uint64) (*model.Agent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var a model.Agent
	err := db.First(&a, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &a, err
}

// FindByRegTokenHash 根据一次性注册令牌哈希查询待激活 Agent。
// 参数 tokenHash 为令牌摘要；未找到返回 (nil, nil)。
func (r *Repository) FindByRegTokenHash(tokenHash string) (*model.Agent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var a model.Agent
	err := db.Where("reg_token_hash = ? AND status = 'pending'", tokenHash).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &a, err
}

// FindActiveUpdateAgentByTarget 查询指定部署目标当前处于激活态的更新 Agent。
// 参数 targetID 为部署目标主键；未找到返回 (nil, nil)。
func (r *Repository) FindActiveUpdateAgentByTarget(targetID uint64) (*model.Agent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var a model.Agent
	err := db.Where("agent_type = 'update' AND deploy_target_id = ? AND status = 'active'", targetID).Order("id DESC").First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &a, err
}

// FindActiveDevAgents 查询所有处于激活态的开发 Agent。
// 无入参；返回开发 Agent 列表与错误。
func (r *Repository) FindActiveDevAgents() ([]model.Agent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.Agent
	err := db.Where("agent_type = 'dev' AND status = 'active'").Order("id ASC").Find(&list).Error
	return list, err
}

// CreateAgent 创建一条待登记 Agent 记录。
// 参数 a 为 Agent 模型指针；返回创建错误。
func (r *Repository) CreateAgent(a *model.Agent) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(a).Error
}

// SaveAgent 更新 Agent 信息。
// 参数 a 为 Agent 模型指针；返回更新错误。
func (r *Repository) SaveAgent(a *model.Agent) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(a).Error
}

// EnqueueCommand 将一条待投递指令写入数据库队列。
// 参数 cmd 为指令模型指针；返回写入错误。
func (r *Repository) EnqueueCommand(cmd *model.AgentCommand) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(cmd).Error
}

// FetchPendingCommands 清理已过期指令，并读取指定 Agent 在 ackSeq 之后尚未完成的待发/续传指令（按 ID 升序）。
// 参数 agentID 为目标 Agent，ackSeq 为客户端已确认的最大序号，now 为当前时刻；返回指令切片与错误。
func (r *Repository) FetchPendingCommands(agentID, ackSeq uint64, now time.Time) ([]model.AgentCommand, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	// 先将已确认序号及以前的 delivered 指令推进为 running/succeeded（若尚未结单）并过期超时未投递指令
	_ = db.Model(&model.AgentCommand{}).
		Where("agent_id = ? AND status = 'pending' AND expires_at <= ?", agentID, now).
		Update("status", "expired").Error
	if ackSeq > 0 {
		_ = db.Model(&model.AgentCommand{}).
			Where("agent_id = ? AND id <= ? AND status = 'delivered'", agentID, ackSeq).
			Update("status", "running").Error
	}

	var cmds []model.AgentCommand
	err := db.Where("agent_id = ? AND id > ? AND status IN ('pending', 'delivered') AND expires_at > ?", agentID, ackSeq, now).
		Order("id ASC").Limit(20).Find(&cmds).Error
	if err != nil {
		return nil, err
	}
	if len(cmds) > 0 {
		ids := make([]uint64, 0, len(cmds))
		for _, c := range cmds {
			ids = append(ids, c.ID)
		}
		_ = db.Model(&model.AgentCommand{}).Where("id IN ? AND status = 'pending'", ids).
			Updates(map[string]any{"status": "delivered", "delivered_at": &now}).Error
	}
	return cmds, nil
}

// UpdateCommandStatus 根据唯一指令 UID 更新指令状态（running / succeeded / failed）。
// 参数 commandUID 为唯一指令 ID，status 为新状态，errMsg 为可选失败原因；返回更新错误。
func (r *Repository) UpdateCommandStatus(commandUID, status, errMsg string) error {
	db := r.dbProvider()
	if db == nil || commandUID == "" {
		return nil
	}
	updates := map[string]any{
		"status":    status,
		"error_msg": errMsg,
	}
	if status == "succeeded" || status == "failed" {
		now := time.Now()
		updates["finished_at"] = &now
	}
	return db.Model(&model.AgentCommand{}).Where("command_uid = ?", commandUID).Updates(updates).Error
}

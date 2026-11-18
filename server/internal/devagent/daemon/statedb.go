package daemon

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"autoship/internal/database"
	"autoship/internal/devagent/config"
	"autoship/pkg/keychain"
)

// AgentWorkStateRecord 是本地独立库 `autoship_agent` 中持久化开发任务状态与会话引用的 GORM 模型（严格落实开发规范第 2 条：完整表注释与字段注释）。
type AgentWorkStateRecord struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement;comment:本地状态自增主键"`
	WorkID         string    `gorm:"type:varchar(64);uniqueIndex;not null;comment:开发任务全局唯一标识"`
	ProductCode    string    `gorm:"type:varchar(64);index;not null;comment:所属产品唯一编码"`
	Status         string    `gorm:"type:varchar(48);not null;comment:本地开发任务执行阶段状态"`
	BranchName     string    `gorm:"type:varchar(128);not null;default:'';comment:独立worktree分支名称"`
	WorktreeDir    string    `gorm:"type:varchar(512);not null;default:'';comment:独立worktree本地目录路径"`
	HeadCommitSHA  string    `gorm:"type:varchar(64);not null;default:'';comment:任务分支最新提交哈希"`
	TranscriptPath string    `gorm:"type:varchar(512);not null;default:'';comment:本地JSONL会话记录文件路径"`
	TotalTokens    int       `gorm:"not null;default:0;comment:累计消耗大模型Token数量"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime;comment:状态最后更新时间"`
	CreatedAt      time.Time `gorm:"autoCreateTime;comment:状态首次创建时间"`
}

// TableName 返回本地开发任务状态物理表名。
// 无入参；返回固定表名字符串。
func (AgentWorkStateRecord) TableName() string {
	return "agent_work_states"
}

// TableComment 返回本地开发任务状态表的中文用途注释。
// 无入参；返回非空中文表注释。
func (AgentWorkStateRecord) TableComment() string {
	return "Mac开发Agent本地开发任务运行状态与JSONL会话引用表"
}

// AgentCommandReceiptRecord 记录已消费的 Server 下发指令 ID，保障至少一次投递下的严格幂等去重（7.1 节）。
type AgentCommandReceiptRecord struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;comment:幂等回执自增主键"`
	CommandID   string    `gorm:"type:varchar(64);uniqueIndex;not null;comment:Server指令信封全局唯一ID"`
	CommandType string    `gorm:"type:varchar(64);not null;comment:指令类型枚举"`
	Seq         uint64    `gorm:"not null;default:0;comment:对端发送序号"`
	ProcessedAt time.Time `gorm:"autoCreateTime;comment:指令幂等登记时间"`
}

// TableName 返回指令幂等回执物理表名。
// 无入参；返回固定表名字符串。
func (AgentCommandReceiptRecord) TableName() string {
	return "agent_command_receipts"
}

// TableComment 返回指令幂等回执表的中文用途注释。
// 无入参；返回非空中文表注释。
func (AgentCommandReceiptRecord) TableComment() string {
	return "Mac开发Agent接收Server指令幂等去重回执表"
}

// LocalStateModels 返回 `autoship_agent` 库全部需要迁移与校验注释的模型切片。
// 无入参；返回 []database.TableCommenter。
func LocalStateModels() []database.TableCommenter {
	return []database.TableCommenter{
		&AgentWorkStateRecord{},
		&AgentCommandReceiptRecord{},
	}
}

// StateStore 封装可选的本地 MySQL `autoship_agent` 状态库（8.1 节：独立账号，与被测应用数据库彻底隔离）。
type StateStore struct {
	db *gorm.DB
}

// OpenStateStore 根据 `LocalStateDBConfig` 连接并自动建库迁移 `autoship_agent`；当 Enabled=false 时返回 nil, nil。
// 参数 cfg 为本地状态库配置，keychainSvc 为钥匙串命名空间，secrets 为钥匙串读取接口；返回 *StateStore 及连接/迁移错误。
func OpenStateStore(cfg config.LocalStateDBConfig, keychainSvc string, secrets keychain.Store) (*StateStore, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	models := LocalStateModels()
	if err := database.ValidateModelsMetadata(models); err != nil {
		return nil, fmt.Errorf("autoship_agent 状态库模型注释自检失败: %w", err)
	}

	pass := cfg.Password
	if secrets != nil && strings.TrimSpace(cfg.KeychainAccount) != "" {
		if v, err := secrets.Get(keychainSvc, cfg.KeychainAccount); err == nil && v != "" {
			pass = v
		}
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port <= 0 {
		port = 3306
	}
	dbName := strings.ReplaceAll(strings.TrimSpace(cfg.DBName), "`", "")
	if dbName == "" {
		dbName = "autoship_agent"
	}

	rootDSN := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		cfg.User, pass, host, port)
	rootDB, err := gorm.Open(mysql.Open(rootDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("连接本机状态库实例失败: %w", err)
	}
	if sqlRoot, _ := rootDB.DB(); sqlRoot != nil {
		_ = rootDB.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName)).Error
		_ = sqlRoot.Close()
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		cfg.User, pass, host, port, dbName)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("打开本地状态库 %s 失败: %w", dbName, err)
	}
	if err := database.MigrateWithComments(db, models); err != nil {
		return nil, err
	}
	return &StateStore{db: db}, nil
}

// UpsertWorkState 新增或刷新单条开发任务的本地持久化运行状态。
// 参数 rec 为任务状态实体；StateStore 未启用时无操作返回 nil。
func (s *StateStore) UpsertWorkState(rec AgentWorkStateRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	var existing AgentWorkStateRecord
	err := s.db.Where("work_id = ?", rec.WorkID).First(&existing).Error
	if err == nil {
		rec.ID = existing.ID
		rec.CreatedAt = existing.CreatedAt
		return s.db.Save(&rec).Error
	}
	return s.db.Create(&rec).Error
}

// HasProcessedCommand 查询指定指令 ID 是否已在状态库中登记消费。
// 参数 cmdID 为指令唯一标识；已处理返回 true，否则返回 false。
func (s *StateStore) HasProcessedCommand(cmdID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	var count int64
	_ = s.db.Model(&AgentCommandReceiptRecord{}).Where("command_id = ?", cmdID).Count(&count).Error
	return count > 0
}

// RecordCommand 登记已接收处理的指令回执。
// 参数 cmdID/cmdType/seq 为指令元数据；未启用时直接返回 nil。
func (s *StateStore) RecordCommand(cmdID, cmdType string, seq uint64) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Create(&AgentCommandReceiptRecord{
		CommandID:   cmdID,
		CommandType: cmdType,
		Seq:         seq,
	}).Error
}

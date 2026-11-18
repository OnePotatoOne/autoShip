// Package database 负责 MySQL 8 连接管理、全模块模型自动迁移，以及表注释与字段注释的强制校验与同步（严格落实开发规范第 2 条）。
package database

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"autoship/internal/config"
	"autoship/pkg/crypto"
)

// TableCommenter 约束所有业务数据表模型必须同时声明确定性的物理表名与中文表注释。
type TableCommenter interface {
	// TableName 返回数据库物理表名。
	TableName() string
	// TableComment 返回数据库物理表的中文用途注释（不得为空）。
	TableComment() string
}

var (
	mu               sync.RWMutex
	globalDB         *gorm.DB
	registeredModels []TableCommenter
)

// RegisterModels 将各业务模块的数据表结构体指针注册到全局迁移列表中。
// 参数 models 为实现了 TableCommenter 接口的结构体指针列表；在进程启动或单元测试期间调用。
func RegisterModels(models ...TableCommenter) {
	mu.Lock()
	defer mu.Unlock()
	registeredModels = append(registeredModels, models...)
}

// RegisteredModels 返回已注册全部数据表模型的浅拷贝切片，供迁移及注释完整性单测检查使用。
// 无入参；返回 []TableCommenter 切片。
func RegisteredModels() []TableCommenter {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]TableCommenter, len(registeredModels))
	copy(out, registeredModels)
	return out
}

// Get 返回当前全局已初始化的 *gorm.DB 实例（数据库尚未连通或处于待初始化向导态时可能为 nil）。
// 无入参；返回 *gorm.DB。
func Get() *gorm.DB {
	mu.RLock()
	defer mu.RUnlock()
	return globalDB
}

// Set 允许集成测试或动态重连逻辑替换全局 *gorm.DB 实例。
// 参数 db 为已打开的 GORM 连接；无返回值。
func Set(db *gorm.DB) {
	mu.Lock()
	defer mu.Unlock()
	globalDB = db
}

// ValidateModelsMetadata 在不依赖外部数据库实例的前提下，使用反射静态审计全部模型的表注释与字段 comment 标签是否 100% 完整。
// 参数 models 为待校验的模型切片；若任何表缺少 TableComment 或任何持久化字段缺少 comment 标签则立即返回详细错误。
func ValidateModelsMetadata(models []TableCommenter) error {
	if len(models) == 0 {
		return errors.New("未注册任何数据库模型")
	}
	seenTables := make(map[string]bool, len(models))
	for _, m := range models {
		if m == nil {
			return errors.New("发现 nil 数据库模型")
		}
		tableName := strings.TrimSpace(m.TableName())
		tableComment := strings.TrimSpace(m.TableComment())
		if tableName == "" {
			return fmt.Errorf("模型 %T 的 TableName() 为空", m)
		}
		if tableComment == "" {
			return fmt.Errorf("表 %s (%T) 缺少表注释 TableComment()", tableName, m)
		}
		if seenTables[tableName] {
			continue
		}
		seenTables[tableName] = true

		t := reflect.TypeOf(m)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return fmt.Errorf("表 %s 对应模型 %T 不是结构体", tableName, m)
		}
		if err := checkStructFieldComments(tableName, t); err != nil {
			return err
		}
	}
	return nil
}

// checkStructFieldComments 递归检查结构体内所有非忽略（非 gorm:"-"）导出字段是否声明了非空的 comment 标签。
// 参数 tableName 为所属表名，t 为结构体反射类型；全部合规返回 nil，否则返回具体缺失字段错误。
func checkStructFieldComments(tableName string, t reflect.Type) error {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		gormTag := f.Tag.Get("gorm")
		if gormTag == "-" || strings.HasPrefix(gormTag, "-:") {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if err := checkStructFieldComments(tableName, f.Type); err != nil {
				return err
			}
			continue
		}
		if !hasNonEmptyGormComment(gormTag) {
			return fmt.Errorf("表 %s 的字段 %s 缺少非空 gorm comment 注释标签 (当前 tag=%q)", tableName, f.Name, gormTag)
		}
	}
	return nil
}

// hasNonEmptyGormComment 解析 gorm 标签分号分隔段，确认包含非空的 comment:xxx 定义。
// 参数 tag 为原始 gorm struct tag 字符串；存在有效字段注释返回 true，否则返回 false。
func hasNonEmptyGormComment(tag string) bool {
	parts := strings.Split(tag, ";")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(trimmed, "comment:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "comment:"))
			val = strings.Trim(val, "'\"")
			return val != ""
		}
	}
	return false
}

// Init 连接 MySQL 8，自动创建目标数据库（utf8mb4），对全部注册模型执行建表与表注释同步，并赋值全局 DB。
// 参数 cfg 为数据库配置，encKey 为用于解密密码的 32 字节密钥；成功返回 *gorm.DB，失败返回详细错误。
func Init(cfg config.DatabaseConfig, encKey []byte) (*gorm.DB, error) {
	models := RegisteredModels()
	if len(models) > 0 {
		if err := ValidateModelsMetadata(models); err != nil {
			return nil, fmt.Errorf("模型注释规范自检不通过: %w", err)
		}
	}

	plainPass, err := crypto.DecryptField(cfg.Password, encKey)
	if err != nil {
		return nil, fmt.Errorf("解密数据库密码失败: %w", err)
	}
	if err := ensureDatabaseExists(cfg, plainPass); err != nil {
		return nil, err
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN(plainPass)), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 数据库连接失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	}

	if err := MigrateWithComments(db, models); err != nil {
		return nil, err
	}
	Set(db)
	return db, nil
}

// MigrateWithComments 逐表附带 InnoDB + utf8mb4 + 表注释选项执行 GORM AutoMigrate，并补跑 ALTER TABLE COMMENT 确保升级已有表时表注释同步更新。
// 参数 db 为已连接的 MySQL GORM 实例，models 为模型列表；成功返回 nil，失败返回错误。
func MigrateWithComments(db *gorm.DB, models []TableCommenter) error {
	for _, m := range models {
		escapedComment := strings.ReplaceAll(m.TableComment(), "'", "''")
		tableOpts := fmt.Sprintf("ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='%s'", escapedComment)
		if err := db.Set("gorm:table_options", tableOpts).AutoMigrate(m); err != nil {
			return fmt.Errorf("迁移数据表 %s 失败: %w", m.TableName(), err)
		}
		// 对已存在的数据表同步刷新表级注释
		alterSQL := fmt.Sprintf("ALTER TABLE `%s` COMMENT = '%s'", m.TableName(), escapedComment)
		if err := db.Exec(alterSQL).Error; err != nil {
			return fmt.Errorf("同步数据表 %s 表注释失败: %w", m.TableName(), err)
		}
	}
	return nil
}

// ensureDatabaseExists 在指定库尚不存在时连接实例根路径执行 CREATE DATABASE IF NOT EXISTS。
// 参数 cfg 为数据库主机端口账号配置，plainPass 为明文密码；成功返回 nil，失败返回错误。
func ensureDatabaseExists(cfg config.DatabaseConfig, plainPass string) error {
	rootDSN := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		cfg.User, plainPass, cfg.Host, cfg.Port)
	rootDB, err := gorm.Open(mysql.Open(rootDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("连接 MySQL 实例失败: %w", err)
	}
	sqlDB, _ := rootDB.DB()
	if sqlDB != nil {
		defer sqlDB.Close()
	}
	safeName := strings.ReplaceAll(cfg.DBName, "`", "")
	createSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", safeName)
	if err := rootDB.Exec(createSQL).Error; err != nil {
		return fmt.Errorf("创建数据库 %s 失败: %w", safeName, err)
	}
	return nil
}

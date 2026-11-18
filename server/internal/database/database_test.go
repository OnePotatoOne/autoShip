// Package database_test 测试数据库模型的表注释与字段注释静态门禁校验逻辑。
package database_test

import (
	"testing"

	"autoship/internal/database"
)

// validSampleModel 是同时包含表注释与完整字段注释的测试样本模型。
type validSampleModel struct {
	ID   uint64 `gorm:"primaryKey;autoIncrement;comment:主键ID"`
	Name string `gorm:"size:64;not null;comment:样本名称"`
}

// TableName 返回合法测试样本物理表名。
func (validSampleModel) TableName() string { return "sample_valid" }

// TableComment 返回合法测试样本物理表中文注释。
func (validSampleModel) TableComment() string { return "测试合法样本表" }

// missingFieldCommentModel 是故意缺少 Name 字段注释以测试门禁拦截能力的负向样本模型。
type missingFieldCommentModel struct {
	ID   uint64 `gorm:"primaryKey;comment:主键ID"`
	Name string `gorm:"size:64;not null"`
}

// TableName 返回缺失注释样本物理表名。
func (missingFieldCommentModel) TableName() string { return "sample_invalid" }

// TableComment 返回缺失注释样本物理表中文注释。
func (missingFieldCommentModel) TableComment() string { return "缺少字段注释样本表" }

// TestValidateModelsMetadata 验证合法表与字段注释可通过，缺失字段注释会被精准拦截。
func TestValidateModelsMetadata(t *testing.T) {
	if err := database.ValidateModelsMetadata([]database.TableCommenter{&validSampleModel{}}); err != nil {
		t.Fatalf("合法模型被误判: %v", err)
	}
	if err := database.ValidateModelsMetadata([]database.TableCommenter{&missingFieldCommentModel{}}); err == nil {
		t.Fatalf("缺失字段注释的模型未被拦截")
	}
}

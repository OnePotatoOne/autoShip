package gates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"autoship/pkg/manifest"
)

var (
	// destructiveMigrationRe 匹配删除表/列、重命名表/列、清空表或 GORM DropTable/DropColumn 等破坏性迁移操作
	destructiveMigrationRe = regexp.MustCompile(`(?im)\b(?:DROP\s+TABLE|DROP\s+COLUMN|RENAME\s+COLUMN|RENAME\s+TABLE|TRUNCATE\s+TABLE|ALTER\s+COLUMN\s+\S+\s+TYPE)\b|\.\s*(?:DropTable|DropColumn|RenameColumn|RenameTable)\s*\(`)
	// dataRewriteMigrationRe 匹配针对存量数据的批量改写语句（UPDATE ... SET / DELETE FROM / INSERT INTO ... SELECT）
	dataRewriteMigrationRe = regexp.MustCompile(`(?im)^\s*\+?\s*(?:UPDATE\s+[` + "`" + `A-Za-z0-9_]+\s+SET\b|DELETE\s+FROM\s+[` + "`" + `A-Za-z0-9_]+\b|INSERT\s+INTO\s+[` + "`" + `A-Za-z0-9_]+[^;\r\n]*\bSELECT\b)`)
	// additiveMigrationRe 匹配新增表、新增列、新增索引或 AutoMigrate 等增量表结构变更
	additiveMigrationRe = regexp.MustCompile(`(?im)\b(?:CREATE\s+TABLE|ADD\s+COLUMN|CREATE\s+(?:UNIQUE\s+)?INDEX|ADD\s+INDEX|AutoMigrate)\b`)
)

// ClassifyMigrationRisk 综合 SQL 变更与 Go 模型 diff 将本次任务的数据库迁移风险划分为 none | additive | data_rewrite | destructive（8.7 节）。
// 参数 changedFiles 为改动相对路径切片，relevantDiffText 为涉及 SQL/Model 文件的 diff 文本；返回最高风险等级与命中原因列表。
func ClassifyMigrationRisk(changedFiles []string, relevantDiffText string) (manifest.MigrationRisk, []string) {
	hasDBRelatedFile := false
	for _, f := range changedFiles {
		lower := strings.ToLower(f)
		if strings.HasSuffix(lower, ".sql") || strings.Contains(lower, "migration") || strings.Contains(lower, "model") || strings.Contains(lower, "database") {
			hasDBRelatedFile = true
			break
		}
	}
	if !hasDBRelatedFile && strings.TrimSpace(relevantDiffText) == "" {
		return manifest.RiskNone, nil
	}

	// 只扫描 diff 中新增的代码行（以 `+` 开头但排除 `+++` 文件头），若传入的是纯 SQL 全文则一并扫描
	scanText := extractAddedDiffLinesOrRaw(relevantDiffText)
	if strings.TrimSpace(scanText) == "" {
		return manifest.RiskNone, nil
	}

	var reasons []string
	risk := manifest.RiskNone

	if matches := destructiveMigrationRe.FindAllString(scanText, -1); len(matches) > 0 {
		risk = manifest.RiskDestructive
		reasons = append(reasons, fmt.Sprintf("检测到破坏性表结构变更指令: %s", strings.Join(uniqueStrings(matches), ", ")))
		return risk, reasons
	}
	if matches := dataRewriteMigrationRe.FindAllString(scanText, -1); len(matches) > 0 {
		risk = manifest.RiskDataRewrite
		reasons = append(reasons, "检测到存量业务数据改写语句（部署前必须先执行数据库备份）")
		return risk, reasons
	}
	if matches := additiveMigrationRe.FindAllString(scanText, -1); len(matches) > 0 {
		risk = manifest.RiskAdditive
		reasons = append(reasons, fmt.Sprintf("检测到增量表结构变更: %s", strings.Join(uniqueStrings(matches), ", ")))
	}
	return risk, reasons
}

// ColumnMeta 描述从 MySQL information_schema.COLUMNS 读取的单列结构快照，用于回滚演练前后的破坏性差异比对。
type ColumnMeta struct {
	ColumnName    string `json:"columnName"`    // 字段名
	ColumnType    string `json:"columnType"`    // 完整列类型（如 varchar(64)、bigint）
	IsNullable    string `json:"isNullable"`    // "YES" 或 "NO"
	HasDefault    bool   `json:"hasDefault"`    // 是否声明了默认值（COLUMN_DEFAULT IS NOT NULL）
	Extra         string `json:"extra"`         // auto_increment 等附加属性
}

// SchemaSnapshot 保存某个数据库在特定时刻的全部物理表及列元数据映射（tableName -> colName -> ColumnMeta）。
type SchemaSnapshot struct {
	Tables map[string]map[string]ColumnMeta `json:"tables"` // 表名 -> 字段名 -> 字段元数据
}

// SchemaInspector 抽象采集数据库表结构快照的接口（生产查询 MySQL information_schema，单测可注入内存快照）。
type SchemaInspector interface {
	// Snapshot 读取指定数据库名的当前全部表与字段定义。
	Snapshot(ctx context.Context, dbName string) (*SchemaSnapshot, error)
}

// MySQLSchemaInspector 使用标准 sql.DB 查询 MySQL 8 `information_schema.TABLES` 与 `COLUMNS` 生成快照。
type MySQLSchemaInspector struct {
	db *sql.DB
}

// NewMySQLSchemaInspector 基于已打开的 MySQL 连接构造 *MySQLSchemaInspector。
// 参数 db 为具有目标测试库读取权限的 *sql.DB；返回 *MySQLSchemaInspector。
func NewMySQLSchemaInspector(db *sql.DB) *MySQLSchemaInspector {
	return &MySQLSchemaInspector{db: db}
}

// Snapshot 查询 MySQL `information_schema` 构造指定数据库的完整表与列结构快照。
// 参数 ctx 为上下文，dbName 为目标测试数据库名；返回 *SchemaSnapshot 及查询错误。
func (i *MySQLSchemaInspector) Snapshot(ctx context.Context, dbName string) (*SchemaSnapshot, error) {
	if i == nil || i.db == nil {
		return nil, errors.New("MySQLSchemaInspector 未绑定数据库连接")
	}
	snap := &SchemaSnapshot{Tables: make(map[string]map[string]ColumnMeta)}

	tRows, err := i.db.QueryContext(ctx,
		`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'`, dbName)
	if err != nil {
		return nil, fmt.Errorf("查询 information_schema.TABLES 失败: %w", err)
	}
	defer tRows.Close()
	for tRows.Next() {
		var tName string
		if err := tRows.Scan(&tName); err != nil {
			return nil, err
		}
		snap.Tables[tName] = make(map[string]ColumnMeta)
	}

	cRows, err := i.db.QueryContext(ctx,
		`SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA
		 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?`, dbName)
	if err != nil {
		return nil, fmt.Errorf("查询 information_schema.COLUMNS 失败: %w", err)
	}
	defer cRows.Close()
	for cRows.Next() {
		var tName, cName, cType, nullable, extra string
		var colDefault sql.NullString
		if err := cRows.Scan(&tName, &cName, &cType, &nullable, &colDefault, &extra); err != nil {
			return nil, err
		}
		if _, ok := snap.Tables[tName]; !ok {
			snap.Tables[tName] = make(map[string]ColumnMeta)
		}
		snap.Tables[tName][cName] = ColumnMeta{
			ColumnName: cName,
			ColumnType: strings.ToLower(strings.TrimSpace(cType)),
			IsNullable: strings.ToUpper(strings.TrimSpace(nullable)),
			HasDefault: colDefault.Valid,
			Extra:      strings.ToLower(strings.TrimSpace(extra)),
		}
	}
	return snap, nil
}

// CompareSchemasForRollback 对比上一版与候选版迁移后的表结构快照，检测任何会导致回滚到上一版后不兼容的破坏性差异（删表、删列、列类型改变、或新增无默认值的 NOT NULL 列）。
// 参数 before 为上一版结构快照，after 为候选版迁移后的结构快照；返回不兼容差异列表（为空表示向后兼容可安全回滚）。
func CompareSchemasForRollback(before, after *SchemaSnapshot) []string {
	if before == nil || after == nil {
		return nil
	}
	var diffs []string
	for tableName, beforeCols := range before.Tables {
		afterCols, ok := after.Tables[tableName]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("既有数据表 %s 在候选版本迁移后被删除", tableName))
			continue
		}
		for colName, bCol := range beforeCols {
			aCol, colExists := afterCols[colName]
			if !colExists {
				diffs = append(diffs, fmt.Sprintf("表 %s 的既有字段 %s 被删除，旧版程序回滚后查询/写入将报错", tableName, colName))
				continue
			}
			if bCol.ColumnType != aCol.ColumnType {
				diffs = append(diffs, fmt.Sprintf("表 %s.%s 字段类型发生变更 (%s -> %s)，存在回滚兼容风险",
					tableName, colName, bCol.ColumnType, aCol.ColumnType))
			}
			if bCol.IsNullable == "YES" && aCol.IsNullable == "NO" && !aCol.HasDefault {
				diffs = append(diffs, fmt.Sprintf("表 %s.%s 从可空收窄为无默认值的 NOT NULL", tableName, colName))
			}
		}
		// 检查既有表上新增的列：若为 NOT NULL 且无 DEFAULT 且非自增，旧版回滚后执行 INSERT 将因缺列值直接失败！
		for colName, aCol := range afterCols {
			if _, existed := beforeCols[colName]; existed {
				continue
			}
			if aCol.IsNullable == "NO" && !aCol.HasDefault && !strings.Contains(aCol.Extra, "auto_increment") {
				diffs = append(diffs, fmt.Sprintf("既有表 %s 新增了无默认值的 NOT NULL 字段 %s，上一版回滚后执行 INSERT 会失败", tableName, colName))
			}
		}
	}
	sort.Strings(diffs)
	return diffs
}

// RollbackDrillSpec 定义一次完整四步数据库回滚演练所需的生命周期钩子与检查器（8.7 节）。
type RollbackDrillSpec struct {
	DBName          string                                  // 演练使用的临时测试数据库名
	Inspector       SchemaInspector                         // 表结构快照采集器
	StartPrevious   func(ctx context.Context) (func(), error) // 启动上一版并等待健康就绪，返回停止函数
	StartCandidate  func(ctx context.Context) (func(), error) // 启动候选版完成数据库迁移并等待健康就绪，返回停止函数
	WriteSampleData func(ctx context.Context) error           // 在候选版运行期间写入模拟业务数据
	VerifyPrevious  func(ctx context.Context) error           // 上一版重新启动后的健康检查与只读探测校验
}

// RunRollbackDrill 按“上一版启动 → 候选版启动迁移 → 写入测试数据 → 上一版再次启动并校验健康与表结构无破坏性差异”执行完整回滚演练（8.7 节）。
// 参数 ctx 为演练总超时上下文，spec 为演练编排定义；返回门禁 Result。
func RunRollbackDrill(ctx context.Context, spec RollbackDrillSpec) Result {
	if spec.StartPrevious == nil || spec.StartCandidate == nil || spec.Inspector == nil {
		return Result{
			GateName: "rollback_drill",
			Status:   StatusSkipped,
			Summary:  "未配置上一版产物或表结构检查器，跳过回滚演练",
		}
	}

	// 第 1 步：启动上一版建立基准库表结构并采样快照
	stopPrev1, err := spec.StartPrevious(ctx)
	if err != nil {
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("回滚演练步骤 1（启动上一版基线）失败: %v", err),
			Violations: []Violation{{Rule: "drill.prev_start", Message: err.Error()}},
		}
	}
	beforeSnap, snapErr := spec.Inspector.Snapshot(ctx, spec.DBName)
	stopPrev1()
	if snapErr != nil {
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("采集上一版 information_schema 快照失败: %v", snapErr),
			Violations: []Violation{{Rule: "drill.schema_before", Message: snapErr.Error()}},
		}
	}

	// 第 2 步：启动候选版本执行新迁移，并写入一批业务数据
	stopCand, err := spec.StartCandidate(ctx)
	if err != nil {
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("回滚演练步骤 2（候选版本启动与迁移）失败: %v", err),
			Violations: []Violation{{Rule: "drill.candidate_start", Message: err.Error()}},
		}
	}
	if spec.WriteSampleData != nil {
		if writeErr := spec.WriteSampleData(ctx); writeErr != nil {
			stopCand()
			return Result{
				GateName:   "rollback_drill",
				Status:     StatusFailed,
				Summary:    fmt.Sprintf("回滚演练步骤 3（候选版本写入业务数据）失败: %v", writeErr),
				Violations: []Violation{{Rule: "drill.write_data", Message: writeErr.Error()}},
			}
		}
	}
	afterSnap, snapAfterErr := spec.Inspector.Snapshot(ctx, spec.DBName)
	stopCand()
	if snapAfterErr != nil {
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("采集候选版迁移后 information_schema 快照失败: %v", snapAfterErr),
			Violations: []Violation{{Rule: "drill.schema_after", Message: snapAfterErr.Error()}},
		}
	}

	// 第 3 步：对比迁移前后 information_schema 是否存在破坏回滚兼容性的结构变更
	incompatDiffs := CompareSchemasForRollback(beforeSnap, afterSnap)

	// 第 4 步：重新启动上一版，执行健康检查与模块只读探测（同时防止上一版启动时反向删改新列失败）
	stopPrev2, err := spec.StartPrevious(ctx)
	if err != nil {
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("回滚演练步骤 4（迁移后回退重启上一版）失败: %v", err),
			Violations: []Violation{{Rule: "drill.prev_restart", Message: err.Error()}},
		}
	}
	var verifyErr error
	if spec.VerifyPrevious != nil {
		verifyErr = spec.VerifyPrevious(ctx)
	}
	postRestartSnap, _ := spec.Inspector.Snapshot(ctx, spec.DBName)
	stopPrev2()

	if verifyErr != nil {
		incompatDiffs = append(incompatDiffs, fmt.Sprintf("上一版回退重启后健康检查不通过: %v", verifyErr))
	}
	// 检查上一版重启是否反向破坏了候选版本刚迁移出来的结构（如附录 A 提到的旧版 AutoMigrate 含 DropColumn）
	if postRestartSnap != nil {
		for tbl, candCols := range afterSnap.Tables {
			postCols, ok := postRestartSnap.Tables[tbl]
			if !ok {
				incompatDiffs = append(incompatDiffs, fmt.Sprintf("上一版重启后反向删除了候选版本新建的表 %s", tbl))
				continue
			}
			for col := range candCols {
				if _, colOK := postCols[col]; !colOK {
					incompatDiffs = append(incompatDiffs, fmt.Sprintf("上一版重启后反向删除了候选版本新增的字段 %s.%s", tbl, col))
				}
			}
		}
	}

	if len(incompatDiffs) > 0 {
		violations := make([]Violation, 0, len(incompatDiffs))
		for _, msg := range incompatDiffs {
			violations = append(violations, Violation{Rule: "drill.incompatible_schema", Message: msg})
		}
		return Result{
			GateName:   "rollback_drill",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("数据库回滚演练未通过：共发现 %d 处回滚兼容性问题", len(incompatDiffs)),
			Violations: violations,
		}
	}
	return Result{
		GateName: "rollback_drill",
		Status:   StatusPassed,
		Summary:  "数据库四步回滚演练通过：上一版回退启动健康且 information_schema 无破坏性差异",
	}
}

// extractAddedDiffLinesOrRaw 从 unified diff 中提取新增行；若输入非 diff 格式则原样返回。
func extractAddedDiffLinesOrRaw(text string) string {
	if !strings.Contains(text, "@@") && !strings.Contains(text, "+++") {
		return text
	}
	var added []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added = append(added, line[1:])
		}
	}
	return strings.Join(added, "\n")
}

// uniqueStrings 对匹配到的关键字去重并转大写展示。
func uniqueStrings(items []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, item := range items {
		norm := strings.ToUpper(strings.Join(strings.Fields(item), " "))
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

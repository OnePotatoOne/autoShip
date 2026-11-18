package gates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"autoship/pkg/manifest"
)

// fakeDepFetcher 用于在单元测试中模拟官方包仓库返回的发布时间与包体积。
type fakeDepFetcher struct {
	meta map[string]*PackageReleaseMeta
}

// FetchMeta 返回预设的模拟包元数据。
func (f *fakeDepFetcher) FetchMeta(_ context.Context, _, pkgName, version string) (*PackageReleaseMeta, error) {
	if m, ok := f.meta[pkgName+"@"+version]; ok {
		return m, nil
	}
	return &PackageReleaseMeta{PublishedAt: time.Now().AddDate(-1, 0, 0), SizeBytes: 1024}, nil
}

// memorySchemaInspector 按调用顺序返回预设的回滚演练前后数据库结构快照。
type memorySchemaInspector struct {
	snapshots []*SchemaSnapshot
	idx       int
}

// Snapshot 依次弹出下一个模拟的数据库结构快照。
func (m *memorySchemaInspector) Snapshot(_ context.Context, _ string) (*SchemaSnapshot, error) {
	if m.idx >= len(m.snapshots) {
		return m.snapshots[len(m.snapshots)-1], nil
	}
	out := m.snapshots[m.idx]
	m.idx++
	return out, nil
}

// TestGoTSDBAndUTF8CommentGates 全面验证 Go AST 注释、TypeScript 注释、SQL/GORM 数据库注释及 UTF-8 合法性检查器。
func TestGoTSDBAndUTF8CommentGates(t *testing.T) {
	wt := t.TempDir()

	// 1. Go 注释检查：缺失函数/类型注释被检出，补齐后通过
	badGo := "package sample\n\ntype User struct { ID int }\nfunc helper() {}\n"
	if err := os.WriteFile(filepath.Join(wt, "bad.go"), []byte(badGo), 0644); err != nil {
		t.Fatal(err)
	}
	rGoBad := CheckGoComments(wt, []string{"bad.go"})
	if rGoBad.Status != StatusFailed || len(rGoBad.Violations) != 2 {
		t.Fatalf("期望检出结构体与私有函数 2 处缺失注释，实际: %+v", rGoBad)
	}

	goodGo := "package sample\n\n// User 表示系统用户。\ntype User struct { ID int }\n\n// helper 执行内部辅助校验。\nfunc helper() {}\n"
	if err := os.WriteFile(filepath.Join(wt, "good.go"), []byte(goodGo), 0644); err != nil {
		t.Fatal(err)
	}
	if rGoGood := CheckGoComments(wt, []string{"good.go"}); rGoGood.Status != StatusPassed {
		t.Fatalf("补齐注释后 Go 检查应通过: %+v", rGoGood)
	}

	// 2. TypeScript 注释检查：带 @Component 装饰器的类、方法及顶层函数
	badTS := "@Component({ selector: 'app-x' })\nexport class DemoComponent {\n  loadData(id: number) {\n    return id;\n  }\n}\nexport function formatVal(v: string) { return v; }\n"
	if err := os.WriteFile(filepath.Join(wt, "bad.ts"), []byte(badTS), 0644); err != nil {
		t.Fatal(err)
	}
	rTSBad := CheckTSComments(wt, []string{"bad.ts"})
	if rTSBad.Status != StatusFailed || len(rTSBad.Violations) != 3 {
		t.Fatalf("期望检出 TS class/method/function 3 处缺失注释，实际: %+v", rTSBad)
	}

	goodTS := "/** 示例组件 */\n@Component({\n  selector: 'app-x'\n})\nexport class DemoComponent {\n  // 加载详情数据\n  loadData(id: number) {\n    if (id > 0) { return id; }\n    return 0;\n  }\n}\n/** 格式化字符串 */\nexport function formatVal(v: string) { return v; }\n"
	if err := os.WriteFile(filepath.Join(wt, "good.ts"), []byte(goodTS), 0644); err != nil {
		t.Fatal(err)
	}
	if rTSGood := CheckTSComments(wt, []string{"good.ts"}); rTSGood.Status != StatusPassed {
		t.Fatalf("合规 TS 文件检查应当通过: %+v", rTSGood)
	}

	// 3. 数据库表注释与字段注释检查（SQL + GORM）
	badSQL := "CREATE TABLE orders (\n  id BIGINT PRIMARY KEY AUTO_INCREMENT COMMENT '主键',\n  amount DECIMAL(10, 2) NOT NULL\n) ENGINE=InnoDB;\n"
	if err := os.WriteFile(filepath.Join(wt, "001_init.sql"), []byte(badSQL), 0644); err != nil {
		t.Fatal(err)
	}
	rDBBad := CheckDatabaseComments(wt, []string{"001_init.sql"})
	if rDBBad.Status != StatusFailed || len(rDBBad.Violations) != 2 {
		t.Fatalf("期望检出缺失表注释与 amount 字段注释共 2 处，实际: %+v", rDBBad)
	}

	goodSQL := "CREATE TABLE orders (\n  id BIGINT NOT NULL COMMENT '主键',\n  amount DECIMAL(10, 2) NOT NULL COMMENT '订单金额',\n  PRIMARY KEY (id)\n) ENGINE=InnoDB COMMENT='订单主表';\n"
	goodGORM := "package sample\n\n// OrderModel 订单模型\ntype OrderModel struct {\n\tID uint64 `gorm:\"primaryKey;comment:主键ID\"`\n}\nfunc (OrderModel) TableName() string { return \"orders\" }\nfunc (OrderModel) TableComment() string { return \"订单主表\" }\n"
	_ = os.WriteFile(filepath.Join(wt, "002_good.sql"), []byte(goodSQL), 0644)
	_ = os.WriteFile(filepath.Join(wt, "model.go"), []byte(goodGORM), 0644)
	if rDBGood := CheckDatabaseComments(wt, []string{"002_good.sql", "model.go"}); rDBGood.Status != StatusPassed {
		t.Fatalf("合规 SQL 与 GORM 模型应通过数据库注释检查: %+v", rDBGood)
	}

	// 4. UTF-8 检查：非法 GBK 字节应当失败
	_ = os.WriteFile(filepath.Join(wt, "gbk.txt"), []byte{0xC4, 0xE3, 0xBA, 0xC3}, 0644)
	if rUTF8 := CheckUTF8Files(wt, []string{"gbk.txt"}); rUTF8.Status != StatusFailed {
		t.Fatalf("非法非 UTF-8 字节应当失败: %+v", rUTF8)
	}
	if rUTF8OK := CheckUTF8Files(wt, []string{"good.go", "good.ts"}); rUTF8OK.Status != StatusPassed {
		t.Fatalf("合法 UTF-8 文件应当通过: %+v", rUTF8OK)
	}
}

// TestDependencyPolicyAndTestIntegrityGates 验证依赖 30 天/10MB/禁止擅自升级策略以及测试防删、防 skip 与既有测试修改打标。
func TestDependencyPolicyAndTestIntegrityGates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	oldMod := "module demo\n\nrequire github.com/gin-gonic/gin v1.11.0\n"
	newMod := "module demo\n\nrequire (\n\tgithub.com/gin-gonic/gin v1.12.0\n\texample.com/heavy-sdk v0.1.0\n\texample.com/brand-new v1.0.0\n)\n"
	changes := DiffGoModContents("go.mod", oldMod, newMod)
	if len(changes) != 3 {
		t.Fatalf("期望识别出 1 个升级 + 2 个新增依赖，实际: %+v", changes)
	}

	fetcher := &fakeDepFetcher{
		meta: map[string]*PackageReleaseMeta{
			"github.com/gin-gonic/gin@v1.12.0": {PublishedAt: now.AddDate(0, -3, 0), SizeBytes: 512 * 1024},
			"example.com/heavy-sdk@v0.1.0":     {PublishedAt: now.AddDate(0, -2, 0), SizeBytes: 15 * 1024 * 1024}, // 15MB > 10MB
			"example.com/brand-new@v1.0.0":     {PublishedAt: now.AddDate(0, 0, -5), SizeBytes: 100 * 1024},      // 仅发布 5 天
		},
	}
	depRes := CheckDependencyPolicy(ctx, changes, fetcher, now)
	if depRes.Status != StatusAskOwner || len(depRes.Violations) < 3 {
		t.Fatalf("期望触发 ask_owner 且捕获擅自升级、超 10MB、未满 30 天三项违规，实际: %+v", depRes)
	}

	// 测试完整性检查：新增 t.Skip 或删除测试函数必须失败
	failTestDiffs := []TestFileDiffInput{
		{
			FilePath:   "auth_test.go",
			OldContent: "package auth\nimport \"testing\"\nfunc TestLogin(t *testing.T) {}\nfunc TestLogout(t *testing.T) {}\n",
			NewContent: "package auth\nimport \"testing\"\nfunc TestLogin(t *testing.T) { t.Skip(\"temp\") }\n",
		},
	}
	tFail := CheckTestIntegrity(failTestDiffs)
	if tFail.Status != StatusFailed || len(tFail.Violations) != 2 {
		t.Fatalf("期望捕获新增 t.Skip 与删除 TestLogout 共 2 处违规，实际: %+v", tFail)
	}

	// 合法修改既有测试断言且保留全部用例：应通过并单独记录 ModifiedTests
	okTestDiffs := []TestFileDiffInput{
		{
			FilePath:   "auth_test.go",
			OldContent: "package auth\nimport \"testing\"\nfunc TestLogin(t *testing.T) { _ = 1 }\n",
			NewContent: "package auth\nimport \"testing\"\nfunc TestLogin(t *testing.T) { _ = 2 }\nfunc TestNewCase(t *testing.T) {}\n",
			Reason:     "接口新增返回字段，同步补充断言",
		},
	}
	tOK := CheckTestIntegrity(okTestDiffs)
	if tOK.Status != StatusPassed || len(tOK.ModifiedTests) != 1 || len(tOK.ModifiedTests[0].Symbols) != 1 {
		t.Fatalf("合法修改既有测试应通过并记录单列清单，实际: %+v", tOK)
	}
}

// TestMigrationRiskAndRollbackDrill 验证数据库迁移四级风险识别与四步回滚演练（含破坏性列差异拦截）。
func TestMigrationRiskAndRollbackDrill(t *testing.T) {
	ctx := context.Background()

	rDest, _ := ClassifyMigrationRisk([]string{"003.sql"}, "+ALTER TABLE users DROP COLUMN legacy_token;")
	if rDest != manifest.RiskDestructive {
		t.Fatalf("DROP COLUMN 应当判为 destructive，实际 %s", rDest)
	}
	rRewrite, _ := ClassifyMigrationRisk([]string{"004.sql"}, "+UPDATE users SET role = 'member' WHERE role = '';")
	if rRewrite != manifest.RiskDataRewrite {
		t.Fatalf("UPDATE 存量数据应当判为 data_rewrite，实际 %s", rRewrite)
	}
	rAdd, _ := ClassifyMigrationRisk([]string{"005.sql"}, "+ALTER TABLE users ADD COLUMN avatar VARCHAR(255) NULL;")
	if rAdd != manifest.RiskAdditive {
		t.Fatalf("新增可空列应当判为 additive，实际 %s", rAdd)
	}

	// 场景 1：仅新增可空列，回滚演练四步全通
	baseSnap := &SchemaSnapshot{
		Tables: map[string]map[string]ColumnMeta{
			"users": {"id": {ColumnName: "id", ColumnType: "bigint", IsNullable: "NO", Extra: "auto_increment"}},
		},
	}
	candCompatSnap := &SchemaSnapshot{
		Tables: map[string]map[string]ColumnMeta{
			"users": {
				"id":     {ColumnName: "id", ColumnType: "bigint", IsNullable: "NO", Extra: "auto_increment"},
				"avatar": {ColumnName: "avatar", ColumnType: "varchar(255)", IsNullable: "YES"},
			},
		},
	}
	okDrill := RunRollbackDrill(ctx, RollbackDrillSpec{
		DBName:          "demo_test_drill",
		Inspector:       &memorySchemaInspector{snapshots: []*SchemaSnapshot{baseSnap, candCompatSnap, candCompatSnap}},
		StartPrevious:   func(context.Context) (func(), error) { return func() {}, nil },
		StartCandidate:  func(context.Context) (func(), error) { return func() {}, nil },
		WriteSampleData: func(context.Context) error { return nil },
		VerifyPrevious:  func(context.Context) error { return nil },
	})
	if okDrill.Status != StatusPassed {
		t.Fatalf("兼容增量迁移应通过回滚演练: %+v", okDrill)
	}

	// 场景 2：既有表新增了无默认值的 NOT NULL 列，回滚后上一版 INSERT 将失败 -> 演练必须拦截
	candBreakingSnap := &SchemaSnapshot{
		Tables: map[string]map[string]ColumnMeta{
			"users": {
				"id":        {ColumnName: "id", ColumnType: "bigint", IsNullable: "NO", Extra: "auto_increment"},
				"must_fill": {ColumnName: "must_fill", ColumnType: "varchar(32)", IsNullable: "NO", HasDefault: false},
			},
		},
	}
	badDrill := RunRollbackDrill(ctx, RollbackDrillSpec{
		DBName:         "demo_test_drill",
		Inspector:      &memorySchemaInspector{snapshots: []*SchemaSnapshot{baseSnap, candBreakingSnap, candBreakingSnap}},
		StartPrevious:  func(context.Context) (func(), error) { return func() {}, nil },
		StartCandidate: func(context.Context) (func(), error) { return func() {}, nil },
	})
	if badDrill.Status != StatusFailed {
		t.Fatalf("新增无默认值 NOT NULL 列必须被回滚演练拦截: %+v", badDrill)
	}
}

// TestCDPPageInspectorWithLocalChrome 在宿主安装了 Chrome 时验证页面截图与控制台报错捕获（未安装时优雅跳过）。
func TestCDPPageInspectorWithLocalChrome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><h1>AutoShip Preview OK</h1></body></html>`))
	}))
	defer srv.Close()

	inspector := NewCDPInspector("", 15*time.Second)
	if !inspector.Available() {
		_, res, err := inspector.InspectPages(context.Background(), []string{srv.URL}, t.TempDir(), nil)
		if err != nil || res.Status != StatusSkipped {
			t.Fatalf("无 Chrome 时应返回 skipped，实际 res=%+v err=%v", res, err)
		}
		return
	}

	shotDir := t.TempDir()
	recs, res, err := inspector.InspectPages(context.Background(), []string{srv.URL}, shotDir, nil)
	if err != nil {
		t.Fatalf("CDP 巡检出错: %v", err)
	}
	if res.Status != StatusPassed || len(recs) != 1 {
		t.Fatalf("正常页面巡检应当通过: res=%+v recs=%+v", res, recs)
	}
	if st, statErr := os.Stat(recs[0].ScreenshotPath); statErr != nil || st.Size() == 0 {
		t.Fatalf("未生成有效截图文件: %v", statErr)
	}
}

// TestSelfAuditAllDevAgentSourceCommentsAndUTF8 使用本包实现的 Go AST 注释检查器、数据库模型注释检查器与 UTF-8 检查器对整个 devagent 与 3 个 CLI 入口源码做 100% 合规自审。
func TestSelfAuditAllDevAgentSourceCommentsAndUTF8(t *testing.T) {
	repoRoot := filepath.Clean("../../../..")
	scanTargets := []string{
		"server/internal/devagent",
		"server/cmd/autoship-dev-agent/main.go",
		"server/cmd/autoship-release/main.go",
		"server/cmd/autoship-bench/main.go",
		"configs/dev-agent.example.yaml",
	}

	var relFiles []string
	for _, target := range scanTargets {
		absTarget := filepath.Join(repoRoot, filepath.FromSlash(target))
		_ = filepath.Walk(absTarget, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr == nil {
				relFiles = append(relFiles, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if len(relFiles) == 0 {
		t.Fatal("未扫描到任何待自审源码文件")
	}

	if rUTF8 := CheckUTF8Files(repoRoot, relFiles); rUTF8.Status != StatusPassed {
		t.Fatalf("源码 UTF-8 编码自审未通过: %+v", rUTF8)
	}
	if rGo := CheckGoComments(repoRoot, relFiles); rGo.Status != StatusPassed {
		t.Fatalf("源码 Go AST 注释完整性自审未通过: %+v", rGo)
	}
	if rDB := CheckDatabaseComments(repoRoot, relFiles); rDB.Status == StatusFailed {
		t.Fatalf("源码数据库模型注释自审未通过: %+v", rDB)
	}
}


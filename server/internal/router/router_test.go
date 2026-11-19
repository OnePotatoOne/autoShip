// Package router_test 验证 autoShip Server 全量数据表模型注释合规性、双监听器边界物理隔离及客服完成门逻辑。
package router_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoship/internal/config"
	"autoship/internal/database"
	intakeSvc "autoship/internal/modules/intake/service"
	"autoship/internal/router"
	"autoship/pkg/pki"
)

// TestAllRegisteredModelsHaveTableAndColumnComments 强制验证全系统所有注册的数据表（30+ 张表）100% 包含表级注释与非空字段注释（开发规范第 2 条）。
func TestAllRegisteredModelsHaveTableAndColumnComments(t *testing.T) {
	cfgPath := t.TempDir() + "/server.yaml"
	cfg, encKey, err := config.LoadOrInit(cfgPath)
	if err != nil {
		t.Fatalf("加载默认配置失败: %v", err)
	}
	ca, err := pki.EnsureCA(t.TempDir() + "/pki")
	if err != nil {
		t.Fatalf("初始化测试 CA 失败: %v", err)
	}
	// 触发全部业务模块 init() 完成模型注册
	_ = router.Build(cfg, cfgPath, encKey, ca, database.Get)

	models := database.RegisteredModels()
	if len(models) < 30 {
		t.Fatalf("预期注册至少 30 张核心数据表，实际仅 %d 张", len(models))
	}
	if err := database.ValidateModelsMetadata(models); err != nil {
		t.Fatalf("数据库模型注释门禁不通过: %v", err)
	}
}

// TestDualListenerNetworkBoundary 验证后台管理接口不在公网监听器暴露，且公网监听器默认拦截非白名单来源 IP。
func TestDualListenerNetworkBoundary(t *testing.T) {
	cfgPath := t.TempDir() + "/server.yaml"
	cfg, encKey, err := config.LoadOrInit(cfgPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	ca, err := pki.EnsureCA(t.TempDir() + "/pki")
	if err != nil {
		t.Fatalf("初始化 CA 失败: %v", err)
	}
	app := router.Build(cfg, cfgPath, encKey, ca, database.Get)

	// 1. 公网引擎严禁暴露 /api/v1/admin/*，且非白名单 IP 直接返回 403
	reqPubAdmin := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/status", nil)
	reqPubAdmin.RemoteAddr = "203.0.113.99:54321"
	wPubAdmin := httptest.NewRecorder()
	app.PublicEngine.ServeHTTP(wPubAdmin, reqPubAdmin)
	if wPubAdmin.Code != http.StatusForbidden {
		t.Fatalf("非白名单 IP 访问公网端口期望 403，实际 %d", wPubAdmin.Code)
	}

	// 即使把 IP 加入公网白名单，公网引擎上也绝不存在管理后台路由
	cfg.PublicAllowIPs = []string{"203.0.113.99"}
	wPubAdminAllowedIP := httptest.NewRecorder()
	app.PublicEngine.ServeHTTP(wPubAdminAllowedIP, reqPubAdmin)
	if wPubAdminAllowedIP.Code != http.StatusNotFound {
		t.Fatalf("公网监听器不应存在管理后台路由，期望 404，实际 %d", wPubAdminAllowedIP.Code)
	}

	// 2. Tailnet 引擎未登录访问受保护管理接口返回 401
	reqTailProtected := httptest.NewRequest(http.MethodGet, "/api/v1/admin/products", nil)
	wTailProtected := httptest.NewRecorder()
	app.TailnetEngine.ServeHTTP(wTailProtected, reqTailProtected)
	if wTailProtected.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问管理接口期望 401，实际 %d", wTailProtected.Code)
	}
}

// TestIntakeCompletionGateAndPrototypeSanitizer 验证客服提交完成门拦截缺失必填项，且原型 HTML 剥离外链引用。
func TestIntakeCompletionGateAndPrototypeSanitizer(t *testing.T) {
	incompleteBug := intakeSvc.StructuredDraft{
		IssueType: "bug",
		Title:     "登录报错",
	}
	if missing := intakeSvc.ValidateCompletionGate(incompleteBug, 0); len(missing) == 0 {
		t.Fatalf("缺失必填项的缺陷草稿未被完成门拦截")
	}

	completeBug := intakeSvc.StructuredDraft{
		IssueType:         "bug",
		Title:             "Safari 浏览器下点击导出报表按钮报 500",
		ModuleCode:        "report",
		ReproSteps:        "1. 打开报表页 2. 选择本月范围 3. 点击导出",
		ExpectedResult:    "正常下载 Excel 文件",
		ActualResult:      "页面提示 500 服务器内部错误",
		Frequency:         "必现",
		ImpactScope:       "所有审计员角色",
		ErrorOrScreenshot: "接口返回 HTTP 500 traceId=abc",
	}
	if missing := intakeSvc.ValidateCompletionGate(completeBug, 0); len(missing) != 0 {
		t.Fatalf("完整缺陷草稿被误拦: %v", missing)
	}

	dirtyHTML := `<div><script src="https://evil.example.com/x.js"></script><link href='http://cdn.example.com/a.css'><span>按钮</span></div>`
	cleanHTML := intakeSvc.SanitizePrototypeHTML(dirtyHTML)
	if strings.Contains(cleanHTML, "evil.example.com") || strings.Contains(cleanHTML, "cdn.example.com") {
		t.Fatalf("原型 HTML 未彻底剥离外部资源引用: %s", cleanHTML)
	}
}

// TestExternalPortIsolationAndValidation 验证外部调用端口配置归一化、禁止与管理端同端口以及 /open/v1/ping 与 /open/v1/issues 仅挂载在外部引擎。
// 参数 t 为 Go 测试上下文；无返回值。
func TestExternalPortIsolationAndValidation(t *testing.T) {
	addr, err := config.NormalizeAndValidatePublicListenAddr("19443", "100.90.160.50:9444")
	if err != nil || addr != "0.0.0.0:19443" {
		t.Fatalf("纯端口号归一化失败: got=%q err=%v", addr, err)
	}
	if _, collisionErr := config.NormalizeAndValidatePublicListenAddr("9444", "100.90.160.50:9444"); collisionErr == nil {
		t.Fatalf("期望拦截与管理端相同的外部端口 9444，但未报错")
	}
	if _, collisionErr2 := config.NormalizeAndValidatePublicListenAddr("0.0.0.0:9444", "127.0.0.1:9444"); collisionErr2 == nil {
		t.Fatalf("期望拦截与管理端同端口的 0.0.0.0:9444，但未报错")
	}

	cfgPath := t.TempDir() + "/server.yaml"
	cfg, encKey, err := config.LoadOrInit(cfgPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	cfg.PublicAllowIPs = []string{"203.0.113.88"}
	ca, err := pki.EnsureCA(t.TempDir() + "/pki")
	if err != nil {
		t.Fatalf("初始化 CA 失败: %v", err)
	}
	app := router.Build(cfg, cfgPath, encKey, ca, database.Get)

	// PublicEngine 上存在 /open/v1/ping 与 /open/v1/issues（未带签名时返回 401 而非 404）
	for _, p := range []string{"/open/v1/ping", "/open/v1/issues"} {
		method := http.MethodGet
		if p == "/open/v1/issues" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, p, nil)
		req.RemoteAddr = "203.0.113.88:50000"
		w := httptest.NewRecorder()
		app.PublicEngine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("外部端口路由 %s 期望命中 HMAC 鉴权返回 401，实际 %d", p, w.Code)
		}
	}
}

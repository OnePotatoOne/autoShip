package gates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// PageInspectionRecord 记录单个目标页面经本地 Chrome CDP 访问后的截图、控制台错误与失败接口统计。
type PageInspectionRecord struct {
	URL            string   `json:"url"`            // 访问的完整页面地址
	ScreenshotPath string   `json:"screenshotPath"` // 保存的全页 PNG 截图绝对路径
	ConsoleErrors  []string `json:"consoleErrors"`  // 捕获到的 console.error 与未捕获 JS 异常
	FailedRequests []string `json:"failedRequests"` // 捕获到的网络加载失败或 HTTP 5xx 接口列表
}

// CDPInspector 使用本机已安装的 Google Chrome（独立临时配置目录，绝不下载浏览器）执行页面巡检与截图采集（8.2 & 8.5 & 8.7 节）。
type CDPInspector struct {
	chromeBin string
	timeout   time.Duration
}

// NewCDPInspector 创建绑定本机 Chrome 可执行文件的页面巡检器。
// 参数 chromeBinaryOverride 为可选显式 Chrome 路径（留空自动在 macOS / Linux 标准安装位置探测），timeout 为单页巡检超时；返回 *CDPInspector。
func NewCDPInspector(chromeBinaryOverride string, timeout time.Duration) *CDPInspector {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	bin := strings.TrimSpace(chromeBinaryOverride)
	if bin == "" {
		bin = LocateLocalChromeBinary()
	}
	return &CDPInspector{
		chromeBin: bin,
		timeout:   timeout,
	}
}

// LocateLocalChromeBinary 探测宿主机器已安装的 Google Chrome 可执行文件路径（绝不联网下载浏览器以遵守 10MB 体积规范）。
// 无入参；找到返回可执行文件绝对路径，未安装返回空字符串。
func LocateLocalChromeBinary() string {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	for _, cmdName := range []string{"google-chrome", "google-chrome-stable", "chromium"} {
		if path, err := exec.LookPath(cmdName); err == nil {
			return path
		}
	}
	return ""
}

// Available 判断本机是否存在可用的 Google Chrome 供 CDP 页面巡检调用。
// 无入参；存在返回 true，否则返回 false。
func (c *CDPInspector) Available() bool {
	if c == nil || c.chromeBin == "" {
		return false
	}
	st, err := os.Stat(c.chromeBin)
	return err == nil && !st.IsDir()
}

// InspectPages 启动带临时独立用户目录的无头 Chrome，依次访问 urls 抓图并收集控制台报错和失败网络请求，与基线对比生成门禁结果。
// 参数 ctx 为上下文，urls 为待巡检完整 URL 列表，screenshotDir 为截图输出目录，baselineErrCount 为主分支允许的历史基线错误数（默认传 nil）；返回巡检明细切片、门禁 Result 及底层错误。
func (c *CDPInspector) InspectPages(ctx context.Context, urls []string, screenshotDir string, baselineErrCount map[string]int) ([]PageInspectionRecord, Result, error) {
	if len(urls) == 0 {
		return nil, Result{
			GateName: "cdp_page_inspection",
			Status:   StatusSkipped,
			Summary:  "产品档案未声明待巡检页面路径",
		}, nil
	}
	if !c.Available() {
		return nil, Result{
			GateName: "cdp_page_inspection",
			Status:   StatusSkipped,
			Summary:  "本机未检测到已安装的 Google Chrome，跳过 CDP 页面巡检",
		}, nil
	}
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		return nil, Result{}, fmt.Errorf("创建截图目录失败: %w", err)
	}

	tempProfileDir, err := os.MkdirTemp("", "autoship-chrome-profile-*")
	if err != nil {
		return nil, Result{}, fmt.Errorf("创建临时浏览器配置目录失败: %w", err)
	}
	defer os.RemoveAll(tempProfileDir)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(c.chromeBin),
		chromedp.UserDataDir(tempProfileDir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Headless,
		chromedp.IgnoreCertErrors,
		chromedp.WindowSize(1440, 900),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	var records []PageInspectionRecord
	var violations []Violation

	for idx, targetURL := range urls {
		rec, err := c.inspectSinglePage(allocCtx, idx, targetURL, screenshotDir)
		if err != nil {
			violations = append(violations, Violation{
				Rule:    "cdp.navigate.failed",
				Symbol:  targetURL,
				Message: fmt.Sprintf("访问页面 %s 失败: %v", targetURL, err),
			})
			continue
		}
		records = append(records, rec)

		totalIssues := len(rec.ConsoleErrors) + len(rec.FailedRequests)
		allowed := 0
		if baselineErrCount != nil {
			allowed = baselineErrCount[targetURL]
		}
		if totalIssues > allowed {
			violations = append(violations, Violation{
				Rule:   "cdp.page.errors",
				Symbol: targetURL,
				Message: fmt.Sprintf("页面 %s 新增运行期错误（控制台报错 %d 条，失败请求 %d 条，高于基线 %d）: %s",
					targetURL, len(rec.ConsoleErrors), len(rec.FailedRequests), allowed,
					strings.Join(append(rec.ConsoleErrors, rec.FailedRequests...), "; ")),
			})
		}
	}

	if len(violations) > 0 {
		return records, Result{
			GateName:   "cdp_page_inspection",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("CDP 页面巡检未通过：%d 个页面出现新增控制台报错或接口失败", len(violations)),
			Violations: violations,
		}, nil
	}
	return records, Result{
		GateName: "cdp_page_inspection",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已完成 %d 个页面的 Chrome CDP 巡检与全页截图，无新增报错", len(records)),
	}, nil
}

// inspectSinglePage 在独立标签页上下文中打开目标 URL、订阅 Console 与 Network 事件流并导出 PNG 截图。
func (c *CDPInspector) inspectSinglePage(allocCtx context.Context, index int, targetURL, screenshotDir string) (PageInspectionRecord, error) {
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	defer cancelTab()
	timedCtx, cancelTimeout := context.WithTimeout(tabCtx, c.timeout)
	defer cancelTimeout()

	// 先订阅事件迭代器，保证导航期间产生的所有控制台报错与网络失败请求零丢失
	consoleSeq := chromedp.Console(timedCtx)
	reqSentSeq := chromedp.Events(timedCtx, network.RequestWillBeSent)
	loadFailSeq := chromedp.Events(timedCtx, network.LoadingFailed)
	respRecvSeq := chromedp.Events(timedCtx, network.ResponseReceived)

	var mu sync.Mutex
	var consoleErrs []string
	var netFailures []string
	reqURLs := make(map[network.RequestID]string)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		for msg, err := range consoleSeq {
			if err != nil {
				return
			}
			if msg.IsException() || strings.EqualFold(string(msg.Type), "error") {
				mu.Lock()
				consoleErrs = append(consoleErrs, fmt.Sprintf("%s: %s", msg.Type, msg.Text))
				mu.Unlock()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range reqSentSeq {
			if err != nil {
				return
			}
			if ev.Request != nil {
				mu.Lock()
				reqURLs[ev.RequestID] = ev.Request.URL
				mu.Unlock()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range loadFailSeq {
			if err != nil {
				return
			}
			mu.Lock()
			u := reqURLs[ev.RequestID]
			netFailures = append(netFailures, fmt.Sprintf("network failed (%s): %s", ev.ErrorText, u))
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for ev, err := range respRecvSeq {
			if err != nil {
				return
			}
			if ev.Response != nil && ev.Response.Status >= 500 {
				mu.Lock()
				netFailures = append(netFailures, fmt.Sprintf("HTTP %d: %s", ev.Response.Status, ev.Response.URL))
				mu.Unlock()
			}
		}
	}()

	_, _ = chromedp.Call(timedCtx, network.Enable, network.EnableParams{})
	if err := chromedp.Do(timedCtx,
		chromedp.Navigate(targetURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(250*time.Millisecond),
	); err != nil {
		cancelTimeout()
		wg.Wait()
		return PageInspectionRecord{URL: targetURL}, err
	}

	pngBuf, err := chromedp.Run(timedCtx, chromedp.FullScreenshot(100))
	cancelTimeout()
	wg.Wait()
	if err != nil {
		return PageInspectionRecord{URL: targetURL}, err
	}

	shotFile := filepath.Join(screenshotDir, fmt.Sprintf("page_%02d.png", index+1))
	if writeErr := os.WriteFile(shotFile, pngBuf, 0644); writeErr != nil {
		return PageInspectionRecord{URL: targetURL}, writeErr
	}

	mu.Lock()
	defer mu.Unlock()
	return PageInspectionRecord{
		URL:            targetURL,
		ScreenshotPath: shotFile,
		ConsoleErrors:  consoleErrs,
		FailedRequests: netFailures,
	}, nil
}

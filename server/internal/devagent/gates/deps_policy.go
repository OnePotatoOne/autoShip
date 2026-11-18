package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// MinDependencyAgeDays 是新引入或变更依赖版本必须满足的最小发布天数（开发规范第 4 条：30 天）。
	MinDependencyAgeDays = 30
	// MaxDependencySizeBytes 是拟引入组件允许的最大体积字节上限（开发规范第 5 条：10 MB）。
	MaxDependencySizeBytes int64 = 10 * 1024 * 1024
)

// DependencyChange 描述从 go.mod 或 package.json 变更对比中提取的单个第三方依赖变动项。
type DependencyChange struct {
	Ecosystem  string `json:"ecosystem"`  // "go" 或 "npm"
	FilePath   string `json:"filePath"`   // 所在清单相对路径（如 go.mod 或 web/package.json）
	Package    string `json:"package"`    // 模块路径或 npm 包名
	OldVersion string `json:"oldVersion"` // 变更前版本（新增依赖时为空）
	NewVersion string `json:"newVersion"` // 变更后目标版本
	Action     string `json:"action"`     // "added"（新增）或 "upgraded"（升级/变更已有依赖）
}

// PackageReleaseMeta 描述从官方包仓库（proxy.golang.org / registry.npmjs.org）查询到的版本发布时间、体积及 LTS 信息。
type PackageReleaseMeta struct {
	PublishedAt time.Time `json:"publishedAt"` // 该版本的官方发布时间
	SizeBytes   int64     `json:"sizeBytes"`   // 组件压缩包或解包字节体积
	HasLTS      bool      `json:"hasLts"`      // 该包是否存在官方 LTS 通道（如 npm dist-tags.lts）
	IsLTS       bool      `json:"isLts"`       // 当前所选版本是否命中 LTS
}

// MetadataFetcher 抽象查询 Go Module 或 npm 包版本发布时间与体积元数据的接口，支持本地代理与单测 Mock 注入。
type MetadataFetcher interface {
	// FetchMeta 查询指定生态（go/npm）包特定版本的发布时间与字节体积。
	FetchMeta(ctx context.Context, ecosystem, pkgName, version string) (*PackageReleaseMeta, error)
}

// HTTPRegistryFetcher 是基于标准 HTTP 客户端（可选本地代理）访问 proxy.golang.org 与 registry.npmjs.org 的默认实现。
type HTTPRegistryFetcher struct {
	client *http.Client
}

// NewHTTPRegistryFetcher 创建带超时与可选 HTTP 代理的官方包仓库元数据查询客户端。
// 参数 proxyURL 为可选代理地址（如 http://127.0.0.1:7890），timeout 为单次请求超时；返回 *HTTPRegistryFetcher。
func NewHTTPRegistryFetcher(proxyURL string, timeout time.Duration) *HTTPRegistryFetcher {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tr := &http.Transport{}
	if strings.TrimSpace(proxyURL) != "" {
		if parsed, err := url.Parse(strings.TrimSpace(proxyURL)); err == nil {
			tr.Proxy = http.ProxyURL(parsed)
		}
	}
	return &HTTPRegistryFetcher{
		client: &http.Client{Transport: tr, Timeout: timeout},
	}
}

// FetchMeta 从 proxy.golang.org 或 registry.npmjs.org 获取目标版本的发布时间与包体积。
// 参数 ctx 为上下文，ecosystem 为 "go" 或 "npm"，pkgName 为包名，version 为版本号；返回 *PackageReleaseMeta 及网络/解析错误。
func (f *HTTPRegistryFetcher) FetchMeta(ctx context.Context, ecosystem, pkgName, version string) (*PackageReleaseMeta, error) {
	switch ecosystem {
	case "go":
		return f.fetchGoModuleMeta(ctx, pkgName, version)
	case "npm":
		return f.fetchNpmPackageMeta(ctx, pkgName, version)
	default:
		return nil, fmt.Errorf("未知依赖生态: %s", ecosystem)
	}
}

// fetchGoModuleMeta 查询 proxy.golang.org 的 `.info` 时间戳与 `.zip` Content-Length 体积。
func (f *HTTPRegistryFetcher) fetchGoModuleMeta(ctx context.Context, modPath, ver string) (*PackageReleaseMeta, error) {
	escapedMod := escapeGoModulePath(modPath)
	infoURL := fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.info", escapedMod, url.PathEscape(ver))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Go Proxy 返回 HTTP %d", resp.StatusCode)
	}
	var info struct {
		Time time.Time `json:"Time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	var sizeBytes int64
	zipURL := fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.zip", escapedMod, url.PathEscape(ver))
	if headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, zipURL, nil); err == nil {
		if headResp, err := f.client.Do(headReq); err == nil {
			sizeBytes = headResp.ContentLength
			_ = headResp.Body.Close()
		}
	}
	return &PackageReleaseMeta{
		PublishedAt: info.Time,
		SizeBytes:   sizeBytes,
	}, nil
}

// fetchNpmPackageMeta 查询 registry.npmjs.org 包文档中的 time[ver]、dist.unpackedSize 与 dist-tags.lts。
func (f *HTTPRegistryFetcher) fetchNpmPackageMeta(ctx context.Context, pkgName, rawVer string) (*PackageReleaseMeta, error) {
	cleanVer := strings.TrimLeft(strings.TrimSpace(rawVer), "^~>=<v ")
	regURL := fmt.Sprintf("https://registry.npmjs.org/%s", pkgName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, regURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm registry 返回 HTTP %d", resp.StatusCode)
	}

	var doc struct {
		DistTags map[string]string            `json:"dist-tags"`
		Time     map[string]string            `json:"time"`
		Versions map[string]struct {
			Dist struct {
				UnpackedSize int64 `json:"unpackedSize"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	tsRaw, ok := doc.Time[cleanVer]
	if !ok {
		return nil, fmt.Errorf("npm 包 %s 未找到版本 %s 的发布时间", pkgName, cleanVer)
	}
	pubTime, err := time.Parse(time.RFC3339, tsRaw)
	if err != nil {
		return nil, err
	}
	ltsVer := doc.DistTags["lts"]
	return &PackageReleaseMeta{
		PublishedAt: pubTime,
		SizeBytes:   doc.Versions[cleanVer].Dist.UnpackedSize,
		HasLTS:      ltsVer != "",
		IsLTS:       ltsVer != "" && ltsVer == cleanVer,
	}, nil
}

// CheckDependencyPolicy 对比基准版本与当前工作树的 `go.mod` / `package.json`，校验不擅自升级已有依赖、新版本发布满 30 天、优先 LTS 且单组件体积不超过 10MB（超限或存疑一律触发 ask_owner 暂停上报，8.7 节）。
// 参数 ctx 为上下文，changes 为解析出的依赖变动切片，fetcher 为元数据查询器，now 为当前校对时间；返回门禁 Result。
func CheckDependencyPolicy(ctx context.Context, changes []DependencyChange, fetcher MetadataFetcher, now time.Time) Result {
	if len(changes) == 0 {
		return Result{
			GateName: "dependency_policy",
			Status:   StatusSkipped,
			Summary:  "本次改动未新增或升级任何第三方依赖",
		}
	}

	var violations []Violation
	var prompts []string

	for _, chg := range changes {
		// 规则 4：严禁擅自升级已有依赖，必须飞书上报经人工确认
		if chg.Action == "upgraded" {
			msg := fmt.Sprintf("检测到擅自变更已有依赖 %s (%s -> %s)，按规范须暂停并经人工确认", chg.Package, chg.OldVersion, chg.NewVersion)
			violations = append(violations, Violation{
				Rule:    "deps.upgrade.requires_owner",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
		}

		if fetcher == nil {
			msg := fmt.Sprintf("新增/变更依赖 %s@%s 无法离线核验发布时间与体积，请人工确认是否允许引入", chg.Package, chg.NewVersion)
			violations = append(violations, Violation{
				Rule:    "deps.meta.unverified",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
			continue
		}

		meta, err := fetcher.FetchMeta(ctx, chg.Ecosystem, chg.Package, chg.NewVersion)
		if err != nil {
			msg := fmt.Sprintf("查询依赖 %s@%s 元数据失败 (%v)，须人工确认发布时间满 30 天且体积 ≤ 10MB", chg.Package, chg.NewVersion, err)
			violations = append(violations, Violation{
				Rule:    "deps.meta.fetch_failed",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
			continue
		}

		// 规则 5：体积超过 10 MB 立即停止开发并飞书上报
		if meta.SizeBytes > MaxDependencySizeBytes {
			mb := float64(meta.SizeBytes) / (1024.0 * 1024.0)
			msg := fmt.Sprintf("拟引入组件 %s@%s 体积为 %.2f MB，超过 10 MB 硬上限，已立即暂停开发并上报授权", chg.Package, chg.NewVersion, mb)
			violations = append(violations, Violation{
				Rule:    "deps.size_over_10mb",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
		}

		// 规则 4：禁止使用发布未满 30 天的版本
		age := now.Sub(meta.PublishedAt)
		if age < MinDependencyAgeDays*24*time.Hour {
			days := int(age.Hours() / 24)
			msg := fmt.Sprintf("依赖 %s@%s 发布于 %s（仅满 %d 天，未满 30 天要求），须暂停并经人工确认",
				chg.Package, chg.NewVersion, meta.PublishedAt.Format("2006-01-02"), days)
			violations = append(violations, Violation{
				Rule:    "deps.age_under_30_days",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
		}

		// 规则 4：存在 LTS 通道却未选 LTS 时提示上报
		if meta.HasLTS && !meta.IsLTS {
			msg := fmt.Sprintf("依赖 %s 存在官方 LTS 版本但当前选择了非 LTS 版本 %s，请确认", chg.Package, chg.NewVersion)
			violations = append(violations, Violation{
				Rule:    "deps.non_lts",
				File:    chg.FilePath,
				Symbol:  chg.Package,
				Message: msg,
			})
			prompts = append(prompts, msg)
		}
	}

	if len(violations) > 0 {
		return Result{
			GateName:       "dependency_policy",
			Status:         StatusAskOwner,
			Summary:        fmt.Sprintf("依赖策略检查触发人工确认（共 %d 项超限或升级变更）", len(violations)),
			Violations:     violations,
			AskOwnerPrompt: strings.Join(prompts, "\n"),
		}
	}
	return Result{
		GateName: "dependency_policy",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已通过 %d 项新增依赖的发布时间（≥30天）与体积（≤10MB）策略核验", len(changes)),
	}
}

// DiffGoModContents 解析修改前后的 go.mod 文本，识别新增或版本变更的直接/间接模块依赖。
// 参数 filePath 为文件相对路径，oldContent 与 newContent 为前后文本；返回 []DependencyChange。
func DiffGoModContents(filePath, oldContent, newContent string) []DependencyChange {
	oldMap := parseGoModRequires(oldContent)
	newMap := parseGoModRequires(newContent)
	var out []DependencyChange
	for pkg, newVer := range newMap {
		oldVer, existed := oldMap[pkg]
		if !existed {
			out = append(out, DependencyChange{
				Ecosystem:  "go",
				FilePath:   filePath,
				Package:    pkg,
				NewVersion: newVer,
				Action:     "added",
			})
		} else if oldVer != newVer {
			out = append(out, DependencyChange{
				Ecosystem:  "go",
				FilePath:   filePath,
				Package:    pkg,
				OldVersion: oldVer,
				NewVersion: newVer,
				Action:     "upgraded",
			})
		}
	}
	return out
}

// DiffPackageJSONContents 解析修改前后的 package.json 文本，识别 dependencies 与 devDependencies 中的新增或升级项。
// 参数 filePath 为文件相对路径，oldContent 与 newContent 为前后 JSON 文本；返回 []DependencyChange。
func DiffPackageJSONContents(filePath, oldContent, newContent string) []DependencyChange {
	oldMap := parsePackageJSONDeps(oldContent)
	newMap := parsePackageJSONDeps(newContent)
	var out []DependencyChange
	for pkg, newVer := range newMap {
		oldVer, existed := oldMap[pkg]
		if !existed {
			out = append(out, DependencyChange{
				Ecosystem:  "npm",
				FilePath:   filePath,
				Package:    pkg,
				NewVersion: newVer,
				Action:     "added",
			})
		} else if oldVer != newVer {
			out = append(out, DependencyChange{
				Ecosystem:  "npm",
				FilePath:   filePath,
				Package:    pkg,
				OldVersion: oldVer,
				NewVersion: newVer,
				Action:     "upgraded",
			})
		}
	}
	return out
}

// parseGoModRequires 提取 go.mod 中所有 require 行与 require (...) 块中的模块名与版本。
func parseGoModRequires(content string) map[string]string {
	res := make(map[string]string)
	inRequireBlock := false
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.Split(rawLine, "//")[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "require (") || line == "require(" {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}
		if strings.HasPrefix(line, "require ") {
			fields := strings.Fields(strings.TrimPrefix(line, "require "))
			if len(fields) >= 2 {
				res[fields[0]] = fields[1]
			}
			continue
		}
		if inRequireBlock {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				res[fields[0]] = fields[1]
			}
		}
	}
	return res
}

// parsePackageJSONDeps 解析 package.json 中的 dependencies 与 devDependencies 键值表。
func parsePackageJSONDeps(content string) map[string]string {
	res := make(map[string]string)
	if strings.TrimSpace(content) == "" {
		return res
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(content), &pkg); err != nil {
		return res
	}
	for k, v := range pkg.Dependencies {
		res[k] = v
	}
	for k, v := range pkg.DevDependencies {
		res[k] = v
	}
	return res
}

// escapeGoModulePath 按 Go Module Proxy 规范将大写字母转义为 `!小写`。
func escapeGoModulePath(modPath string) string {
	var b strings.Builder
	for _, r := range modPath {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

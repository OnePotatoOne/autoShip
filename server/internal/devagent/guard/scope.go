// Package guard 实现开发引擎的工具调用安全守卫（8.8 节）、文件三区范围划分与最终 git diff 改动范围精确计算，以及公共区改动的受影响模块静态引用分析（8.5 节）。
package guard

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"autoship/internal/devagent/config"
)

// Zone 表示产品代码路径在单次开发任务中的改动范围分区枚举（8.5 节）。
type Zone string

const (
	// ZoneCurrentModule 表示需求归属的本模块区（含本模块测试），允许自由修改。
	ZoneCurrentModule Zone = "current_module"
	// ZoneOtherModule 表示本模块之外的其他业务模块区，修改时逐项标记并附改动理由。
	ZoneOtherModule Zone = "other_module"
	// ZonePublic 表示公共库、共享组件、依赖锁文件及构建部署脚本所在的公共区，修改时打特殊标记、附理由并强制追加系统性影响审计。
	ZonePublic Zone = "public_zone"
)

// PublicBadge 是公共区改动在活动流与验收包第一屏展示的固定特殊标记。
const PublicBadge = "PUBLIC_ZONE_AUDIT_REQUIRED"

// builtinPublicNames 定义即使产品档案未显式列出也一律归入公共区的依赖与构建配置文件名。
var builtinPublicNames = map[string]bool{
	"go.mod":             true,
	"go.sum":             true,
	"package.json":       true,
	"package-lock.json":  true,
	"pnpm-lock.yaml":     true,
	"yarn.lock":          true,
	"makefile":           true,
	"dockerfile":         true,
	"docker-compose.yml": true,
}

// builtinPublicPrefixes 定义默认视为公共区的目录前缀。
var builtinPublicPrefixes = []string{
	".github/",
	"deploy/",
}

// FileDiffStat 记录单个改动文件在 git diff 中的状态、所属分区与增删行数。
type FileDiffStat struct {
	FilePath     string `json:"filePath"`     // 仓库内相对路径（使用正斜杠）
	ChangeStatus string `json:"changeStatus"` // A(新增) / M(修改) / D(删除) / R(重命名)
	Zone         Zone   `json:"zone"`         // 所属三区枚举
	ModuleName   string `json:"moduleName"`   // 匹配到的模块名称（公共区为 __public__）
	AddedLines   int    `json:"addedLines"`   // 新增代码行数
	DeletedLines int    `json:"deletedLines"` // 删除代码行数
	Reason       string `json:"reason"`       // 写文件阶段记录的跨模块或公共区改动理由
}

// ModuleScopeStat 汇总单个模块在本次任务中的改动文件数与总增删行数（供验收包第一屏展示）。
type ModuleScopeStat struct {
	ModuleName   string         `json:"moduleName"`   // 模块名
	Zone         Zone           `json:"zone"`         // 该模块对应的分区
	FileCount    int            `json:"fileCount"`    // 改动文件总数
	AddedLines   int            `json:"addedLines"`   // 模块新增总行数
	DeletedLines int            `json:"deletedLines"` // 模块删除总行数
	Files        []FileDiffStat `json:"files"`        // 该模块下改动的具体文件明细
}

// MarkedScopeChange 表示单列在验收包第一屏的其他模块改动项或公共区特殊标记改动项。
type MarkedScopeChange struct {
	FilePath     string `json:"filePath"`     // 仓库内相对路径
	ModuleName   string `json:"moduleName"`   // 所属模块名
	Zone         Zone   `json:"zone"`         // other_module 或 public_zone
	SpecialBadge string `json:"specialBadge"` // 公共区专用特殊标记（PUBLIC_ZONE_AUDIT_REQUIRED）
	ChangeStatus string `json:"changeStatus"` // 变更状态码
	AddedLines   int    `json:"addedLines"`   // 新增行数
	DeletedLines int    `json:"deletedLines"` // 删除行数
	Reason       string `json:"reason"`       // 模型给出的改动理由（未填写时标注缺失告警）
}

// ScopeReport 是由代码对最终 git diff 精确计算出的完整改动范围报告（驱动验收包第一屏、飞书摘要与公共区审计门）。
type ScopeReport struct {
	TargetModule          string              `json:"targetModule"`          // 本需求归属的目标模块名
	TotalFiles            int                 `json:"totalFiles"`            // 总改动文件数
	TotalAddedLines       int                 `json:"totalAddedLines"`       // 总新增行数
	TotalDeletedLines     int                 `json:"totalDeletedLines"`     // 总删除行数
	ByModule              []ModuleScopeStat   `json:"byModule"`              // 按模块分组的文件数与行数统计
	OtherModuleChanges    []MarkedScopeChange `json:"otherModuleChanges"`    // 其他模块改动单列清单及理由
	PublicZoneChanges     []MarkedScopeChange `json:"publicZoneChanges"`     // 公共区改动单列清单、特殊标记及理由
	TouchedOtherModules   []string            `json:"touchedOtherModules"`   // 涉及的其他模块名称去重列表
	RequiresSystemicAudit bool                `json:"requiresSystemicAudit"` // 是否因涉及公共区而必须执行系统性影响审计
	FeishuSummary         string              `json:"feishuSummary"`         // 飞书通知一行摘要文案
}

// AffectedModule 描述在公共区改动后通过代码静态引用分析得出的受连带影响业务模块（8.5 节）。
type AffectedModule struct {
	ModuleName            string   `json:"moduleName"`            // 受影响业务模块名
	Directories           []string `json:"directories"`           // 模块包含的相对目录列表
	ReferencedPublicFiles []string `json:"referencedPublicFiles"` // 被该模块直接引用或全局波及的公共区文件
	Reason                string   `json:"reason"`                // 判定受影响的静态依据说明
}

// ScopeAnalyzer 维护产品模块范围规则与运行时写入理由，提供路径三区判定、git diff 统计与受影响模块计算能力。
type ScopeAnalyzer struct {
	product      config.ProductProfile
	targetModule string
	mu           sync.RWMutex
	writeReasons map[string]string // clean slash relPath -> reason
}

// NewScopeAnalyzer 根据产品档案与本需求归属的目标模块名创建范围分析器。
// 参数 product 为产品档案配置，targetModule 为需求所属模块标识；返回 *ScopeAnalyzer。
func NewScopeAnalyzer(product config.ProductProfile, targetModule string) *ScopeAnalyzer {
	return &ScopeAnalyzer{
		product:      product,
		targetModule: strings.TrimSpace(targetModule),
		writeReasons: make(map[string]string),
	}
}

// ClassifyPath 将仓库内相对路径划分为 current_module / other_module / public_zone 三区之一。
// 参数 relPath 为仓库内相对路径；返回分区 Zone 与归属模块名称（公共区固定返回 "__public__"）。
func (s *ScopeAnalyzer) ClassifyPath(relPath string) (Zone, string) {
	clean := normalizeRelSlash(relPath)
	if clean == "" {
		return ZoneCurrentModule, s.defaultModuleLabel()
	}

	// 1. 优先判定公共区（内置依赖/构建文件 + 产品档案声明的公共目录），确保公共改动绝不漏审
	baseLower := strings.ToLower(filepath.Base(clean))
	if builtinPublicNames[baseLower] {
		return ZonePublic, "__public__"
	}
	for _, pfx := range builtinPublicPrefixes {
		if strings.HasPrefix(clean, pfx) {
			return ZonePublic, "__public__"
		}
	}
	for _, pubDir := range s.product.PublicZoneDirs {
		normPub := strings.TrimSuffix(normalizeRelSlash(pubDir), "/")
		if normPub != "" && (clean == normPub || strings.HasPrefix(clean, normPub+"/")) {
			return ZonePublic, "__public__"
		}
	}

	// 2. 按产品模块目录前缀匹配业务模块（取最长前缀匹配以支持嵌套目录）
	matchedMod := ""
	bestLen := -1
	for modName, dirs := range s.product.Modules {
		for _, dir := range dirs {
			normDir := strings.TrimSuffix(normalizeRelSlash(dir), "/")
			if normDir == "" {
				continue
			}
			if clean == normDir || strings.HasPrefix(clean, normDir+"/") {
				if len(normDir) > bestLen {
					bestLen = len(normDir)
					matchedMod = modName
				}
			}
		}
	}

	if matchedMod != "" {
		if s.targetModule == "" || matchedMod == s.targetModule {
			return ZoneCurrentModule, matchedMod
		}
		return ZoneOtherModule, matchedMod
	}

	// 3. 未命中任何已声明目录：若指定了具体目标模块且产品配置了模块表，则归入未分类其他模块以提示人工关注
	if s.targetModule != "" && len(s.product.Modules) > 0 {
		return ZoneOtherModule, "unclassified"
	}
	return ZoneCurrentModule, s.defaultModuleLabel()
}

// RecordWriteReason 记录模型在修改文件时附带的跨模块或公共区改动理由。
// 参数 relPath 为仓库相对路径，reason 为模型填写的修改原因；无返回值。
func (s *ScopeAnalyzer) RecordWriteReason(relPath, reason string) {
	clean := normalizeRelSlash(relPath)
	trimmed := strings.TrimSpace(reason)
	if clean == "" || trimmed == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeReasons[clean] = trimmed
}

// ReasonFor 读取此前写文件阶段记录的某相对路径改动理由。
// 参数 relPath 为仓库相对路径；返回已登记理由（未登记时返回空串）。
func (s *ScopeAnalyzer) ReasonFor(relPath string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writeReasons[normalizeRelSlash(relPath)]
}

// ComputeGitDiffScope 在 worktree 中对照 baseRef 运行真实 git diff（含未跟踪新文件），由代码精确汇总验收包第一屏数据。
// 参数 ctx 为上下文，worktreeDir 为任务工作树路径，baseRef 为基准提交/分支（为空时取 HEAD）；返回 *ScopeReport 及执行错误。
func (s *ScopeAnalyzer) ComputeGitDiffScope(ctx context.Context, worktreeDir, baseRef string) (*ScopeReport, error) {
	ref := strings.TrimSpace(baseRef)
	if ref == "" {
		ref = "HEAD"
	}
	numstatOut, err := runGitInDir(ctx, worktreeDir, "diff", "--numstat", ref)
	if err != nil {
		return nil, err
	}
	nameStatusOut, err := runGitInDir(ctx, worktreeDir, "diff", "--name-status", ref)
	if err != nil {
		return nil, err
	}
	untrackedOut, _ := runGitInDir(ctx, worktreeDir, "ls-files", "--others", "--exclude-standard")

	var extraNumstat, extraStatus []string
	for _, line := range strings.Split(strings.TrimSpace(untrackedOut), "\n") {
		rel := strings.TrimSpace(line)
		if rel == "" {
			continue
		}
		lines := countFileLines(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		extraNumstat = append(extraNumstat, fmt.Sprintf("%d\t0\t%s", lines, rel))
		extraStatus = append(extraStatus, fmt.Sprintf("A\t%s", rel))
	}
	if len(extraNumstat) > 0 {
		numstatOut = strings.TrimSpace(numstatOut + "\n" + strings.Join(extraNumstat, "\n"))
		nameStatusOut = strings.TrimSpace(nameStatusOut + "\n" + strings.Join(extraStatus, "\n"))
	}
	return s.ComputeFromDiffOutputs(numstatOut, nameStatusOut), nil
}

// ComputeFromDiffOutputs 解析 `git diff --numstat` 与 `git diff --name-status` 原始文本生成确定性 ScopeReport（便于纯内存单测复用）。
// 参数 numstatText 与 nameStatusText 为 git 输出；返回完整计算的 *ScopeReport。
func (s *ScopeAnalyzer) ComputeFromDiffOutputs(numstatText, nameStatusText string) *ScopeReport {
	statusMap := make(map[string]string)
	for _, rawLine := range strings.Split(nameStatusText, "\n") {
		fields := strings.Split(strings.TrimSpace(rawLine), "\t")
		if len(fields) < 2 {
			continue
		}
		code := fields[0]
		if len(code) > 1 {
			code = code[:1]
		}
		targetPath := normalizeRelSlash(fields[len(fields)-1])
		statusMap[targetPath] = code
	}

	type modAgg struct {
		stat ModuleScopeStat
	}
	aggByMod := make(map[string]*modAgg)
	seenFile := make(map[string]bool)

	report := &ScopeReport{
		TargetModule: s.targetModule,
	}
	otherModSet := make(map[string]bool)

	for _, rawLine := range strings.Split(numstatText, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		relPath := normalizeRenamePath(parts[2])
		if relPath == "" || seenFile[relPath] {
			continue
		}
		seenFile[relPath] = true

		chgStatus := statusMap[relPath]
		if chgStatus == "" {
			chgStatus = "M"
		}
		zone, modName := s.ClassifyPath(relPath)
		reason := s.ReasonFor(relPath)
		if reason == "" && zone != ZoneCurrentModule {
			reason = "未提供显式改动说明（需人工复核）"
		}

		fStat := FileDiffStat{
			FilePath:     relPath,
			ChangeStatus: chgStatus,
			Zone:         zone,
			ModuleName:   modName,
			AddedLines:   added,
			DeletedLines: deleted,
			Reason:       reason,
		}

		agg, ok := aggByMod[modName]
		if !ok {
			agg = &modAgg{
				stat: ModuleScopeStat{
					ModuleName: modName,
					Zone:       zone,
				},
			}
			aggByMod[modName] = agg
		}
		agg.stat.FileCount++
		agg.stat.AddedLines += added
		agg.stat.DeletedLines += deleted
		agg.stat.Files = append(agg.stat.Files, fStat)

		report.TotalFiles++
		report.TotalAddedLines += added
		report.TotalDeletedLines += deleted

		switch zone {
		case ZoneOtherModule:
			otherModSet[modName] = true
			report.OtherModuleChanges = append(report.OtherModuleChanges, MarkedScopeChange{
				FilePath:     relPath,
				ModuleName:   modName,
				Zone:         ZoneOtherModule,
				ChangeStatus: chgStatus,
				AddedLines:   added,
				DeletedLines: deleted,
				Reason:       reason,
			})
		case ZonePublic:
			report.PublicZoneChanges = append(report.PublicZoneChanges, MarkedScopeChange{
				FilePath:     relPath,
				ModuleName:   modName,
				Zone:         ZonePublic,
				SpecialBadge: PublicBadge,
				ChangeStatus: chgStatus,
				AddedLines:   added,
				DeletedLines: deleted,
				Reason:       reason,
			})
		}
	}

	modKeys := make([]string, 0, len(aggByMod))
	for k := range aggByMod {
		modKeys = append(modKeys, k)
	}
	sort.Strings(modKeys)
	for _, k := range modKeys {
		item := aggByMod[k].stat
		sort.Slice(item.Files, func(i, j int) bool { return item.Files[i].FilePath < item.Files[j].FilePath })
		report.ByModule = append(report.ByModule, item)
	}

	for m := range otherModSet {
		report.TouchedOtherModules = append(report.TouchedOtherModules, m)
	}
	sort.Strings(report.TouchedOtherModules)

	report.RequiresSystemicAudit = len(report.PublicZoneChanges) > 0
	report.FeishuSummary = fmt.Sprintf(
		"本次改动共 %d 个文件（+%d/-%d 行），涉及 %d 个其他模块、%d 处公共区",
		report.TotalFiles, report.TotalAddedLines, report.TotalDeletedLines,
		len(report.TouchedOtherModules), len(report.PublicZoneChanges),
	)
	return report
}

var tsImportRe = regexp.MustCompile(`(?m)(?:from\s+['"]([^'"]+)['"]|import\s*\(\s*['"]([^'"]+)['"]\s*\))`)

// FindAffectedModules 在公共区发生改动后静态扫描全仓各业务模块的 Go import 与 TS/JS import，计算受连带影响的模块清单（8.5 节第 1 步）。
// 参数 worktreeDir 为工作树根目录，publicFiles 为被修改的公共区相对路径切片；返回按模块名排序的 []AffectedModule 及读取错误。
func (s *ScopeAnalyzer) FindAffectedModules(worktreeDir string, publicFiles []string) ([]AffectedModule, error) {
	if len(publicFiles) == 0 || len(s.product.Modules) == 0 {
		return nil, nil
	}

	cleanPub := make([]string, 0, len(publicFiles))
	globalBuildChanged := false
	pubGoPkgDirs := make(map[string][]string) // relDir -> []pubFile
	pubTSKeys := make(map[string]string)      // searchToken -> pubFile

	for _, pf := range publicFiles {
		cp := normalizeRelSlash(pf)
		if cp == "" {
			continue
		}
		cleanPub = append(cleanPub, cp)
		base := strings.ToLower(filepath.Base(cp))
		if builtinPublicNames[base] || strings.HasPrefix(cp, "deploy/") || strings.HasPrefix(cp, ".github/") {
			globalBuildChanged = true
		}
		dir := normalizeRelSlash(filepath.Dir(cp))
		if strings.HasSuffix(cp, ".go") && dir != "" && dir != "." {
			pubGoPkgDirs[dir] = append(pubGoPkgDirs[dir], cp)
		}
		if strings.HasSuffix(cp, ".ts") || strings.HasSuffix(cp, ".tsx") || strings.HasSuffix(cp, ".js") {
			noExt := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(cp, ".tsx"), ".ts"), ".js")
			pubTSKeys[noExt] = cp
			if dir != "" && dir != "." {
				pubTSKeys[dir] = cp
			}
		}
	}

	goModulePrefix, goRootRel := detectGoModuleRoot(worktreeDir)

	var affected []AffectedModule
	modNames := make([]string, 0, len(s.product.Modules))
	for m := range s.product.Modules {
		modNames = append(modNames, m)
	}
	sort.Strings(modNames)

	for _, modName := range modNames {
		modDirs := s.product.Modules[modName]
		if globalBuildChanged {
			affected = append(affected, AffectedModule{
				ModuleName:            modName,
				Directories:           modDirs,
				ReferencedPublicFiles: cleanPub,
				Reason:                "涉及全局依赖锁文件或构建/部署脚本变更，全模块均受潜在连带影响",
			})
			continue
		}

		refSet := make(map[string]bool)
		for _, relModDir := range modDirs {
			absModDir := filepath.Join(worktreeDir, filepath.FromSlash(relModDir))
			if st, err := os.Stat(absModDir); err != nil || !st.IsDir() {
				continue
			}
			_ = filepath.Walk(absModDir, func(path string, info os.FileInfo, walkErr error) error {
				if walkErr != nil || info.IsDir() {
					return nil
				}
				switch {
				case strings.HasSuffix(path, ".go"):
					checkGoFileImports(path, goModulePrefix, goRootRel, pubGoPkgDirs, refSet)
				case strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx") || strings.HasSuffix(path, ".js"):
					checkTSFileImports(path, pubTSKeys, refSet)
				}
				return nil
			})
		}

		if len(refSet) > 0 {
			var refs []string
			for r := range refSet {
				refs = append(refs, r)
			}
			sort.Strings(refs)
			affected = append(affected, AffectedModule{
				ModuleName:            modName,
				Directories:           modDirs,
				ReferencedPublicFiles: refs,
				Reason:                fmt.Sprintf("静态代码引用了被修改的公共区文件: %s", strings.Join(refs, ", ")),
			})
		}
	}
	return affected, nil
}

// defaultModuleLabel 返回未配置模块名时的默认标签。
func (s *ScopeAnalyzer) defaultModuleLabel() string {
	if s.targetModule != "" {
		return s.targetModule
	}
	return "default"
}

// normalizeRelSlash 将任意相对路径规范化为不带前导 `./` 或 `/` 的正斜杠形式。
func normalizeRelSlash(raw string) string {
	s := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

// normalizeRenamePath 处理 git diff --numstat 中形如 `a/{old => new}/b.go` 或 `old => new` 的重命名路径展示。
func normalizeRenamePath(raw string) string {
	if strings.Contains(raw, "=>") {
		if lBrace, rBrace := strings.Index(raw, "{"), strings.Index(raw, "}"); lBrace >= 0 && rBrace > lBrace {
			mid := raw[lBrace+1 : rBrace]
			parts := strings.Split(mid, "=>")
			if len(parts) == 2 {
				rebuilt := raw[:lBrace] + strings.TrimSpace(parts[1]) + raw[rBrace+1:]
				return normalizeRelSlash(rebuilt)
			}
		}
		parts := strings.Split(raw, "=>")
		return normalizeRelSlash(parts[len(parts)-1])
	}
	return normalizeRelSlash(raw)
}

// runGitInDir 在给定工作树目录下执行 git 命令并返回输出文本。
func runGitInDir(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s 失败: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// countFileLines 统计工作树新增未跟踪文件的文本行数。
func countFileLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// detectGoModuleRoot 探测仓库根目录或 server/ 子目录下的 go.mod 模块路径前缀及其相对目录。
func detectGoModuleRoot(worktreeDir string) (modulePath string, relSubdir string) {
	for _, sub := range []string{"", "server", "backend"} {
		gomodFile := filepath.Join(worktreeDir, sub, "go.mod")
		data, err := os.ReadFile(gomodFile)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "module ") {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "module ")), normalizeRelSlash(sub)
			}
		}
	}
	return "", ""
}

// checkGoFileImports 使用 go/parser 解析单个 Go 源文件的 import 列表并比对是否命中被改动的公共包目录。
func checkGoFileImports(filePath, modPrefix, goRootRel string, pubGoPkgDirs map[string][]string, refSet map[string]bool) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return
	}
	for _, imp := range astFile.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		for pubDir, files := range pubGoPkgDirs {
			relToGoRoot := pubDir
			if goRootRel != "" && strings.HasPrefix(pubDir, goRootRel+"/") {
				relToGoRoot = strings.TrimPrefix(pubDir, goRootRel+"/")
			}
			expectedImport := relToGoRoot
			if modPrefix != "" {
				expectedImport = modPrefix + "/" + relToGoRoot
			}
			if impPath == expectedImport || strings.HasSuffix(impPath, "/"+relToGoRoot) {
				for _, f := range files {
					refSet[f] = true
				}
			}
		}
	}
}

// checkTSFileImports 扫描单个 TypeScript/JS 文件中的 import 路径并匹配是否引用了被改动的公共组件/工具。
func checkTSFileImports(filePath string, pubTSKeys map[string]string, refSet map[string]bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	matches := tsImportRe.FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		spec := m[1]
		if spec == "" && len(m) > 2 {
			spec = m[2]
		}
		cleanSpec := strings.TrimLeft(spec, "./@~")
		for key, origPubFile := range pubTSKeys {
			if cleanSpec != "" && (strings.HasSuffix(key, cleanSpec) || strings.Contains(spec, filepath.Base(key))) {
				refSet[origPubFile] = true
			}
		}
	}
}

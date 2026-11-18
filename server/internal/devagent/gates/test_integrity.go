package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// testSkipMarkersRe 匹配 Go 与 TS/JS 测试中新增的跳过指令（t.Skip / .skip / xit / xdescribe / xtest）
	testSkipMarkersRe = regexp.MustCompile(`\b(?:t\.(?:Skip|Skipf|SkipNow)|(?:it|test|describe)\.skip|xit|xdescribe|xtest)\s*\(`)
	// tsTestCaseTitleRe 匹配 TypeScript 测试文件中的 it('title', ...) 或 test('title', ...)
	tsTestCaseTitleRe = regexp.MustCompile(`\b(?:it|test)\s*\(\s*['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]`)
)

// TestFileDiffInput 描述单个测试文件在基准提交与当前工作树之间的前后内容对比输入。
type TestFileDiffInput struct {
	FilePath   string // 测试文件相对路径（如 service_test.go 或 app.spec.ts）
	OldContent string // 基准提交中的文件内容（新增测试文件时为空）
	NewContent string // 当前工作树中的文件内容（被删除时为空）
	Deleted    bool   // 该测试文件是否被删除
	Reason     string // 模型记录的修改既有测试理由
}

// CheckTestIntegrity 检查测试完整性：严禁删除既有测试文件、严禁删除既有测试函数/用例、严禁新增 t.Skip/.skip/xit，并对既有测试的所有修改逐项打标单列（8.5 & 8.7 节）。
// 参数 diffs 为所有涉及变动的测试文件前后快照列表；返回门禁 Result（包含 ModifiedTests 明细供验收包单列展示）。
func CheckTestIntegrity(diffs []TestFileDiffInput) Result {
	var relevant []TestFileDiffInput
	for _, d := range diffs {
		if IsTestFilePath(d.FilePath) {
			relevant = append(relevant, d)
		}
	}
	if len(relevant) == 0 {
		return Result{
			GateName: "test_integrity",
			Status:   StatusSkipped,
			Summary:  "本次改动未涉及测试文件变更",
		}
	}

	var violations []Violation
	var modifiedRecords []ModifiedTestRecord

	for _, item := range relevant {
		cleanPath := filepath.ToSlash(strings.TrimSpace(item.FilePath))

		// 1. 既有测试文件被删除：直接判失败
		if item.Deleted && strings.TrimSpace(item.OldContent) != "" {
			violations = append(violations, Violation{
				Rule:    "test.file.deleted",
				File:    cleanPath,
				Message: fmt.Sprintf("严禁删除既有测试文件 %s", cleanPath),
			})
			continue
		}

		// 2. 检测新增的测试跳过指令（对比新旧文件中 skip 出现次数或新增行）
		oldSkipCount := len(testSkipMarkersRe.FindAllStringIndex(item.OldContent, -1))
		newSkipCount := len(testSkipMarkersRe.FindAllStringIndex(item.NewContent, -1))
		if newSkipCount > oldSkipCount {
			violations = append(violations, Violation{
				Rule:    "test.skip.added",
				File:    cleanPath,
				Message: fmt.Sprintf("测试文件 %s 新增了测试跳过调用（t.Skip / .skip / xit / xdescribe），严禁通过跳过规避回归失败", cleanPath),
			})
		}

		// 若为全新增加的测试文件，只需确认无 skip 即可放行
		if strings.TrimSpace(item.OldContent) == "" {
			continue
		}

		// 3. 对比既有测试函数/用例是否被删除，以及哪些既有测试函数体发生了修改
		deletedSymbols, modifiedSymbols := compareTestSymbols(cleanPath, item.OldContent, item.NewContent)
		for _, sym := range deletedSymbols {
			violations = append(violations, Violation{
				Rule:    "test.case.deleted",
				File:    cleanPath,
				Symbol:  sym,
				Message: fmt.Sprintf("严禁删除既有测试用例/函数 %s", sym),
			})
		}

		// 4. 对既有测试文件的修改逐项标记并核查理由
		if item.OldContent != item.NewContent {
			reason := strings.TrimSpace(item.Reason)
			if reason == "" {
				reason = "未附说明（需人工核对既有测试变更意图）"
			}
			modifiedRecords = append(modifiedRecords, ModifiedTestRecord{
				FilePath: cleanPath,
				Symbols:  modifiedSymbols,
				Reason:   reason,
			})
		}
	}

	if len(violations) > 0 {
		return Result{
			GateName:      "test_integrity",
			Status:        StatusFailed,
			Summary:       fmt.Sprintf("测试完整性门禁未通过：发现 %d 处删除测试或新增跳过指令", len(violations)),
			Violations:    violations,
			ModifiedTests: modifiedRecords,
		}
	}
	return Result{
		GateName:      "test_integrity",
		Status:        StatusPassed,
		Summary:       fmt.Sprintf("测试完整性校验通过（单列既有测试修改 %d 个文件）", len(modifiedRecords)),
		ModifiedTests: modifiedRecords,
	}
}

// IsTestFilePath 判断相对路径是否属于 Go 或 TypeScript/JavaScript 测试文件。
// 参数 relPath 为文件相对路径；属于测试文件返回 true，否则返回 false。
func IsTestFilePath(relPath string) bool {
	lower := strings.ToLower(filepath.ToSlash(strings.TrimSpace(relPath)))
	return strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".spec.tsx") ||
		strings.HasSuffix(lower, ".test.tsx")
}

// compareTestSymbols 提取新旧测试文件中的测试函数或用例名，返回被删除的符号列表与被修改的既有符号列表。
func compareTestSymbols(filePath, oldSrc, newSrc string) (deleted []string, modified []string) {
	if strings.HasSuffix(filePath, "_test.go") {
		oldFuncs := extractGoTestFuncBodies(oldSrc)
		newFuncs := extractGoTestFuncBodies(newSrc)
		for name, oldBody := range oldFuncs {
			newBody, ok := newFuncs[name]
			if !ok {
				deleted = append(deleted, name)
			} else if oldBody != newBody {
				modified = append(modified, name)
			}
		}
		sort.Strings(deleted)
		sort.Strings(modified)
		return deleted, modified
	}

	oldCases := extractTSTestTitles(oldSrc)
	newCases := extractTSTestTitles(newSrc)
	for title := range oldCases {
		if !newCases[title] {
			deleted = append(deleted, title)
		}
	}
	sort.Strings(deleted)
	if oldSrc != newSrc && len(deleted) == 0 {
		modified = append(modified, filepath.Base(filePath))
	}
	return deleted, modified
}

// extractGoTestFuncBodies 使用 go/parser 提取 *_test.go 中所有 Test*/Benchmark*/Fuzz* 顶层函数名称及其源码片段。
func extractGoTestFuncBodies(src string) map[string]string {
	out := make(map[string]string)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		return out
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		name := fn.Name.Name
		if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Benchmark") && !strings.HasPrefix(name, "Fuzz") {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		if start >= 0 && end <= len(src) && start < end {
			out[name] = src[start:end]
		} else {
			out[name] = name
		}
	}
	return out
}

// extractTSTestTitles 提取 TS/JS 测试文件中所有 it(...) / test(...) 的标题集合。
func extractTSTestTitles(src string) map[string]bool {
	out := make(map[string]bool)
	for _, m := range tsTestCaseTitleRe.FindAllStringSubmatch(src, -1) {
		if len(m) >= 2 {
			out[m[1]] = true
		}
	}
	return out
}

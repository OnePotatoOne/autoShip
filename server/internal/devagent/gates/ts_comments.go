package gates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// tsClassDeclRe 匹配 TypeScript 中的导出或内部 class / abstract class 声明
	tsClassDeclRe = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`)
	// tsFuncDeclRe 匹配 TypeScript 顶层或模块级 function 声明
	tsFuncDeclRe = regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*[<(]`)
	// tsMethodDeclRe 匹配 class 花括号层级为 1 时的类成员方法声明（含访问修饰符、getter/setter、async 及普通方法）
	tsMethodDeclRe = regexp.MustCompile(`^(?:(?:public|private|protected|readonly|static|override|abstract|async|get|set)\s+)*([A-Za-z_$][A-Za-z0-9_$]*)\s*(?:<[^>{;]*>)?\s*\([^;]*$`)
	// tsControlKeywords 排除可能被误判为方法签名的控制流关键字
	tsControlKeywords = map[string]bool{
		"if": true, "for": true, "while": true, "switch": true, "catch": true,
		"return": true, "throw": true, "new": true, "super": true, "typeof": true,
		"import": true, "export": true, "function": true,
	}
)

// CheckTSComments 扫描改动或新增的 `.ts` 源文件，校验所有 `class`、`function` 及类方法是否带有前导注释（开发规范第 1 条 & 8.7 节）。
// 参数 worktreeDir 为工作树根目录，changedRelFiles 为改动相对路径列表；返回门禁检查 Result。
func CheckTSComments(worktreeDir string, changedRelFiles []string) Result {
	var tsFiles []string
	for _, rel := range changedRelFiles {
		clean := filepath.ToSlash(strings.TrimSpace(rel))
		if (strings.HasSuffix(clean, ".ts") || strings.HasSuffix(clean, ".tsx")) && !strings.HasSuffix(clean, ".d.ts") {
			full := filepath.Join(worktreeDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				tsFiles = append(tsFiles, clean)
			}
		}
	}
	if len(tsFiles) == 0 {
		return Result{
			GateName: "ts_comments",
			Status:   StatusSkipped,
			Summary:  "本次改动未涉及 TypeScript 源文件",
		}
	}

	var violations []Violation
	for _, rel := range tsFiles {
		raw, err := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		if err != nil {
			violations = append(violations, Violation{
				Rule:    "ts.read.error",
				File:    rel,
				Line:    1,
				Message: fmt.Sprintf("读取 TypeScript 文件失败: %v", err),
			})
			continue
		}
		violations = append(violations, inspectSingleTSFile(string(raw), rel)...)
	}

	if len(violations) > 0 {
		return Result{
			GateName:   "ts_comments",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("TypeScript 注释检查未通过：共发现 %d 处缺少前导注释的类、函数或方法", len(violations)),
			Violations: violations,
		}
	}
	return Result{
		GateName: "ts_comments",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已通过 %d 个 TypeScript 文件的 class、function 与类方法前导注释检查", len(tsFiles)),
	}
}

// inspectSingleTSFile 按行追踪块注释、装饰器与花括号嵌套深度，校验每个类、函数及类方法的前导注释。
func inspectSingleTSFile(content, displayPath string) []Violation {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var violations []Violation

	inBlockComment := false
	hasLeadingComment := false
	inDecorator := false
	decoratorParenDepth := 0

	braceDepth := 0
	classBraceStack := []int{} // 记录每个嵌套 class 起始的花括号层级
	pendingClassOpen := false

	for idx, rawLine := range lines {
		lineNo := idx + 1
		trimmed := strings.TrimSpace(rawLine)

		// 1. 处理多行注释状态 /* ... */ 或 /** ... */
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
				hasLeadingComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if strings.Contains(trimmed, "*/") {
				hasLeadingComment = true
			} else {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			hasLeadingComment = true
			continue
		}
		if trimmed == "" {
			// 空行中断紧邻的前导注释关联（要求注释紧贴声明或其装饰器上方）
			hasLeadingComment = false
			continue
		}

		// 2. 跳过 Angular / TS 装饰器（如 @Component({...})），保持其上方已识别的 hasLeadingComment 不丢失
		if inDecorator {
			decoratorParenDepth += strings.Count(trimmed, "(") - strings.Count(trimmed, ")")
			if decoratorParenDepth <= 0 {
				inDecorator = false
				decoratorParenDepth = 0
			}
			continue
		}
		if strings.HasPrefix(trimmed, "@") {
			diff := strings.Count(trimmed, "(") - strings.Count(trimmed, ")")
			if diff > 0 {
				inDecorator = true
				decoratorParenDepth = diff
			}
			continue
		}

		// 3. 检测 class 声明
		if m := tsClassDeclRe.FindStringSubmatch(trimmed); len(m) >= 2 {
			className := m[1]
			if !hasLeadingComment {
				violations = append(violations, Violation{
					Rule:    "ts.class_comment.missing",
					File:    displayPath,
					Line:    lineNo,
					Symbol:  className,
					Message: fmt.Sprintf("TypeScript 类 %s 缺少前导注释", className),
				})
			}
			hasLeadingComment = false
			pendingClassOpen = true
		} else if m := tsFuncDeclRe.FindStringSubmatch(trimmed); len(m) >= 2 {
			// 4. 检测 function 声明
			fnName := m[1]
			if !hasLeadingComment {
				violations = append(violations, Violation{
					Rule:    "ts.func_comment.missing",
					File:    displayPath,
					Line:    lineNo,
					Symbol:  fnName,
					Message: fmt.Sprintf("TypeScript 函数 %s 缺少前导注释", fnName),
				})
			}
			hasLeadingComment = false
		} else if len(classBraceStack) > 0 && braceDepth == classBraceStack[len(classBraceStack)-1] {
			// 5. 当前正处于 class 顶层成员作用域，检测类方法（含 constructor）
			if m := tsMethodDeclRe.FindStringSubmatch(trimmed); len(m) >= 2 {
				methodName := m[1]
				if !tsControlKeywords[methodName] && !strings.Contains(trimmed, "=") {
					if !hasLeadingComment {
						violations = append(violations, Violation{
							Rule:    "ts.method_comment.missing",
							File:    displayPath,
							Line:    lineNo,
							Symbol:  methodName,
							Message: fmt.Sprintf("TypeScript 类方法 %s 缺少前导注释", methodName),
						})
					}
				}
			}
			hasLeadingComment = false
		} else {
			hasLeadingComment = false
		}

		// 6. 更新花括号层级（剥离同行的字符串与行尾 // 注释干扰）
		codeOnly := stripTSLineStringsAndTrailingComment(trimmed)
		opens := strings.Count(codeOnly, "{")
		closes := strings.Count(codeOnly, "}")
		if pendingClassOpen && opens > 0 {
			classBraceStack = append(classBraceStack, braceDepth+1)
			pendingClassOpen = false
		}
		braceDepth += opens - closes
		if braceDepth < 0 {
			braceDepth = 0
		}
		for len(classBraceStack) > 0 && braceDepth < classBraceStack[len(classBraceStack)-1] {
			classBraceStack = classBraceStack[:len(classBraceStack)-1]
		}
	}
	return violations
}

// stripTSLineStringsAndTrailingComment 移除单行 TS 代码中的字符串字面量与行尾注释，以便准确计数花括号。
func stripTSLineStringsAndTrailingComment(line string) string {
	var b strings.Builder
	var quote rune
	escaped := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			continue
		}
		if ch == '/' && i+1 < len(runes) && runes[i+1] == '/' {
			break
		}
		b.WriteRune(ch)
	}
	return b.String()
}

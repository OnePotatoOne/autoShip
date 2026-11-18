package gates

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// binaryExts 列出无需做文本 UTF-8 检查的常见二进制资源扩展名。
var binaryExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".ico": true, ".pdf": true, ".woff": true, ".woff2": true, ".ttf": true,
	".eot": true, ".zip": true, ".gz": true, ".tar": true, ".exe": true,
	".so": true, ".dylib": true, ".dll": true, ".bin": true,
}

// CheckUTF8Files 使用标准库 `utf8.Valid` 检查所有改动或新增的文本文件是否为合法 UTF-8 编码且不含乱码替换符（开发规范第 3 条 & 8.7 节）。
// 参数 worktreeDir 为工作树根目录，changedRelFiles 为改动相对路径列表；返回门禁检查 Result。
func CheckUTF8Files(worktreeDir string, changedRelFiles []string) Result {
	var violations []Violation
	checked := 0

	for _, rel := range changedRelFiles {
		clean := filepath.ToSlash(strings.TrimSpace(rel))
		if clean == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(clean))
		if binaryExts[ext] {
			continue
		}
		full := filepath.Join(worktreeDir, filepath.FromSlash(clean))
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			violations = append(violations, Violation{
				Rule:    "utf8.read.error",
				File:    clean,
				Line:    1,
				Message: fmt.Sprintf("读取文件失败: %v", err),
			})
			continue
		}
		checked++
		if !utf8.Valid(data) {
			violations = append(violations, Violation{
				Rule:    "utf8.invalid_bytes",
				File:    clean,
				Line:    1,
				Message: "文件包含非法的非 UTF-8 字节序列（严禁使用 GBK/ISO-8859-1 等非 UTF-8 编码）",
			})
			continue
		}
		if bytes.Contains(data, []byte("\uFFFD")) {
			violations = append(violations, Violation{
				Rule:    "utf8.replacement_char",
				File:    clean,
				Line:    1,
				Message: "文件包含 Unicode 乱码替换字符 U+FFFD ()，请修复中文乱码",
			})
		}
	}

	if checked == 0 && len(violations) == 0 {
		return Result{
			GateName: "utf8_encoding",
			Status:   StatusSkipped,
			Summary:  "无待检查的文本文件",
		}
	}
	if len(violations) > 0 {
		return Result{
			GateName:   "utf8_encoding",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("UTF-8 编码合法性检查未通过：%d 个文件存在非 UTF-8 字节或乱码", len(violations)),
			Violations: violations,
		}
	}
	return Result{
		GateName: "utf8_encoding",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已通过 %d 个文本文件的 UTF-8 编码合法性校验", checked),
	}
}

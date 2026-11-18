package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// CheckGoComments 使用标准库 go/parser 与 go/ast 检查改动或新增的 Go 源文件中所有导出与内部函数、方法、结构体和接口类型是否具备完整注释（开发规范第 1 条 & 8.7 节）。
// 参数 worktreeDir 为工作树根目录，changedRelFiles 为改动或新增的仓库相对路径切片；返回门禁检查 Result。
func CheckGoComments(worktreeDir string, changedRelFiles []string) Result {
	var goFiles []string
	for _, rel := range changedRelFiles {
		clean := filepath.ToSlash(strings.TrimSpace(rel))
		if strings.HasSuffix(clean, ".go") {
			full := filepath.Join(worktreeDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				goFiles = append(goFiles, clean)
			}
		}
	}
	if len(goFiles) == 0 {
		return Result{
			GateName: "go_comments",
			Status:   StatusSkipped,
			Summary:  "本次改动未涉及 Go 源文件",
		}
	}

	var violations []Violation
	for _, rel := range goFiles {
		full := filepath.Join(worktreeDir, filepath.FromSlash(rel))
		fileViolations := inspectSingleGoFileComments(full, rel)
		violations = append(violations, fileViolations...)
	}

	if len(violations) > 0 {
		return Result{
			GateName:   "go_comments",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("Go AST 注释检查未通过：共发现 %d 处缺少注释的函数、方法或类型", len(violations)),
			Violations: violations,
		}
	}
	return Result{
		GateName: "go_comments",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已通过 %d 个 Go 文件的函数、方法与结构体/接口注释检查", len(goFiles)),
	}
}

// inspectSingleGoFileComments 解析单个 Go 文件的 AST，逐一核验顶层函数、接收者方法及结构体/接口类型声明的文档注释。
func inspectSingleGoFileComments(absPath, displayPath string) []Violation {
	fset := token.NewFileSet()
	fileAST, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return []Violation{{
			Rule:    "go.syntax.error",
			File:    displayPath,
			Line:    1,
			Message: fmt.Sprintf("解析 Go 语法树失败: %v", err),
		}}
	}

	var out []Violation
	for _, decl := range fileAST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// 同时检查导出与包内私有函数及方法
			docText := ""
			if d.Doc != nil {
				docText = strings.TrimSpace(d.Doc.Text())
			}
			symbol := d.Name.Name
			kindDesc := "函数"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recvName := formatRecvType(d.Recv.List[0].Type)
				symbol = recvName + "." + d.Name.Name
				kindDesc = "方法"
			}
			if docText == "" {
				pos := fset.Position(d.Pos())
				out = append(out, Violation{
					Rule:    "go.func_comment.missing",
					File:    displayPath,
					Line:    pos.Line,
					Symbol:  symbol,
					Message: fmt.Sprintf("%s %s 缺少前导完整注释（须说明作用、参数与返回值）", kindDesc, symbol),
				})
			}

		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			groupDoc := ""
			if d.Doc != nil {
				groupDoc = strings.TrimSpace(d.Doc.Text())
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// 重点检查结构体与接口类型（以及所有独立命名的业务类型）
				if !isCheckedGoTypeExpr(ts.Type) {
					continue
				}
				specDoc := ""
				if ts.Doc != nil {
					specDoc = strings.TrimSpace(ts.Doc.Text())
				}
				// 单一 type X struct{} 的注释通常挂在外层 GenDecl.Doc；type ( ... ) 分组声明则挂在各自的 TypeSpec.Doc
				effectiveDoc := specDoc
				if effectiveDoc == "" && len(d.Specs) == 1 {
					effectiveDoc = groupDoc
				}
				if effectiveDoc == "" {
					pos := fset.Position(ts.Pos())
					out = append(out, Violation{
						Rule:    "go.type_comment.missing",
						File:    displayPath,
						Line:    pos.Line,
						Symbol:  ts.Name.Name,
						Message: fmt.Sprintf("类型 %s 缺少完整类型注释", ts.Name.Name),
					})
				}
			}
		}
	}
	return out
}

// isCheckedGoTypeExpr 判断 AST 类型表达式是否为需要强制检查注释的结构体、接口或自定义类型定义。
func isCheckedGoTypeExpr(expr ast.Expr) bool {
	switch expr.(type) {
	case *ast.StructType, *ast.InterfaceType, *ast.FuncType, *ast.Ident, *ast.SelectorExpr, *ast.ArrayType, *ast.MapType:
		return true
	default:
		return true
	}
}

// formatRecvType 提取方法接收者的可读类型名称（如 *Service -> Service）。
func formatRecvType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return formatRecvType(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return formatRecvType(t.X)
	case *ast.IndexListExpr:
		return formatRecvType(t.X)
	default:
		return "receiver"
	}
}

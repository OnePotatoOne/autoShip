package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var (
	// sqlCreateTableStartRe 匹配 CREATE TABLE [IF NOT EXISTS] `table_name` ( 起始片段
	sqlCreateTableStartRe = regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[` + "`" + `"'\[]?([A-Za-z0-9_]+)[` + "`" + `"'\]]?\s*\(`)
	// sqlCommentClauseRe 匹配 SQL 表级或列级非空 COMMENT 'xxx' / COMMENT = 'xxx' 子句
	sqlCommentClauseRe = regexp.MustCompile(`(?is)\bCOMMENT\s*=?\s*['"]([^'"]+)['"]`)
	// sqlConstraintPrefixRe 识别无需列注释的表级主键/索引/外键约束定义行
	sqlConstraintPrefixRe = regexp.MustCompile(`(?i)^(?:PRIMARY\s+KEY|UNIQUE(?:\s+KEY|\s+INDEX)?|KEY\b|INDEX\b|CONSTRAINT\b|FOREIGN\s+KEY|FULLTEXT\b|SPATIAL\b|CHECK\s*\()`)
)

// CheckDatabaseComments 扫描改动或新增的 `.sql` 迁移脚本与 Go GORM 模型文件，强制校验新建表具备表注释且所有字段具备非空字段注释（开发规范第 2 条 & 8.7 节）。
// 参数 worktreeDir 为工作树根目录，changedRelFiles 为改动相对路径列表；返回门禁检查 Result。
func CheckDatabaseComments(worktreeDir string, changedRelFiles []string) Result {
	var sqlFiles, goFiles []string
	for _, rel := range changedRelFiles {
		clean := filepath.ToSlash(strings.TrimSpace(rel))
		full := filepath.Join(worktreeDir, filepath.FromSlash(clean))
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(strings.ToLower(clean), ".sql"):
			sqlFiles = append(sqlFiles, clean)
		case strings.HasSuffix(clean, ".go") && !strings.HasSuffix(clean, "_test.go"):
			goFiles = append(goFiles, clean)
		}
	}

	var violations []Violation
	checkedCount := 0

	for _, rel := range sqlFiles {
		raw, err := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		checked, v := inspectSQLCreateTables(string(raw), rel)
		checkedCount += checked
		violations = append(violations, v...)
	}

	for _, rel := range goFiles {
		full := filepath.Join(worktreeDir, filepath.FromSlash(rel))
		checked, v := inspectGORMModelComments(full, rel)
		checkedCount += checked
		violations = append(violations, v...)
	}

	if checkedCount == 0 && len(violations) == 0 {
		return Result{
			GateName: "db_comments",
			Status:   StatusSkipped,
			Summary:  "本次改动未涉及新建 SQL 表或 GORM 数据表模型",
		}
	}
	if len(violations) > 0 {
		return Result{
			GateName:   "db_comments",
			Status:     StatusFailed,
			Summary:    fmt.Sprintf("数据库表/字段注释检查未通过：共发现 %d 处缺失表注释或字段注释", len(violations)),
			Violations: violations,
		}
	}
	return Result{
		GateName: "db_comments",
		Status:   StatusPassed,
		Summary:  fmt.Sprintf("已通过 %d 个数据库表/GORM 模型的表级与字段级注释完整性校验", checkedCount),
	}
}

// inspectSQLCreateTables 解析 SQL 脚本中的全部 CREATE TABLE 语句，核查表级 COMMENT 与每列的 COMMENT。
func inspectSQLCreateTables(sqlContent, displayPath string) (int, []Violation) {
	var violations []Violation
	locs := sqlCreateTableStartRe.FindAllStringSubmatchIndex(sqlContent, -1)
	if len(locs) == 0 {
		return 0, nil
	}

	for _, loc := range locs {
		tableName := sqlContent[loc[2]:loc[3]]
		openParenIdx := loc[1] - 1 // 指向 `(`
		lineNo := 1 + strings.Count(sqlContent[:loc[0]], "\n")

		closeParenIdx := findMatchingParen(sqlContent, openParenIdx)
		if closeParenIdx < 0 {
			violations = append(violations, Violation{
				Rule:    "db.sql.syntax",
				File:    displayPath,
				Line:    lineNo,
				Symbol:  tableName,
				Message: fmt.Sprintf("CREATE TABLE %s 缺少闭合右括号", tableName),
			})
			continue
		}

		columnsBody := sqlContent[openParenIdx+1 : closeParenIdx]
		// 提取右括号到分号之间的表级选项段
		rest := sqlContent[closeParenIdx+1:]
		semiIdx := strings.IndexByte(rest, ';')
		tableOptions := rest
		if semiIdx >= 0 {
			tableOptions = rest[:semiIdx]
		}

		tableCommentMatch := sqlCommentClauseRe.FindStringSubmatch(tableOptions)
		if len(tableCommentMatch) < 2 || strings.TrimSpace(tableCommentMatch[1]) == "" {
			violations = append(violations, Violation{
				Rule:    "db.table_comment.missing",
				File:    displayPath,
				Line:    lineNo,
				Symbol:  tableName,
				Message: fmt.Sprintf("SQL 新建表 %s 缺少非空表注释（如 COMMENT='...'）", tableName),
			})
		}

		for _, colDef := range splitTopLevelCommaSQL(columnsBody) {
			trimmed := strings.TrimSpace(stripSQLComments(colDef))
			if trimmed == "" || sqlConstraintPrefixRe.MatchString(trimmed) {
				continue
			}
			colName := extractFirstSQLIdentifier(trimmed)
			if colName == "" {
				continue
			}
			colCommentMatch := sqlCommentClauseRe.FindStringSubmatch(trimmed)
			if len(colCommentMatch) < 2 || strings.TrimSpace(colCommentMatch[1]) == "" {
				violations = append(violations, Violation{
					Rule:    "db.column_comment.missing",
					File:    displayPath,
					Line:    lineNo,
					Symbol:  tableName + "." + colName,
					Message: fmt.Sprintf("SQL 表 %s 的字段 %s 缺少非空字段注释（COMMENT '...'）", tableName, colName),
				})
			}
		}
	}
	return len(locs), violations
}

// inspectGORMModelComments 使用 go/ast 扫描 Go 文件中的 GORM 数据表结构体（含 TableName 方法或含 gorm 标签的结构体），校验表注释与字段 comment 标签。
func inspectGORMModelComments(absPath, displayPath string) (int, []Violation) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return 0, nil
	}

	hasTableNameMethod := make(map[string]bool)
	hasNonEmptyTableCommentMethod := make(map[string]bool)

	for _, decl := range astFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		recvType := formatRecvType(fn.Recv.List[0].Type)
		if fn.Name.Name == "TableName" {
			hasTableNameMethod[recvType] = true
		}
		if fn.Name.Name == "TableComment" && funcReturnsNonEmptyString(fn.Body) {
			hasNonEmptyTableCommentMethod[recvType] = true
		}
	}

	var violations []Violation
	modelCount := 0

	for _, decl := range astFile.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			if !isGORMModelStruct(ts.Name.Name, st, hasTableNameMethod) {
				continue
			}
			modelCount++
			typePos := fset.Position(ts.Pos())

			if !hasNonEmptyTableCommentMethod[ts.Name.Name] {
				violations = append(violations, Violation{
					Rule:    "db.gorm_table_comment.missing",
					File:    displayPath,
					Line:    typePos.Line,
					Symbol:  ts.Name.Name,
					Message: fmt.Sprintf("GORM 数据表模型 %s 必须实现返回非空中文注释的 TableComment() string 方法", ts.Name.Name),
				})
			}

			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					// 匿名内嵌结构体（如公共基础字段）由其自身定义处检查
					continue
				}
				rawTag := ""
				if field.Tag != nil {
					if unq, err := strconv.Unquote(field.Tag.Value); err == nil {
						rawTag = unq
					} else {
						rawTag = strings.Trim(field.Tag.Value, "`\"")
					}
				}
				gormVal := reflect.StructTag(rawTag).Get("gorm")
				if gormVal == "-" || strings.HasPrefix(gormVal, "-:") {
					continue
				}
				for _, ident := range field.Names {
					if !ident.IsExported() {
						continue
					}
					if !gormTagHasComment(gormVal) {
						fPos := fset.Position(ident.Pos())
						violations = append(violations, Violation{
							Rule:    "db.gorm_field_comment.missing",
							File:    displayPath,
							Line:    fPos.Line,
							Symbol:  ts.Name.Name + "." + ident.Name,
							Message: fmt.Sprintf("GORM 模型 %s 的持久化字段 %s 缺少非空 gorm:\"comment:...\" 注释标签", ts.Name.Name, ident.Name),
						})
					}
				}
			}
		}
	}
	return modelCount, violations
}

// isGORMModelStruct 判断结构体是否为 GORM 数据表实体（实现了 TableName() 或拥有带 gorm 结构标签的字段）。
func isGORMModelStruct(typeName string, st *ast.StructType, hasTableName map[string]bool) bool {
	if hasTableName[typeName] {
		return true
	}
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		rawTag := strings.Trim(f.Tag.Value, "`\"")
		if unq, err := strconv.Unquote(f.Tag.Value); err == nil {
			rawTag = unq
		}
		gormVal := reflect.StructTag(rawTag).Get("gorm")
		if gormVal != "" && gormVal != "-" {
			return true
		}
	}
	return false
}

// gormTagHasComment 检查 gorm 标签字符串中是否包含非空的 comment:xxx 段。
func gormTagHasComment(gormTag string) bool {
	for _, seg := range strings.Split(gormTag, ";") {
		trimmed := strings.TrimSpace(seg)
		if strings.HasPrefix(trimmed, "comment:") {
			val := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "comment:")), "'\"")
			return val != ""
		}
	}
	return false
}

// funcReturnsNonEmptyString 判断 TableComment() 方法体是否返回了非空字符串字面量或表达式。
func funcReturnsNonEmptyString(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			continue
		}
		if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			unquoted := strings.Trim(lit.Value, "`\"")
			return strings.TrimSpace(unquoted) != ""
		}
		return true
	}
	return false
}

// findMatchingParen 在忽略字符串引号的前提下寻找配对的闭合右括号下标。
func findMatchingParen(s string, openIdx int) int {
	depth := 0
	var quote byte
	for i := openIdx; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevelCommaSQL 按顶层逗号切分 CREATE TABLE 括号内的各列与约束定义（忽略 DECIMAL(10,2) 等内部括号逗号）。
func splitTopLevelCommaSQL(body string) []string {
	var parts []string
	start := 0
	depth := 0
	var quote byte
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if quote != 0 {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			quote = ch
			continue
		}
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	if start < len(body) {
		parts = append(parts, body[start:])
	}
	return parts
}

// stripSQLComments 剥离字段片段开头的 `--` 行注释。
func stripSQLComments(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, " ")
}

// extractFirstSQLIdentifier 提取列定义首个标识符作为字段名。
func extractFirstSQLIdentifier(colDef string) string {
	fields := strings.Fields(colDef)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "`\"'[]")
}

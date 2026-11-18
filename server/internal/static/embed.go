// Package static 提供内嵌到 Go 单二进制中的管理后台前端静态资源文件系统。
package static

import "embed"

// FS 保存编译期内嵌的 dist 目录全部前端静态文件；路由层通过 fs.Sub(static.FS, "dist") 对外提供服务。
//
//go:embed all:dist
var FS embed.FS

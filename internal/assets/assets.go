// Package assets 内嵌前端模板与静态资源。
//
// 用 go:embed 打包进二进制，因此部署时只需要一个可执行文件 + 一个 SQLite
// 数据文件；需要改模板时（开发模式 ERP_DEV=1）也可以直接从磁盘读取。
package assets

import (
	"embed"
	"io/fs"
)

// ReadStatic 读取 static/ 下的文件内容。
func ReadStatic(name string) ([]byte, error) {
	return fs.ReadFile(FS, "static/"+name)
}

// FS 包含 templates/ 与 static/ 两个目录。
//
//go:embed templates static
var FS embed.FS

// Package webui 内嵌 Web 控制台的静态资源（单二进制随服务一起分发）。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// Assets 返回 dist 目录下的静态资源文件系统（index.html / app.js / app.css 等）。
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// dist 由 //go:embed 在编译期固定，理论上不会失败。
		panic(err)
	}
	return sub
}

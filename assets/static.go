// Package assets 暴露前端静态资源（embed.FS）
package assets

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var frontendFS embed.FS

// DistFS 嵌入的前端静态文件系统（已切换到 dist/ 子目录）
var DistFS fs.FS

func init() {
	sub, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		panic(err)
	}
	DistFS = sub
}

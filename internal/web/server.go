package web

import (
	"net/http"

	"isvbytes.com/codex-relayx/assets"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册前端静态文件路由
func RegisterRoutes(r *gin.Engine) {
	distFS := assets.DistFS
	fileServer := http.FileServer(http.FS(distFS))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if isAPIPath(path) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		// 先尝试按请求路径取静态文件
		if path != "/" && path != "" {
			if f, err := distFS.Open(path[1:]); err == nil {
				f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		// SPA fallback → index.html
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

func isAPIPath(path string) bool {
	apiPrefixes := []string{
		"/v1/",
		"/admin/api/",
	}
	for _, prefix := range apiPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

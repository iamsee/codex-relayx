package server

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"isvbytes.com/codex-relayx/internal/api"
	"isvbytes.com/codex-relayx/internal/proxy"
	"isvbytes.com/codex-relayx/internal/state"
	webPkg "isvbytes.com/codex-relayx/internal/web"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Server HTTP 服务器
type Server struct {
	state    *state.AppState
	port     int
	logger   *zap.Logger
	httpSrv  *http.Server
}

// NewServer 创建服务器
func NewServer(s *state.AppState, port int, logger *zap.Logger) *Server {
	return &Server{
		state:  s,
		port:   port,
		logger: logger,
	}
}

// Start 启动服务器
func (s *Server) Start() error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// 中间件
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())

	// 创建处理器
	proxyHandler := proxy.NewHandler(s.state, s.logger)
	adminHandler := api.NewHandler(s.state)

	// 协议转换路由（Codex CLI 主用）
	// proxyHandler 的方法签名是 http.HandlerFunc，需要适配成 gin.HandlerFunc
	adapt := func(h http.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { h(c.Writer, c.Request) }
	}
	r.POST("/v1/responses", adapt(proxyHandler.HandleResponses))
	r.POST("/v1/chat/completions", adapt(proxyHandler.HandleChatCompletions))
	r.GET("/v1/models", adapt(proxyHandler.HandleModels))

	// 管理 API 路由
	adminGroup := r.Group("/admin/api")
	adminHandler.RegisterRoutes(adminGroup)

	// 前端静态文件（最后注册，SPA fallback 在 web 包内）
	webPkg.RegisterRoutes(r)

	// 创建 HTTP 服务器
	s.httpSrv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Windows 用户自动打开浏览器
	if runtime.GOOS == "windows" {
		go openBrowser(fmt.Sprintf("http://127.0.0.1:%d", s.port))
	}

	return s.httpSrv.ListenAndServe()
}

// Stop 停止服务器
func (s *Server) Stop() {
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(ctx)
	}
}

// openBrowser 打开浏览器
func openBrowser(url string) {
	time.Sleep(500 * time.Millisecond) // 等待服务器启动
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	}

	if cmd != "" {
		exec.Command(cmd, args...).Start()
	}
}

// corsMiddleware CORS 中间件
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Length")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

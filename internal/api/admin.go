package api

import (
	"fmt"
	"net/http"

	"isvbytes.com/codex-relayx/internal/config"
	"isvbytes.com/codex-relayx/internal/state"
	"github.com/gin-gonic/gin"
)

// Handler 管理 API 处理器
type Handler struct {
	state *state.AppState
}

// NewHandler 创建处理器
func NewHandler(s *state.AppState) *Handler {
	return &Handler{state: s}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	r.GET("/config", h.getConfig)
	r.PUT("/config", h.updateConfig)

	r.GET("/upstreams", h.listUpstreams)
	r.POST("/upstreams", h.createUpstream)
	r.GET("/upstreams/:id", h.getUpstream)
	r.PUT("/upstreams/:id", h.updateUpstream)
	r.DELETE("/upstreams/:id", h.deleteUpstream)

	r.GET("/models", h.listModels)
	r.POST("/models", h.createModel)
	r.DELETE("/models/:name", h.deleteModel)

	r.GET("/stats", h.getStats)
	r.GET("/logs", h.getLogs)
}

// getConfig 获取配置信息
func (h *Handler) getConfig(c *gin.Context) {
	cfg := h.state.GetConfig()
	c.JSON(http.StatusOK, gin.H{
		"listen_port":            cfg.ListenPort,
		"admin_password_set":     cfg.AdminPassword != "",
		"request_timeout_secs":   cfg.RequestTimeoutSecs,
		"upstream_count":         len(cfg.Upstreams),
		"enabled_upstream_count": len(cfg.EnabledUpstreams()),
		"model_mapping_count":    len(cfg.ModelMapping),
	})
}

// updateConfig 更新配置
func (h *Handler) updateConfig(c *gin.Context) {
	var req struct {
		ListenPort         *int   `json:"listen_port,omitempty"`
		RequestTimeoutSecs *int   `json:"request_timeout_secs,omitempty"`
		AdminPassword      *string `json:"admin_password,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.state.UpdateConfig(func(cfg *config.AppConfig) {
		if req.ListenPort != nil {
			cfg.ListenPort = *req.ListenPort
		}
		if req.RequestTimeoutSecs != nil {
			cfg.RequestTimeoutSecs = *req.RequestTimeoutSecs
		}
		if req.AdminPassword != nil {
			cfg.AdminPassword = *req.AdminPassword
		}
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// listUpstreams 列出所有上游
func (h *Handler) listUpstreams(c *gin.Context) {
	c.JSON(http.StatusOK, h.state.GetUpstreams())
}

// getUpstream 获取指定上游
func (h *Handler) getUpstream(c *gin.Context) {
	id := c.Param("id")
	upstreams := h.state.GetUpstreams()
	for _, u := range upstreams {
		if u.ID == id {
			c.JSON(http.StatusOK, u)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "upstream not found"})
}

// createUpstream 创建上游
func (h *Handler) createUpstream(c *gin.Context) {
	var upstream config.UpstreamConfig
	if err := c.ShouldBindJSON(&upstream); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 检查 ID 是否已存在
	upstreams := h.state.GetUpstreams()
	for _, u := range upstreams {
		if u.ID == upstream.ID {
			c.JSON(http.StatusConflict, gin.H{"error": "upstream ID already exists"})
			return
		}
	}

	if err := h.state.AddUpstream(upstream); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusCreated)
}

// updateUpstream 更新上游
func (h *Handler) updateUpstream(c *gin.Context) {
	id := c.Param("id")
	var upstream config.UpstreamConfig
	if err := c.ShouldBindJSON(&upstream); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.state.UpdateUpstream(id, upstream); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// deleteUpstream 删除上游
func (h *Handler) deleteUpstream(c *gin.Context) {
	id := c.Param("id")
	if err := h.state.DeleteUpstream(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// listModels 列出所有模型映射
func (h *Handler) listModels(c *gin.Context) {
	cfg := h.state.GetConfig()
	var models []map[string]any

	// 全局映射
	for codex, upstream := range cfg.ModelMapping {
		models = append(models, map[string]any{
			"codex_model":    codex,
			"upstream_model": upstream,
			"upstream_id":    "global",
			"enabled":        true,
		})
	}

	// 上游级映射
	upstreams := h.state.GetUpstreams()
	for _, u := range upstreams {
		for codex, upstream := range u.ModelMapping {
			models = append(models, map[string]any{
				"codex_model":    codex,
				"upstream_model": upstream,
				"upstream_id":    u.ID,
				"enabled":        u.Enabled,
			})
		}
	}

	c.JSON(http.StatusOK, models)
}

// createModel 创建模型映射
func (h *Handler) createModel(c *gin.Context) {
	var req struct {
		CodexModel    string `json:"codex_model"`
		UpstreamModel string `json:"upstream_model"`
		UpstreamID    string `json:"upstream_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.state.AddModelMapping(req.CodexModel, req.UpstreamModel, req.UpstreamID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusCreated)
}

// deleteModel 删除模型映射
func (h *Handler) deleteModel(c *gin.Context) {
	name := c.Param("name")
	if err := h.state.DeleteModelMapping(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// getStats 获取统计信息
func (h *Handler) getStats(c *gin.Context) {
	c.JSON(http.StatusOK, h.state.GetStats())
}

// getLogs 获取日志
func (h *Handler) getLogs(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "100")
	limit := 100
	if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	c.JSON(http.StatusOK, h.state.GetLogs(limit))
}

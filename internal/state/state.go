package state

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"isvbytes.com/codex-relayx/internal/config"
)

// AppState 应用全局状态
type AppState struct {
	cfg     *config.AppConfig
	dataDir string
	mu      sync.RWMutex

	// 统计数据
	requestCount  int64
	errorCount    int64
	startTime     time.Time

	// 日志（保留最近的 N 条）
	logs          []LogEntry
	maxLogs       int
	logsMu        sync.Mutex
}

// LogEntry 请求日志
type LogEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Model         string    `json:"model"`
	UpstreamName  string    `json:"upstream_name"`
	UpstreamModel string    `json:"upstream_model"`
	StatusCode    int       `json:"status_code"`
	LatencyMs     int64     `json:"latency_ms"`
	Tools         []string  `json:"tools"`
	Error         string    `json:"error,omitempty"`
}

// StatsSnapshot 统计快照
type StatsSnapshot struct {
	Uptime       string  `json:"uptime"`
	TotalRequests int64  `json:"total_requests"`
	Errors       int64   `json:"errors"`
	UpstreamCount int    `json:"upstream_count"`
	EnabledUpstreams int `json:"enabled_upstreams"`
}

// NewAppState 创建应用状态
func NewAppState(cfg *config.AppConfig, dataDir string) (*AppState, error) {
	return &AppState{
		cfg:     cfg,
		dataDir: dataDir,
		startTime: time.Now(),
		logs:    make([]LogEntry, 0, 1000),
		maxLogs: 1000,
	}, nil
}

// GetConfig 获取配置（线程安全）
func (s *AppState) GetConfig() *config.AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// UpdateConfig 更新配置（线程安全 + 持久化）
func (s *AppState) UpdateConfig(f func(cfg *config.AppConfig)) error {
	s.mu.Lock()
	f(s.cfg)
	s.mu.Unlock()
	return s.PersistConfig()
}

// PersistConfig 持久化配置到 data_dir/config.json
func (s *AppState) PersistConfig() error {
	configPath := filepath.Join(s.dataDir, "config.json")
	return config.SaveToFile(configPath, s.cfg)
}

// ResolveModel 解析模型名称
func (s *AppState) ResolveModel(model string) (string, *config.UpstreamConfig) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.ResolveModel(model)
}

// RecordRequest 记录请求
func (s *AppState) RecordRequest(entry LogEntry) {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()

	// 添加时间戳
	entry.Timestamp = time.Now()

	// 添加到日志
	s.logs = append(s.logs, entry)

	// 超过最大数量，移除最早的
	if len(s.logs) > s.maxLogs {
		s.logs = s.logs[1:]
	}

	// 更新统计
	s.requestCount++
	if entry.StatusCode >= 400 {
		s.errorCount++
	}
}

// GetLogs 获取日志（最近的 N 条）
func (s *AppState) GetLogs(limit int) []LogEntry {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()

	start := len(s.logs) - limit
	if start < 0 {
		start = 0
	}

	result := make([]LogEntry, len(s.logs)-start)
	copy(result, s.logs[start:])
	return result
}

// GetStats 获取统计信息
func (s *AppState) GetStats() StatsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	uptime := time.Since(s.startTime)
	hours := int(uptime.Hours())
	minutes := int(uptime.Minutes()) % 60
	seconds := int(uptime.Seconds()) % 60

	return StatsSnapshot{
		Uptime:        fmt.Sprintf("%dh%dm%ds", hours, minutes, seconds),
		TotalRequests: s.requestCount,
		Errors:        s.errorCount,
		UpstreamCount: len(s.cfg.Upstreams),
		EnabledUpstreams: len(s.cfg.EnabledUpstreams()),
	}
}

// GetUpstreams 获取上游列表（深拷贝）
func (s *AppState) GetUpstreams() []config.UpstreamConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]config.UpstreamConfig, len(s.cfg.Upstreams))
	for i, u := range s.cfg.Upstreams {
		result[i] = u
		// 深拷贝 map
		result[i].ModelMapping = make(map[string]string)
		for k, v := range u.ModelMapping {
			result[i].ModelMapping[k] = v
		}
	}
	return result
}

// AddUpstream 添加上游
func (s *AppState) AddUpstream(upstream config.UpstreamConfig) error {
	return s.UpdateConfig(func(cfg *config.AppConfig) {
		cfg.Upstreams = append(cfg.Upstreams, upstream)
	})
}

// UpdateUpstream 更新上游
func (s *AppState) UpdateUpstream(id string, upstream config.UpstreamConfig) error {
	return s.UpdateConfig(func(cfg *config.AppConfig) {
		for i := range cfg.Upstreams {
			if cfg.Upstreams[i].ID == id {
				upstream.ID = id // 保持 ID 不变
				cfg.Upstreams[i] = upstream
				return
			}
		}
	})
}

// DeleteUpstream 删除上游
func (s *AppState) DeleteUpstream(id string) error {
	return s.UpdateConfig(func(cfg *config.AppConfig) {
		for i := range cfg.Upstreams {
			if cfg.Upstreams[i].ID == id {
				cfg.Upstreams = append(cfg.Upstreams[:i], cfg.Upstreams[i+1:]...)
				return
			}
		}
	})
}

// AddModelMapping 添加模型映射
func (s *AppState) AddModelMapping(codexModel, upstreamModel, upstreamID string) error {
	return s.UpdateConfig(func(cfg *config.AppConfig) {
		if upstreamID == "global" {
			cfg.ModelMapping[codexModel] = upstreamModel
		} else {
			for i := range cfg.Upstreams {
				if cfg.Upstreams[i].ID == upstreamID {
					cfg.Upstreams[i].ModelMapping[codexModel] = upstreamModel
					return
				}
			}
		}
	})
}

// DeleteModelMapping 删除模型映射
func (s *AppState) DeleteModelMapping(codexModel string) error {
	return s.UpdateConfig(func(cfg *config.AppConfig) {
		delete(cfg.ModelMapping, codexModel)
		for i := range cfg.Upstreams {
			delete(cfg.Upstreams[i].ModelMapping, codexModel)
		}
	})
}

// ConfigToJSON 导出配置为 JSON
func (s *AppState) ConfigToJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s.cfg, "", "  ")
}

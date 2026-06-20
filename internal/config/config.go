package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// AppConfig 应用全局配置
type AppConfig struct {
	ListenPort         int                  `json:"listen_port"`
	AdminPassword      string               `json:"admin_password"`
	RequestTimeoutSecs int                  `json:"request_timeout_secs"`
	Upstreams          []UpstreamConfig     `json:"upstreams"`
	ModelMapping       map[string]string    `json:"model_mapping"`
}

// UpstreamConfig 上游配置
type UpstreamConfig struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Enabled       bool              `json:"enabled"`
	BaseURL       string            `json:"base_url"`
	APIKey        string            `json:"api_key"`
	APIFormat     string            `json:"api_format"` // "openai_chat" or "anthropic"
	ModelMapping  map[string]string `json:"model_mapping"`
	TimeoutSecs   *int              `json:"timeout_secs,omitempty"`
	MaxRetries    int               `json:"max_retries"`
}

// Default 返回默认配置
func Default() *AppConfig {
	return &AppConfig{
		ListenPort:         8001,
		AdminPassword:      "",
		RequestTimeoutSecs: 120,
		Upstreams: []UpstreamConfig{
			{
				ID:           "default",
				Name:         "Default Upstream",
				Enabled:      true,
				BaseURL:      "http://127.0.0.1:11000/v1",
				APIKey:       "",
				APIFormat:    "openai_chat",
				ModelMapping: map[string]string{},
				MaxRetries:   3,
			},
		},
		ModelMapping: map[string]string{},
	}
}

// LoadFromFile 从文件加载配置（支持 JSON 和 YAML）
func LoadFromFile(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// 初始化空 map
	if cfg.ModelMapping == nil {
		cfg.ModelMapping = map[string]string{}
	}
	for i := range cfg.Upstreams {
		if cfg.Upstreams[i].ModelMapping == nil {
			cfg.Upstreams[i].ModelMapping = map[string]string{}
		}
	}

	return &cfg, nil
}

// SaveToFile 保存配置到文件
func SaveToFile(path string, cfg *AppConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// 原子写入
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename: %w", err)
	}

	return nil
}

// FindUpstream 根据 ID 查找上游
func (c *AppConfig) FindUpstream(id string) *UpstreamConfig {
	for i := range c.Upstreams {
		if c.Upstreams[i].ID == id {
			return &c.Upstreams[i]
		}
	}
	return nil
}

// EnabledUpstreams 返回所有启用的上游
func (c *AppConfig) EnabledUpstreams() []*UpstreamConfig {
	var result []*UpstreamConfig
	for i := range c.Upstreams {
		if c.Upstreams[i].Enabled {
			result = append(result, &c.Upstreams[i])
		}
	}
	return result
}

// ResolveModel 解析模型名称，返回 (目标模型, 上游)
// 优先使用上游级别的映射，然后全局映射
func (c *AppConfig) ResolveModel(model string) (string, *UpstreamConfig) {
	// 1. 上游级映射
	for i := range c.Upstreams {
		if !c.Upstreams[i].Enabled {
			continue
		}
		if mapped, ok := c.Upstreams[i].ModelMapping[model]; ok {
			return mapped, &c.Upstreams[i]
		}
	}

	// 2. 全局映射
	if mapped, ok := c.ModelMapping[model]; ok {
		upstreams := c.EnabledUpstreams()
		if len(upstreams) > 0 {
			return mapped, upstreams[0]
		}
	}

	// 3. 原样返回
	upstreams := c.EnabledUpstreams()
	if len(upstreams) > 0 {
		return model, upstreams[0]
	}
	return model, nil
}

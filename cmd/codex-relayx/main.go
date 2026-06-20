package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"isvbytes.com/codex-relayx/internal/config"
	"isvbytes.com/codex-relayx/internal/server"
	"isvbytes.com/codex-relayx/internal/state"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	var (
		port       int
		dataDir    string
		configPath string
	)

	flag.IntVar(&port, "port", 8001, "HTTP server port")
	flag.StringVar(&dataDir, "data-dir", "./data", "Data directory for config.json and logs")
	flag.StringVar(&configPath, "config", "", "Config file path (optional)")
	flag.Parse()

	// 环境变量覆盖（生产部署友好）
	if v := os.Getenv("RELAYX_PORT"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &port); n != 1 || err != nil {
			// 忽略无效值
		}
	}
	if v := os.Getenv("RELAYX_DATA_DIR"); v != "" {
		dataDir = v
	}
	if v := os.Getenv("RELAYX_CONFIG"); v != "" {
		configPath = v
	}

	// 初始化日志
	logger, _ := zap.NewProduction(zap.AddCaller())
	zap.ReplaceGlobals(logger)
	defer logger.Sync()

	logger.Info("codex-relayx starting",
		zap.Int("port", port),
		zap.String("data-dir", dataDir),
	)

	// 确保数据目录存在
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Fatal("failed to create data directory", zap.Error(err))
	}

	// 加载配置
	var cfg *config.AppConfig
	var err error

	if configPath != "" {
		cfg, err = config.LoadFromFile(configPath)
	} else {
		configFile := filepath.Join(dataDir, "config.json")
		if _, statErr := os.Stat(configFile); statErr == nil {
			cfg, err = config.LoadFromFile(configFile)
		} else {
			cfg = config.Default()
		}
	}

	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	logger.Info("config loaded",
		zap.Int("upstreams", len(cfg.Upstreams)),
		zap.Int("model_mappings", len(cfg.ModelMapping)),
	)

	// 创建全局状态
	appState, err := state.NewAppState(cfg, dataDir)
	if err != nil {
		logger.Fatal("failed to create app state", zap.Error(err))
	}

	// 首次启动时持久化配置
	if err := appState.PersistConfig(); err != nil {
		logger.Warn("failed to persist initial config", zap.Error(err))
	}

	// 启动 HTTP 服务器
	srv := server.NewServer(appState, port, logger)
	go func() {
		if err := srv.Start(); err != nil {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	logger.Info(fmt.Sprintf("server started on http://127.0.0.1:%d", port))

	// 等待退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("shutting down...")
	srv.Stop()
}

func init() {
	// 设置 zap 的 console encoder
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder

	consoleEncoder := zapcore.NewConsoleEncoder(encoderCfg)
	core := zapcore.NewCore(
		consoleEncoder,
		zapcore.AddSync(os.Stdout),
		zapcore.InfoLevel,
	)

	logger := zap.New(core)
	zap.ReplaceGlobals(logger)
}

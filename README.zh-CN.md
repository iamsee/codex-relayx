# Codex-Relayx（中文）

[![Go Version](https://img.shields.io/github/go-mod/go-version/iamsee/codex-relayx)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/iamsee/codex-relayx)](https://github.com/iamsee/codex-relayx/releases/latest)
[![GitHub stars](https://img.shields.io/github/stars/iamsee/codex-relayx?style=social)](https://github.com/iamsee/codex-relayx/stargazers)

[English](README.md) | [中文](README.zh-CN.md)

一个用 Go 写成的、轻量级的、单文件 LLM 协议转换网关。

## 项目目的

**Codex-Relayx 的存在，是为了能让 OpenAI Codex CLI 直接调用 DeepSeek、智谱 GLM、MiniMax、小米 MiMo 等国产模型，就像调用 OpenAI 一类的一等公民提供方一样。**

Codex CLI 原生只支持两种协议：

- **OpenAI Responses API**（`/v1/responses`）—— 现代路径，用于 `gpt-5` 和推理模型
- **OpenAI Chat Completions API**（`/v1/chat/completions`）—— 兼容路径，用于老的 `gpt-4` 系列

但我们实际想用的提供方全是国产，每个的协议都不一样：

| 提供方 | 公司 | 官方 API 协议 |
|---|---|---|
| **DeepSeek** | 深度求索 | OpenAI Chat Completions 兼容（`https://api.deepseek.com`） |
| **GLM** | 智谱 AI（Z.ai） | OpenAI Chat Completions 兼容；Anthropic Messages 兼容（`https://open.bigmodel.cn/api/anthropic`） |
| **MiniMax** | MiniMax（稀宇科技） | Anthropic Messages 兼容（`https://api.MiniMax.chat/anthropic`）+ OpenAI 兼容（`https://api.MiniMax.chat/v1`） |
| **MiMo** | Xiaomi（小米） | OpenAI Chat Completions 兼容 |

矛盾点：Codex CLI 只会说 OpenAI 的语言，但 DeepSeek-V3/R1、GLM-4.5/4.6/5、MiniMax-M3、MiMo 这些想用的模型，分散在 OpenAI 兼容和 Anthropic 兼容端点后面，每个还有自己的细节差异（header 名、SSE 格式、模型名大小写、UA 偏好……）。

**Codex-Relayx 就是为了打通这个差异。** 在任何这类提供方前面放一个 codex-relayx，把 `api_format` 设为 `openai_chat` 或 `anthropic`，Codex CLI 不改任何客户端配置就能调用。同一个网关也原生支持 `/v1/chat/completions`，因此 OpenAI 风格的消费方（[new-api](https://github.com/songquanpeng/one-api)、Open WebUI 等）也透明工作。

具体落地的场景：

- 让 `codex` 调通阿里云百炼（Anthropic 兼容）的 `qwen3.7-plus`，跑出和 `gpt-5` 一样的多轮工具调用
- 在一个 `base_url` 后面聚合 4 家国产提供方，靠一张 `model_mapping` 表决定流量分发
- 一个管理 UI 切换提供方、热重载配置、看请求日志

## 工作原理

```
                  ┌─────────────────────────────────┐
                  │       codex-relayx（本项目）     │
                  │  /v1/responses （Codex CLI）    │
   Codex CLI ────►│  /v1/chat/completions（new-api）│────► DeepSeek   （openai_chat）
                  │  /v1/models, /admin/api/*, /    │────► 智谱 GLM   （openai_chat 或 anthropic）
                  └─────────────────────────────────┘────► MiniMax    （anthropic 或 openai_chat）
                                                        ────► MiMo      （openai_chat）
```

协议转换层是 Rust 参考实现
[`codexplusplus/src/protocol_proxy.rs`](https://github.com/isvbytes/codexplusplus/blob/master/src/protocol_proxy.rs)
的忠实 Go 移植。

---

## 特性

- **单文件静态二进制** —— 无 CGO、无 Node.js、无 Python 依赖。交叉编译支持
  `linux/amd64`、`linux/arm64`、`windows/amd64`。
- **内嵌管理 UI** —— Vue 3 SPA 通过 `go:embed` 打包进二进制，访问
  `http://<host>:<port>/` 即可管理上游、模型映射、查看请求日志。
- **多提供方聚合** —— 在一个 `base_url` 后面同时驱动 DeepSeek、智谱 GLM、MiniMax、小米 MiMo，
  以及任何 OpenAI / Anthropic 兼容的提供方
- **多协议桥接**
  - 入站：OpenAI Responses（`/v1/responses`）、OpenAI Chat Completions（`/v1/chat/completions`）
  - 出站：OpenAI 兼容或 Anthropic 兼容（`api_format: "openai_chat" | "anthropic"`）
  - 全部支持流式和非流式；非流响应会**反向转换**回调用方协议（Anthropic → Chat Completions，Anthropic → Responses）
- **多上游路由** —— 配置多个上游、独立启停、每个上游独立的 `model_mapping`
  （如 `gpt-4 → qwen3.7-plus`）、重试次数、超时时间
- **原子化配置持久化** —— Admin API 写入走 `tmp + rename`，避免 ConfigMap 并发写、避免损坏
- **实时请求日志** —— 内存中保留最近 1000 条请求，含 model/upstream/latency/tools，方便调试工具调用
- **生产可用** —— 设计为可作为 sidecar 或 K8s in-cluster 部署

---

## 快速开始

### 运行预编译二进制

```bash
# linux/amd64
curl -L -o codex-relayx https://github.com/iamsee/codex-relayx/releases/latest/download/codex-relayx-linux-amd64
chmod +x codex-relayx
./codex-relayx --port 8001 --data-dir ./data
```

浏览器打开 <http://127.0.0.1:8001/> 进入管理 UI。

### Docker 启动

```bash
docker run -d --name codex-relayx \
  -p 8001:8001 \
  -v $(pwd)/data:/var/lib/codex-relayx \
  iamsee/codex-relayx:latest
```

### 从源码编译

需要 Go 1.22+。

```bash
git clone https://github.com/iamsee/codex-relayx.git
cd codex-relayx
go build -o codex-relayx ./cmd/codex-relayx
./codex-relayx --port 8001 --data-dir ./data
```

前端文件位于 `assets/dist/`，编译时打包进二进制。如需修改前端，直接编辑该目录后重新编译。

---

## 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/v1/responses` | OpenAI Responses API（Codex CLI 使用） |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions API（new-api、Open WebUI 等使用） |
| `GET`  | `/v1/models` | 列出已配置的模型 |
| `GET`  | `/admin/api/stats` | 运行时统计（运行时长、请求/错误计数） |
| `GET`  | `/admin/api/config` | 读取当前配置 |
| `POST` | `/admin/api/config` | 更新配置（原子写） |
| `GET`  | `/admin/api/logs?limit=N` | 最近的请求日志 |
| `GET`  | `/` | 管理 UI（Vue 3 SPA） |

### 快速冒烟测试

```bash
# Chat Completions（底层走 Anthropic 上游）
curl -s http://127.0.0.1:8001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"只回复数字 42"}]}' | jq
```

---

## 配置

首次启动时，`codex-relayx` 会在数据目录写入默认 `config.json`。通过管理 UI 或直接编辑文件：

```json
{
  "listen_port": 8001,
  "admin_password": "",
  "request_timeout_secs": 120,
  "upstreams": [
    {
      "id": "bailian-anthropic",
      "name": "阿里云百炼（Anthropic 兼容）",
      "enabled": true,
      "base_url": "https://coding.dashscope.aliyuncs.com/apps/anthropic/v1",
      "api_key": "<your-api-key>",
      "api_format": "anthropic",
      "model_mapping": { "gpt-4": "qwen3.7-plus" },
      "max_retries": 3
    }
  ],
  "model_mapping": {}
}
```

字段详细说明见 [`docs/config.md`](docs/config.md)。

| 字段 | 必填 | 说明 |
|---|---|---|
| `api_format` | 是 | `openai_chat` 或 `anthropic` —— 决定 header 和 body 转换 |
| `base_url` | 是 | OpenAI 协议末尾加 `/v1`；Anthropic 兼容同样末尾加 `/v1`；代码会自动去重 `/v1` |
| `api_key` | 是 | OpenAI 走 `Authorization: Bearer …`；Anthropic 走 `x-api-key` |
| `model_mapping` | 否 | 入站模型名 → 上游模型名 |

---

## 命令行参数

```
codex-relayx [flags]
  --port int         HTTP 服务端口（默认 8001）
  --data-dir string  config.json 和日志的数据目录（默认 "./data"）
  --config string    显式指定 config 文件路径（覆盖 data-dir/config.json）
```

环境变量（覆盖命令行参数）：

- `RELAYX_PORT`
- `RELAYX_DATA_DIR`
- `RELAYX_CONFIG`

---

## 部署

- **Docker Compose** —— 见 [`docker-compose.yml`](docker-compose.yml)
- **Kubernetes** —— 见 [`docs/deploy.md`](docs/deploy.md)，含完整的 Deployment
  模板（hostPath 或 PVC、NodePort/Ingress 示例、Secret 用法）
- **裸机** —— 用 `systemd` / `supervisord` 托管二进制即可

---

## 项目结构

```
codex-relayx/
├── cmd/codex-relayx/main.go    # 入口，命令行参数解析
├── internal/
│   ├── config/                 # AppConfig + JSON 加载/保存
│   ├── state/                  # 线程安全运行时状态 + 日志环形缓冲
│   ├── proxy/                  # 协议转换核心
│   │   ├── handler.go          #   HTTP handler（Responses / Chat / Models）
│   │   └── stream_converter.go #   SSE 事件转换（Anthropic ↔ Responses）
│   ├── api/                    # 管理 API（config / stats / logs）
│   ├── server/                 # Gin 服务装配 + CORS
│   └── web/                    # SPA fallback（加载嵌入的 dist/）
└── assets/
    ├── dist/                   # 预构建的 Vue 3 SPA
    └── static.go               # //go:embed dist/*
```

### 协议转换

转换层是 Rust 参考实现
[`codexplusplus/src/protocol_proxy.rs`](https://github.com/isvbytes/codexplusplus/blob/master/src/protocol_proxy.rs)
的忠实 Go 移植。核心要点：

- **Anthropic SSE → Responses SSE** —— `message_start`、`content_block_start`、
  `content_block_delta`、`content_block_stop`、`message_stop`、`ping` 翻译成 Responses
  事件序列：`response.created` → `output_item.added` → `content_part.added` →
  `output_text.delta` / `output_text.done` → `content_part.done` → `output_item.done` →
  `response.completed`
- **推理事件** —— Anthropic `thinking` 块映射为
  `reasoning_summary_part.added` / `reasoning_summary_text.delta` / `…done`
- **工具调用** —— Anthropic `tool_use` 块映射为 Responses `function_call` 项，
  带 `call_id` 和 `fc_…` ID。工具名向上游扁平化为 `namespace__name`，回程时再反向解析
- **双重编码 arguments** —— Codex CLI 发来的 `function_call.arguments` 是双重
  编码的 JSON 字符串，转换时**解析两次**拿到真正的 object，再转发给 Anthropic 协议上游
  （上游会拒绝 string 类型的 input）
- **Chat ↔ Anthropic** —— `api_format: "anthropic"` 上游 + 非流 `/v1/chat/completions`
  入站时，链路是 Chat → Anthropic → Chat，使 new-api、Open WebUI 等 OpenAI 风格调用方
  透明使用

### 为什么要 Go 重写？

Rust 原型 [`codexplusplus`](https://github.com/isvbytes/codexplusplus) 作为研究/
原型非常合适，但生产环境我们想要：

- 单个静态二进制，丢到任何节点都能跑（避免 glibc/musl 不一致）
- 服务模式，可重启、可热重载配置，不依赖额外编排
- 内嵌管理 UI，无需另起 nginx 托管静态资源
- 更轻的运行时内存占用（适配 k3s 节点）

---

## 开发

```bash
# 格式化与静态检查
gofmt -s -w .
go vet ./...

# 编译
go build -o codex-relayx ./cmd/codex-relayx

# 交叉编译
GOOS=linux  GOARCH=amd64 go build -o dist/codex-relayx-linux-amd64     ./cmd/codex-relayx
GOOS=linux  GOARCH=arm64 go build -o dist/codex-relayx-linux-arm64     ./cmd/codex-relayx
GOOS=windows GOARCH=amd64 go build -o dist/codex-relayx-windows-amd64.exe ./cmd/codex-relayx

# Docker
docker build -t iamsee/codex-relayx:dev .
```

---

## 发布

打 tag 自动触发 `.github/workflows/release.yml`：

```bash
git tag v0.1.0
git push origin v0.1.0
```

自动执行：

1. 交叉编译 `linux/amd64`、`linux/arm64`、`windows/amd64`
2. 计算 `SHA256SUMS`
3. 创建 GitHub Release，上传二进制 + 校验和

默认流水线**不需要任何 GitHub Secrets**——全跑在公网的 `ubuntu-latest` runner 上。

---

## 相关项目

- [`codexplusplus`](https://github.com/isvbytes/codexplusplus) —— Rust 参考实现；
  协议转换遇到疑问时以其 `protocol_proxy.rs` 为真理来源

---

## 许可证

[MIT](LICENSE) © isvbytes.com

## Star History

<a href="https://star-history.com/#iamsee/codex-relayx&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=iamsee/codex-relayx&type=Date&theme=dark" />
    <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=iamsee/codex-relayx&type=Date" />
    <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=iamsee/codex-relayx&type=Date" />
  </picture>
</a>

# Codex-Relayx

[![Go Version](https://img.shields.io/github/go-mod/go-version/isvbytes/codex-relayx)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/isvbytes/codex-relayx)](../../releases)
[![Docker Hub](https://img.shields.io/docker/v/isvbytes/codex-relayx?label=docker)](https://hub.docker.com/r/isvbytes/codex-relayx)

A lightweight, single-binary LLM protocol-conversion gateway written in Go.

## Why this project exists

**Codex-Relayx exists so that the OpenAI Codex CLI can directly call
DeepSeek, Zhipu GLM, MiniMax, and Xiaomi MiMo models, as if they were first-class
OpenAI/Anthropic providers.**

The Codex CLI speaks two protocols natively:

- **OpenAI Responses API** (`/v1/responses`) — the modern path, used for `gpt-5` and reasoning models
- **OpenAI Chat Completions API** (`/v1/chat/completions`) — the legacy path, used for older `gpt-4` family

But the providers we want to call in practice — all of them Chinese — are not
on OpenAI's wire:

| Provider | Real company | Official API protocol |
|---|---|---|
| **DeepSeek** | 深度求索 (DeepSeek AI) | OpenAI Chat Completions compatible (`https://api.deepseek.com`) |
| **GLM** | 智谱 AI (Zhipu AI / Z.ai) | OpenAI Chat Completions compatible; Anthropic Messages compatible at `https://open.bigmodel.cn/api/anthropic` |
| **MiniMax** | MiniMax（稀宇科技） | Anthropic Messages compatible (`https://api.MiniMax.chat/anthropic`) + OpenAI compatible (`https://api.MiniMax.chat/v1`) |
| **MiMo** | Xiaomi（小米） | OpenAI Chat Completions compatible |

The result: Codex CLI can only talk to OpenAI-shaped endpoints, but the models
we want to drive (DeepSeek-V3/R1, GLM-4.5/4.6/5, MiniMax-M3, MiMo) live behind
a mix of OpenAI-compatible and Anthropic-compatible endpoints, each with their
own quirks (header names, SSE format, model name casing, etc.).

**Codex-Relayx bridges that gap.** Drop it in front of any of these providers,
set `api_format` to `openai_chat` or `anthropic`, and Codex CLI can call them
with no client-side changes. The same gateway also speaks
`/v1/chat/completions` natively, so OpenAI-style consumers
([new-api](https://github.com/songquanpeng/one-api), Open WebUI, etc.) work
transparently.

Concretely, the gateway is what made it possible to:

- Run `codex` against `qwen3.7-plus` on **Aliyun Bailian** (Anthropic-compatible) for the same multi-turn tool-calling flows that work on `gpt-5`
- Aggregate four domestic providers behind a single `base_url`, with one `model_mapping` table controlling the fan-out
- Use a single admin UI to switch providers, hot-reload config, and inspect request logs

## How it works

```
                  ┌─────────────────────────────────┐
                  │       codex-relayx (this)       │
                  │  /v1/responses (Codex CLI)      │
   Codex CLI ────►│  /v1/chat/completions (new-api) │────► DeepSeek (openai_chat)
                  │  /v1/models, /admin/api/*, /    │────► Zhipu GLM (openai_chat or anthropic)
                  └─────────────────────────────────┘────► MiniMax (anthropic or openai_chat)
                                                        ────► MiMo (openai_chat)
```

The conversion layer is a faithful Go port of the Rust reference
[`codexplusplus/src/protocol_proxy.rs`](https://github.com/isvbytes/codexplusplus/blob/master/src/protocol_proxy.rs).

---

## Features

- **Single static binary** — no CGO, no Node.js, no Python. Cross-compiles to
  `linux/amd64`, `linux/arm64`, and `windows/amd64`.
- **Embedded admin UI** — Vue 3 SPA shipped inside the binary via `go:embed`.
  Visit `http://<host>:<port>/` to manage upstreams, model mapping, and view request logs.
- **Multi-provider fan-out** — drive DeepSeek, Zhipu GLM, MiniMax, Xiaomi MiMo, and
  any other OpenAI- or Anthropic-compatible provider from a single `base_url`.
- **Multi-protocol bridge**
  - Inbound: OpenAI Responses (`/v1/responses`), OpenAI Chat Completions (`/v1/chat/completions`)
  - Outbound: OpenAI-compatible or Anthropic-compatible (`api_format: "openai_chat" | "anthropic"`)
  - Stream and non-stream for all directions; non-stream responses are normalized back to the
    caller's protocol (`Anthropic → Chat Completions`, `Anthropic → Responses`).
- **Multi-upstream routing** — configure multiple upstreams, enable/disable per id,
  per-upstream `model_mapping` (e.g. `gpt-4 → qwen3.7-plus`), retries, and timeouts.
- **Atomic config persistence** — admin API writes go through `tmp + rename`, no
  configmap gymnastics, no concurrent-write corruption.
- **Live request log** — last 1000 requests in memory with model/upstream/latency/tools
  for debugging tool-calling flows.
- **Production-ready** — designed to run as a sidecar or in-cluster under K8s.

---

## Quick Start

### Run the prebuilt binary

```bash
# linux/amd64
curl -L -o codex-relayx https://github.com/isvbytes/codex-relayx/releases/latest/download/codex-relayx-linux-amd64
chmod +x codex-relayx
./codex-relayx --port 8001 --data-dir ./data
```

Then open <http://127.0.0.1:8001/> for the admin UI.

### Run with Docker

```bash
docker run -d --name codex-relayx \
  -p 8001:8001 \
  -v $(pwd)/data:/var/lib/codex-relayx \
  isvbytes/codex-relayx:latest
```

### Build from source

Requires Go 1.22+.

```bash
git clone https://github.com/isvbytes/codex-relayx.git
cd codex-relayx
go build -o codex-relayx ./cmd/codex-relayx
./codex-relayx --port 8001 --data-dir ./data
```

The admin UI under `assets/dist/` is bundled into the binary at build time.
If you need to modify the UI, edit the files there and rebuild.

---

## Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/responses` | OpenAI Responses API (used by Codex CLI) |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions API (used by new-api, Open WebUI, etc.) |
| `GET`  | `/v1/models` | List configured models |
| `GET`  | `/admin/api/stats` | Runtime stats (uptime, request/error counts) |
| `GET`  | `/admin/api/config` | Read current config |
| `POST` | `/admin/api/config` | Update config (atomic write) |
| `GET`  | `/admin/api/logs?limit=N` | Recent request logs |
| `GET`  | `/` | Admin UI (Vue 3 SPA) |

### Quick smoke test

```bash
# Chat Completions (Anthropic upstream under the hood)
curl -s http://127.0.0.1:8001/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Reply with the number 42 only."}]}' | jq
```

---

## Configuration

On first start, `codex-relayx` writes a default `config.json` into the data directory.
Edit it through the admin UI or directly:

```json
{
  "listen_port": 8001,
  "admin_password": "",
  "request_timeout_secs": 120,
  "upstreams": [
    {
      "id": "bailian-anthropic",
      "name": "Aliyun Bailian (Anthropic-compatible)",
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

Field reference — see [`docs/config.md`](docs/config.md).

| Field | Required | Notes |
|---|---|---|
| `api_format` | yes | `openai_chat` or `anthropic` — drives header + body conversion |
| `base_url` | yes | End with `/v1` for OpenAI or `/v1` for Anthropic-compatible. Trailing `/v1` is auto-trimmed if duplicated |
| `api_key` | yes | Sent as `Authorization: Bearer …` (OpenAI) or `x-api-key` (Anthropic) |
| `model_mapping` | no | Inbound model name → upstream model name |

---

## CLI flags

```
codex-relayx [flags]
  --port int         HTTP server port (default 8001)
  --data-dir string  Data directory for config.json and logs (default "./data")
  --config string    Explicit config file path (overrides data-dir/config.json)
```

Environment variables (override flags):

- `RELAYX_PORT`
- `RELAYX_DATA_DIR`
- `RELAYX_CONFIG`

---

## Deployment

- **Docker Compose** — see [`docker-compose.yml`](docker-compose.yml)
- **Kubernetes** — see [`docs/deploy.md`](docs/deploy.md) for a hardened Deployment
  (hostPath or PVC, NodePort/Ingress example, secret handling)
- **Bare metal** — just run the binary under `systemd` / `supervisord`

---

## Architecture

```
codex-relayx/
├── cmd/codex-relayx/main.go    # entrypoint, flag parsing
├── internal/
│   ├── config/                 # AppConfig + JSON load/save
│   ├── state/                  # thread-safe runtime state + log ring buffer
│   ├── proxy/                  # protocol conversion
│   │   ├── handler.go          #   HTTP handlers (Responses / Chat / Models)
│   │   └── stream_converter.go #   SSE event conversion (Anthropic ↔ Responses)
│   ├── api/                    # admin API (config / stats / logs)
│   ├── server/                 # Gin server wiring + CORS
│   └── web/                    # SPA fallback (serves embedded dist/)
└── assets/
    ├── dist/                   # prebuilt Vue 3 SPA
    └── static.go               # //go:embed dist/*
```

### Protocol conversion

The conversion layer is a faithful Go port of the Rust
[`codexplusplus/src/protocol_proxy.rs`](https://github.com/isvbytes/codexplusplus/blob/master/src/protocol_proxy.rs)
reference implementation. Conversion details:

- **Anthropic SSE → Responses SSE** — translates `message_start`, `content_block_start`,
  `content_block_delta`, `content_block_stop`, `message_stop`, `ping` into the
  Responses event sequence: `response.created` → `output_item.added` →
  `content_part.added` → `output_text.delta` / `output_text.done` →
  `content_part.done` → `output_item.done` → `response.completed`.
- **Reasoning events** — Anthropic `thinking` blocks become
  `reasoning_summary_part.added` / `reasoning_summary_text.delta` / `…done`.
- **Tool calls** — Anthropic `tool_use` blocks become Responses `function_call`
  items with `call_id` and `fc_…` ids. Tool names are flat-`namespace__name` for
  the upstream, reversed on the way back.
- **Double-encoded arguments** — `function_call.arguments` from Codex CLI is
  double-encoded JSON; we parse twice to get a real object before forwarding to
  Anthropic-style upstreams (which reject string inputs).
- **Chat ↔ Anthropic** — non-stream `/v1/chat/completions` against an
  `api_format: "anthropic"` upstream is converted Chat → Anthropic → Chat, so
  OpenAI-style callers (new-api, Open WebUI, …) work transparently.

### Why a separate Go rewrite?

The Rust reference ([`codexplusplus`](https://github.com/isvbytes/codexplusplus))
is great as a research/prototype target, but in production we want:

- One static binary that drops onto any node (no glibc/musl mismatch).
- A server mode that survives restarts and config reloads without ceremony.
- Embedded admin UI — no separate nginx/static-hosting step.
- Slightly leaner runtime memory profile for the k3s node it lives on.

---

## Development

```bash
# Format & vet
gofmt -s -w .
go vet ./...

# Build
go build -o codex-relayx ./cmd/codex-relayx

# Cross-compile (examples)
GOOS=linux  GOARCH=amd64 go build -o dist/codex-relayx-linux-amd64  ./cmd/codex-relayx
GOOS=linux  GOARCH=arm64 go build -o dist/codex-relayx-linux-arm64  ./cmd/codex-relayx
GOOS=windows GOARCH=amd64 go build -o dist/codex-relayx-windows-amd64.exe ./cmd/codex-relayx

# Docker
docker build -t isvbytes/codex-relayx:dev .
```

---

## Release

Releases are cut automatically by `.github/workflows/release.yml` on a pushed tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

That triggers:

1. Cross-compile to `linux/amd64`, `linux/arm64`, `windows/amd64`.
2. Compute `SHA256SUMS`.
3. Publish a GitHub Release with the binaries + checksums attached.
4. Build and push multi-arch Docker images to Docker Hub:
   `isvbytes/codex-relayx:v0.1.0`, `:0.1`, `:latest`.

### Required GitHub secrets

| Secret | Purpose |
|---|---|
| `DOCKERHUB_USERNAME` | Docker Hub login |
| `DOCKERHUB_TOKEN` | Docker Hub access token (https://hub.docker.com/settings/security) |

No other secrets are required for the default pipeline.

---

## Related projects

- [`codexplusplus`](https://github.com/isvbytes/codexplusplus) — the Rust reference
  implementation; treat its `protocol_proxy.rs` as the source of truth for
  protocol conversion.

---

## License

[MIT](LICENSE) © isvbytes.com

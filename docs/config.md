# Configuration Reference

`config.json` lives at `<data-dir>/config.json` (default `./data/config.json`).
The admin UI writes updates atomically (`tmp + rename`); the file can also be
edited by hand and re-read on the next admin-API call.

## Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `listen_port` | int | `8001` | (legacy; the live port is set by `--port`) |
| `admin_password` | string | `""` | (reserved; auth gating not yet implemented) |
| `request_timeout_secs` | int | `120` | Default upstream HTTP timeout |
| `upstreams` | array | `[default]` | One or more upstream providers |
| `model_mapping` | object | `{}` | Global inbound → outbound model name map (applied before per-upstream mapping) |

## Upstream object

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Stable identifier; used in logs and admin UI |
| `name` | string | yes | Human-readable label |
| `enabled` | bool | yes | Whether requests can be routed to this upstream |
| `base_url` | string | yes | Provider base URL. Trailing `/v1` is auto-stripped to avoid double-`/v1` paths |
| `api_key` | string | yes | API key for the upstream. Sent as `Authorization: Bearer …` (OpenAI) or `x-api-key` (Anthropic) |
| `api_format` | string | yes | `"openai_chat"` or `"anthropic"` — drives header choice, body conversion, and SSE event mapping |
| `model_mapping` | object | no | `{ "<inbound>": "<outbound>" }` map applied after the global `model_mapping` |
| `timeout_secs` | int | no | Per-upstream override of the global `request_timeout_secs` |
| `max_retries` | int | `3` | Number of retries on transient upstream failures |

## Routing rules

1. The first **enabled** upstream is used; per-upstream selection is not yet
   implemented (single-upstream operation today).
2. The inbound model name is looked up in:
   1. global `model_mapping` → upstream `model_mapping`, in that order.
   2. If no match, the inbound name is sent as-is.
3. If the upstream returns a 4xx, no retry. 5xx and network errors trigger
   `max_retries` with exponential backoff (1s → 2s → 4s).

## Notes on `api_format`

- `openai_chat` — request body is forwarded unchanged, `Authorization: Bearer …` header.
- `anthropic` — body is converted to the Anthropic Messages format
  (`chatToAnthropic`), `x-api-key` + `anthropic-version: 2023-06-01` + `User-Agent: curl/8.0` headers.
  Non-stream responses are converted back to the caller's expected protocol
  (Responses or Chat Completions) before being returned.

## Example: Bailian (Aliyun) Anthropic endpoint

```json
{
  "upstreams": [
    {
      "id": "bailian-anthropic",
      "name": "Aliyun Bailian (Anthropic-compatible)",
      "enabled": true,
      "base_url": "https://coding.dashscope.aliyuncs.com/apps/anthropic/v1",
      "api_key": "sk-sp-...",
      "api_format": "anthropic",
      "model_mapping": { "gpt-4": "qwen3.7-plus" },
      "max_retries": 3
    }
  ]
}
```

Bailian's Anthropic-compatible endpoint is sensitive to:
- `User-Agent: curl/8.0` (already set by codex-relayx)
- `x-api-key` header (also handled)
- HTTP/1.1 (no HTTP/2 — handled by transport config)
- Trailing `/v1` in `base_url` (auto-trimmed)

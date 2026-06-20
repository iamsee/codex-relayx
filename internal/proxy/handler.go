package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"isvbytes.com/codex-relayx/internal/config"
	"isvbytes.com/codex-relayx/internal/state"
	"go.uber.org/zap"
)

// Handler 协议转换处理器
type Handler struct {
	state  *state.AppState
	client *http.Client
	logger *zap.Logger
}

// NewHandler 创建处理器
func NewHandler(s *state.AppState, logger *zap.Logger) *Handler {
	// 禁用 HTTP/2：部分 Anthropic 兼容端点（如百炼）不支持
	transport := &http.Transport{
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Handler{
		state: s,
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: transport,
			// 禁止自动重定向，避免 SSE 流被重定向中断
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger: logger,
	}
}

// ChatCompletionsRequest Chat Completions 请求格式
type ChatCompletionsRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Stream     bool          `json:"stream"`
	Tools      []ChatTool    `json:"tools,omitempty"`
	Temperature any           `json:"temperature,omitempty"`
	TopP       any           `json:"top_p,omitempty"`
	MaxTokens  any           `json:"max_tokens,omitempty"`
	ToolChoice any           `json:"tool_choice,omitempty"`
}

// ChatMessage 消息
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatTool 工具定义
type ChatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

// ResponsesRequest Responses API 请求格式（Codex CLI 使用）
type ResponsesRequest struct {
	Model string           `json:"model"`
	Input []ResponseItem   `json:"input"`
	Tools []ResponsesTool  `json:"tools,omitempty"`
	Stream bool            `json:"stream,omitempty"`
}

// ResponsesTool Responses API 工具定义（没有 function 包装层）
// 支持 codex 的 namespace 工具：{type:"namespace", name:"<ns>", tools:[{type:"function",...}]}
type ResponsesTool struct {
	Type        string          `json:"type"`                  // "function" | "namespace" | "custom" | ...
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema any             `json:"input_schema,omitempty"`
	Parameters  any             `json:"parameters,omitempty"` // 兼容 OpenAI 格式
	Tools       []ResponsesTool `json:"tools,omitempty"`       // namespace 子工具
}

// ResponseItem 响应项（可以是 message、function_call、function_call_output 等）
type ResponseItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"` // codex namespace 工具的命名空间
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

// HandleChatCompletions 处理 /v1/chat/completions 请求
// 对齐 codexpp handle_chat_completions：
//   - api_format=anthropic：chat → anthropic messages，发 /v1/messages，非流响应转回 chat
//   - 其他：透传 /v1/chat/completions
func (h *Handler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 解析模型
	modelName, _ := req["model"].(string)
	targetModel, upstream := h.state.ResolveModel(modelName)
	if upstream == nil {
		http.Error(w, "no enabled upstream", http.StatusServiceUnavailable)
		return
	}

	// 用 statusRecorder 捕获实际写入的状态码
	sw := &statusRecorder{ResponseWriter: w, status: 200}

	// 记录日志
	defer func() {
		h.state.RecordRequest(state.LogEntry{
			Method:        "POST",
			Path:          "/v1/chat/completions",
			Model:         modelName,
			UpstreamName:  upstream.Name,
			UpstreamModel: targetModel,
			StatusCode:    sw.status,
			LatencyMs:     time.Since(start).Milliseconds(),
		})
	}()

	// 用目标上游 model 覆盖
	req["model"] = targetModel

	// 根据 api_format 选择目标
	if upstream.APIFormat == "anthropic" {
		h.handleChatCompletionsAnthropic(sw, r, req, upstream, modelName, targetModel)
		return
	}

	// OpenAI Chat Completions 透传
	resp, cancel, err := h.forwardToUpstreamStreaming(upstream, "/v1/chat/completions", req, false)
	if err != nil {
		h.logger.Error("upstream error", zap.Error(err))
		sw.status = http.StatusBadGateway
		http.Error(sw, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	defer cancel()

	// 透传响应
	sw.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	sw.status = resp.StatusCode
	sw.WriteHeader(resp.StatusCode)
	io.Copy(sw, resp.Body)
}

// handleChatCompletionsAnthropic chat → anthropic 转换 + 转发
// 对齐 codexpp handle_chat_completions 的 anthropic 分支（1225-1369）
func (h *Handler) handleChatCompletionsAnthropic(
	sw *statusRecorder,
	r *http.Request,
	chatReq map[string]any,
	upstream *config.UpstreamConfig,
	origModel, targetModel string,
) {
	isStream, _ := chatReq["stream"].(bool)
	if isStream {
		chatReq["stream"] = true
	}

	// chat → anthropic messages
	chatForConvert := ChatCompletionsRequest{
		Model:    targetModel,
		Stream:   isStream,
	}
	if msgs, ok := chatReq["messages"].([]any); ok {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			role, _ := mm["role"].(string)
			content, _ := mm["content"].(string)
			cm := ChatMessage{Role: role, Content: content}
			// tool_calls
			if tcs, ok := mm["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcm, _ := tc.(map[string]any)
					fm, _ := tcm["function"].(map[string]any)
					tc2 := ToolCall{ID: stringOf(tcm["id"]), Type: stringOf(tcm["type"])}
					tc2.Function.Name = stringOf(fm["name"])
					tc2.Function.Arguments = stringOf(fm["arguments"])
					cm.ToolCalls = append(cm.ToolCalls, tc2)
				}
			}
			chatForConvert.Messages = append(chatForConvert.Messages, cm)
		}
	}
	if tools, ok := chatReq["tools"].([]any); ok {
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			fm, _ := tm["function"].(map[string]any)
			ct := ChatTool{Type: stringOf(tm["type"])}
			ct.Function.Name = stringOf(fm["name"])
			ct.Function.Description = stringOf(fm["description"])
			ct.Function.Parameters = fm["parameters"]
			chatForConvert.Tools = append(chatForConvert.Tools, ct)
		}
	}
	if v, ok := chatReq["temperature"]; ok {
		chatForConvert.Temperature = v
	}
	if v, ok := chatReq["top_p"]; ok {
		chatForConvert.TopP = v
	}
	if v, ok := chatReq["max_tokens"]; ok {
		chatForConvert.MaxTokens = v
	}
	if v, ok := chatReq["tool_choice"]; ok {
		chatForConvert.ToolChoice = v
	}

	anthropicBody := h.chatToAnthropic(chatForConvert)

	resp, cancel, err := h.forwardToUpstreamStreaming(upstream, "/v1/messages", anthropicBody, isStream)
	if err != nil {
		h.logger.Error("upstream error", zap.Error(err))
		sw.status = http.StatusBadGateway
		http.Error(sw, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	defer cancel()

	if isStream {
		// TODO: codexpp 标 TODO 透传，这里也透传（大多数客户端暂时够用）
		sw.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		sw.status = resp.StatusCode
		sw.WriteHeader(resp.StatusCode)
		io.Copy(sw, resp.Body)
		return
	}

	// 非流：把 anthropic 响应转回 chat completions
	respBody, _ := io.ReadAll(resp.Body)
	sw.status = resp.StatusCode
	if resp.StatusCode >= 400 {
		sw.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		sw.WriteHeader(resp.StatusCode)
		sw.Write(respBody)
		return
	}
	var anthResp map[string]any
	if err := json.Unmarshal(respBody, &anthResp); err != nil {
		sw.Header().Set("Content-Type", "application/json")
		sw.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(sw).Encode(map[string]any{
			"error": map[string]any{
				"message": "failed to parse upstream response: " + err.Error(),
				"type":    "upstream_error",
			},
		})
		return
	}

	chatResp := anthropicToChatCompletion(anthResp, origModel, targetModel)
	sw.Header().Set("Content-Type", "application/json")
	sw.WriteHeader(http.StatusOK)
	json.NewEncoder(sw).Encode(chatResp)
}

// anthropicToChatCompletion 把 Anthropic Messages 非流响应转回 Chat Completions 格式
// 对齐 codexpp anthropic_to_chat_completion（1628 行附近）
func anthropicToChatCompletion(anthResp map[string]any, origModel, targetModel string) map[string]any {
	// 提取文本内容
	var textContent string
	if content, ok := anthResp["content"].([]any); ok {
		for _, block := range content {
			if b, ok := block.(map[string]any); ok {
				if b["type"] == "text" {
					if t, ok := b["text"].(string); ok {
						textContent += t
					}
				}
			}
		}
	}

	// 提取 usage
	var inputTokens, outputTokens int64
	if usage, ok := anthResp["usage"].(map[string]any); ok {
		if t, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int64(t)
		}
		if t, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int64(t)
		}
	}

	modelName, _ := anthResp["model"].(string)
	if modelName == "" {
		modelName = targetModel
	}
	anthID, _ := anthResp["id"].(string)

	// OpenAI Chat Completions 格式
	chatID := "chatcmpl-" + genID("", 24)
	return map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": textContent,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
		"_anthropic_id": anthID, // 调试用，可去掉
	}
}

// statusRecorder 包装 ResponseWriter 以捕获 WriteHeader 写入的状态码
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.written {
		sr.status = code
		sr.written = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.written {
		sr.status = 200
		sr.written = true
	}
	return sr.ResponseWriter.Write(b)
}

// Flush / Hijack / Pusher 透传到底层 ResponseWriter，确保 SSE 流式可用
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sr *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := sr.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (sr *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := sr.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// HandleResponses 处理 /v1/responses 请求（Codex CLI 主用）
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 解析模型
	targetModel, upstream := h.state.ResolveModel(req.Model)
	if upstream == nil {
		http.Error(w, "no enabled upstream", http.StatusServiceUnavailable)
		return
	}

	// 用 statusRecorder 捕获实际写入的状态码
	sw := &statusRecorder{ResponseWriter: w, status: 200}

	// 记录日志
	defer func() {
		h.state.RecordRequest(state.LogEntry{
			Method:        "POST",
			Path:          "/v1/responses",
			Model:         req.Model,
			UpstreamName:  upstream.Name,
			UpstreamModel: targetModel,
			StatusCode:    sw.status,
			LatencyMs:     time.Since(start).Milliseconds(),
		})
	}()

	// 转换 Responses → Chat Completions（同时构建工具名映射）
	chatReq, toolNameMap := h.responsesToChat(req)
	chatReq.Model = targetModel
	chatReq.Stream = req.Stream

	// 根据 api_format 选择目标
	targetPath := "/v1/chat/completions"
	if upstream.APIFormat == "anthropic" {
		// 转换为 Anthropic Messages 格式
		anthropicReq := h.chatToAnthropic(chatReq)
		resp, cancel, err := h.forwardToUpstreamStreaming(upstream, "/v1/messages", anthropicReq, req.Stream)
		if err != nil {
			sw.status = http.StatusBadGateway
			http.Error(sw, "upstream error: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		defer cancel() // 等响应体读完再 cancel

		if req.Stream {
			h.StreamAnthropicToResponses(sw, resp, toolNameMap)
		} else {
			h.nonStreamAnthropicToResponses(sw, resp, &chatReq)
		}
		return
	}

	// OpenAI Chat Completions
	resp, cancel, err := h.forwardToUpstreamStreaming(upstream, targetPath, chatReq, req.Stream)
	if err != nil {
		sw.status = http.StatusBadGateway
		http.Error(sw, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	defer cancel() // 等响应体读完再 cancel

	if req.Stream {
		h.StreamChatToResponses(sw, resp, toolNameMap)
	} else {
		h.nonStreamChatToResponses(sw, resp)
	}
}

// forwardToUpstream 转发 JSON 请求
func (h *Handler) forwardToUpstream(upstream *config.UpstreamConfig, path string, body any, stream bool) (*http.Response, error) {
	resp, _, err := h.forwardToUpstreamStreaming(upstream, path, body, stream)
	return resp, err
}

// forwardToUpstreamStreaming 转发请求并返回 cancel 函数（流式必须主动 cancel，否则 SSE 被 ctx 中断）
func (h *Handler) forwardToUpstreamStreaming(upstream *config.UpstreamConfig, path string, body any, stream bool) (*http.Response, context.CancelFunc, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}

	baseURL := strings.TrimRight(upstream.BaseURL, "/")
	// 智能处理路径：避免 base_url 已含 /v1 时再拼接 /v1
	fullPath := path
	if strings.HasSuffix(baseURL, "/v1") && strings.HasPrefix(path, "/v1/") {
		fullPath = strings.TrimPrefix(path, "/v1")
	}
	url := baseURL + fullPath

	timeout := 120 * time.Second
	if upstream.TimeoutSecs != nil {
		timeout = time.Duration(*upstream.TimeoutSecs) * time.Second
	}

	// 流式用 noTimeout ctx（避免 SSE 长连接被取消）
	// 非流式仍用 timeout
	var ctx context.Context
	var cancel context.CancelFunc
	if stream {
		ctx, cancel = context.WithCancel(context.Background())
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		cancel()
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "codex-relayx/1.0")

	// Anthropic 格式使用 x-api-key 鉴权（百炼 / 官方都要求）
	if upstream.APIFormat == "anthropic" {
		req.Header.Set("x-api-key", upstream.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		// 百炼 Anthropic 端点对 HTTP/2 / UA / 重定向较敏感，模仿 curl
		req.Header.Set("User-Agent", "curl/8.0")
	} else {
		req.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	}

	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := h.client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// responsesToChat 转换 Responses API → Chat Completions
// 同时返回工具名映射（扁平名 → name/namespace），供响应侧还原
func (h *Handler) responsesToChat(req ResponsesRequest) (ChatCompletionsRequest, map[string]ToolNameMapping) {
	chatReq := ChatCompletionsRequest{
		Model:  req.Model,
		Stream: req.Stream,
	}
	toolNameMap := make(map[string]ToolNameMapping)

	// 转换 ResponsesTool → ChatTool（对齐 codexpp build_codex_tool_context + responses_tools_to_chat_tools）
	for _, rt := range req.Tools {
		switch rt.Type {
		case "namespace":
			// codex MCP 工具：扁平化子工具为 "{namespace}__{name}" 发给上游
			for _, child := range rt.Tools {
				if child.Type != "function" && child.Type != "" {
					continue
				}
				flat := flattenNamespaceToolName(rt.Name, child.Name)
				schema := pickSchema(child.InputSchema, child.Parameters)
				ct := ChatTool{Type: "function"}
				ct.Function.Name = flat
				ct.Function.Description = child.Description
				ct.Function.Parameters = schema
				chatReq.Tools = append(chatReq.Tools, ct)
				toolNameMap[flat] = ToolNameMapping{Name: child.Name, Namespace: rt.Name}
			}
		case "function", "":
			// 普通 function 工具：name 原样
			schema := pickSchema(rt.InputSchema, rt.Parameters)
			ct := ChatTool{Type: "function"}
			ct.Function.Name = rt.Name
			ct.Function.Description = rt.Description
			ct.Function.Parameters = schema
			chatReq.Tools = append(chatReq.Tools, ct)
			toolNameMap[rt.Name] = ToolNameMapping{Name: rt.Name, Namespace: ""}
		default:
			// custom / web_search / local_shell / computer_use 等：透传为 function（name 原样）
			schema := pickSchema(rt.InputSchema, rt.Parameters)
			ct := ChatTool{Type: "function"}
			ct.Function.Name = rt.Name
			ct.Function.Description = rt.Description
			ct.Function.Parameters = schema
			chatReq.Tools = append(chatReq.Tools, ct)
			toolNameMap[rt.Name] = ToolNameMapping{Name: rt.Name, Namespace: ""}
		}
	}

	// 转换 input items → messages
	for _, item := range req.Input {
		switch item.Type {
		case "message", "":
			// 普通消息
			role := item.Role
			if role == "" {
				role = "user"
			}
			// 对齐 codexpp：developer → system
			if role == "developer" {
				role = "system"
			}
			var content string
			if len(item.Content) > 0 {
				// 解析 content（可能是 string 或 array）
				var s string
				if err := json.Unmarshal(item.Content, &s); err == nil {
					content = s
				} else {
					// 可能是 array of parts
					var parts []map[string]any
					if err := json.Unmarshal(item.Content, &parts); err == nil {
						for _, p := range parts {
							if text, ok := p["text"].(string); ok {
								content += text
							}
						}
					}
				}
			}
			chatReq.Messages = append(chatReq.Messages, ChatMessage{
				Role:    role,
				Content: content,
			})

		case "function_call":
			// 工具调用 → assistant message with tool_calls
			// 历史 function_call 回传时 name 需用扁平名（namespace__name）匹配上游工具定义
			toolName := flattenNamespaceToolName(item.Namespace, item.Name)
			tc := ToolCall{
				ID:   item.CallID,
				Type: "function",
			}
			tc.Function.Name = toolName
			if len(item.Arguments) > 0 {
				tc.Function.Arguments = string(item.Arguments)
			}
			chatReq.Messages = append(chatReq.Messages, ChatMessage{
				Role:      "assistant",
				ToolCalls: []ToolCall{tc},
			})

		case "function_call_output":
			// 工具结果 → tool message
			// item.Output 是 json.RawMessage，可能是 JSON 字符串 "\"4\""，需要解码
			outputRaw := string(item.Output)
			var outputStr string
			if err := json.Unmarshal(item.Output, &outputStr); err == nil {
				outputRaw = outputStr // 解码后的纯字符串
			}
			chatReq.Messages = append(chatReq.Messages, ChatMessage{
				Role:       "tool",
				Content:    outputRaw,
				ToolCallID: item.CallID,
			})
		}
	}

	return chatReq, toolNameMap
}

// pickSchema 选取工具的 input schema（优先 input_schema，后 parameters，默认空对象）
func pickSchema(inputSchema, parameters any) any {
	if inputSchema != nil {
		return inputSchema
	}
	if parameters != nil {
		return parameters
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// chatToAnthropic 转换 Chat Completions → Anthropic Messages
func (h *Handler) chatToAnthropic(req ChatCompletionsRequest) map[string]any {
	result := map[string]any{
		"model":      req.Model,
		"max_tokens": 4096,
	}

	// 提取 system message
	var systemText string
	var messages []map[string]any

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			systemText += msg.Content + "\n"
		case "user":
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			content := []map[string]any{}
			if msg.Content != "" {
				content = append(content, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				// 对齐 codexpp：arguments 可能是 JSON 字符串或对象；
				// 若为字符串需二次解析为 object（百炼要求 tool_use.input 必须是对象）
				var input any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err == nil {
					if s, ok := input.(string); ok {
						// 二次解析：JSON 字符串里再包了一层 JSON
						var input2 any
						if err := json.Unmarshal([]byte(s), &input2); err == nil {
							input = input2
						} else {
							input = map[string]any{}
						}
					}
				} else {
					input = map[string]any{}
				}
				if input == nil {
					input = map[string]any{}
				}
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": content,
			})
		case "tool":
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolCallID,
						"content":     msg.Content,
					},
				},
			})
		}
	}

	if systemText != "" {
		result["system"] = systemText
	}
	result["messages"] = messages

	// 转换 tools
	if len(req.Tools) > 0 {
		tools := []map[string]any{}
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Function.Name,
				"description":  t.Function.Description,
				"input_schema": t.Function.Parameters,
			})
		}
		result["tools"] = tools
	}

	if req.Stream {
		result["stream"] = true
	}

	return result
}

// nonStreamChatToResponses 非流式 Chat Completions → Responses
func (h *Handler) nonStreamChatToResponses(w http.ResponseWriter, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)

	var chatResp map[string]any
	if err := json.Unmarshal(body, &chatResp); err != nil {
		http.Error(w, "invalid upstream response", http.StatusBadGateway)
		return
	}

	// 提取文本
	var textContent string
	if choices, ok := chatResp["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if msg, ok := c["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok {
					textContent = content
				}
			}
		}
	}

	// 提取 usage
	var inputTokens, outputTokens int64
	if usage, ok := chatResp["usage"].(map[string]any); ok {
		if t, ok := usage["prompt_tokens"].(float64); ok {
			inputTokens = int64(t)
		}
		if t, ok := usage["completion_tokens"].(float64); ok {
			outputTokens = int64(t)
		}
	}

	model, _ := chatResp["model"].(string)
	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())

	response := map[string]any{
		"id":         responseID,
		"object":     "response",
		"status":     "completed",
		"model":      model,
		"created_at": time.Now().Unix(),
		"output": []map[string]any{
			{
				"id":   "msg_" + responseID,
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": textContent, "annotations": []any{}},
				},
				"status": "completed",
			},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// nonStreamAnthropicToResponses 非流式 Anthropic → Responses
func (h *Handler) nonStreamAnthropicToResponses(w http.ResponseWriter, resp *http.Response, req *ChatCompletionsRequest) {
	body, _ := io.ReadAll(resp.Body)

	var anthResp map[string]any
	if err := json.Unmarshal(body, &anthResp); err != nil {
		http.Error(w, "invalid upstream response", http.StatusBadGateway)
		return
	}

	// 提取文本内容
	var textContent string
	if content, ok := anthResp["content"].([]any); ok {
		for _, block := range content {
			if b, ok := block.(map[string]any); ok {
				if b["type"] == "text" {
					if t, ok := b["text"].(string); ok {
						textContent += t
					}
				}
			}
		}
	}

	// 提取 usage
	var inputTokens, outputTokens int64
	if usage, ok := anthResp["usage"].(map[string]any); ok {
		if t, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int64(t)
		}
		if t, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int64(t)
		}
	}

	model, _ := anthResp["model"].(string)

	response := map[string]any{
		"id":         fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		"object":     "response",
		"status":     "completed",
		"model":      model,
		"created_at": time.Now().Unix(),
		"output": []map[string]any{
			{
				"id":   "msg_" + fmt.Sprintf("%d", time.Now().UnixNano()),
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": textContent, "annotations": []any{}},
				},
				"status": "completed",
			},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleModels 处理 /v1/models 请求
func (h *Handler) HandleModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{
		{"id": "gpt-5.4", "object": "model", "created": time.Now().Unix(), "owned_by": "codex-relayx"},
		{"id": "gpt-5", "object": "model", "created": time.Now().Unix(), "owned_by": "codex-relayx"},
		{"id": "claude-sonnet-4-5", "object": "model", "created": time.Now().Unix(), "owned_by": "codex-relayx"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   models,
	})
}

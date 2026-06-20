package proxy

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// sseEvent 构造一个 SSE 事件字符串
func sseEvent(eventType string, data any) string {
	jsonBytes, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonBytes))
}

// genID 生成 codexpp 风格的 id：prefix + n 位 hex
// 对齐 Rust: uuid::Uuid::new_v4().to_string().replace("-","").get(..n)
func genID(prefix string, n int) string {
	b := make([]byte, n/2+1)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%0*x", prefix, n, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)[:n]
}

// ToolNameMapping 工具名映射：扁平名（发给上游）→ (原始 name, namespace)
// 对齐 codexpp CodexToolContext.function_tools 的双向映射。
// codex 发来的 namespace 工具会被扁平化为 "{namespace}__{name}" 发给上游；
// 上游返回 tool_use 时用扁平名，需查回 (name, namespace) 还原给 codex。
type ToolNameMapping struct {
	Name      string
	Namespace string
}

// flattenNamespaceToolName 对齐 codexpp flatten_namespace_tool_name
func flattenNamespaceToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "__" + name
}

// ResponsesStreamState 有状态的流式转换器（Chat Completions SSE / Anthropic SSE → Responses SSE）
// 严格按照 codexplusplus 的 ChatToResponsesState 实现：
//   - reasoning 总是第一个 output item（output_index = 0）
//   - text 的 output_index = if reasoning_started { 1 } else { 0 }
//   - tool_use 固定用 chat_index = 0
//   - content_block_start 时立即发出 added 事件
//   - content_block_stop 时立即 finalize
//   - message_stop 时 finalize 所有项 + response.completed
type ResponsesStreamState struct {
	logger *zap.Logger

	// 响应元数据
	responseID   string
	model        string
	createdAt    int64
	inputTokens  int64
	outputTokens int64
	cachedTokens int64

	// 状态机
	responseStarted  bool
	completed        bool
	nextOutputIndex  uint32

	// text item（message）
	textStarted bool
	textDone    bool
	textItemID  string
	textContent strings.Builder

	// reasoning item
	reasoningStarted bool
	reasoningDone    bool
	reasoningItemID  string
	reasoningText    strings.Builder

	// tool calls（按 chat_index 分组；Anthropic 固定用 0）
	tools map[int]*toolCallState

	// Anthropic SSE: track current content block type
	currentBlockType string

	// 工具名映射：扁平名 → (原始 name, namespace)
	// 对齐 codexpp CodexToolContext.function_tools
	toolNameMap map[string]ToolNameMapping

	// finish state
	finished    bool
	finishReason string
}

// toolCallState 工具调用状态
type toolCallState struct {
	added       bool
	done        bool
	callID      string
	name        string
	namespace   string
	arguments   strings.Builder
	outputIndex uint32
	itemID      string
}

// NewResponsesStreamState 构造新的转换器
// toolNameMap 为请求侧构建的工具名映射（扁平名 → name/namespace），用于响应侧还原
func NewResponsesStreamState(logger *zap.Logger, toolNameMap map[string]ToolNameMapping) *ResponsesStreamState {
	now := time.Now().Unix()
	return &ResponsesStreamState{
		logger:          logger,
		responseID:      "resp_" + genID("", 24),
		model:           "unknown",
		createdAt:       now,
		nextOutputIndex: 0,
		// 对齐 codexpp：结构体初始化时即生成 reasoning/text item id
		// Anthropic 路径下 handle_anthropic_event 直接使用这两个 id（不经过 push_*_delta）
		reasoningItemID: "rs_" + genID("", 24),
		textItemID:      "msg_" + genID("", 24),
		tools:           make(map[int]*toolCallState),
		toolNameMap:     toolNameMap,
	}
}

// resolveToolName 对齐 codexpp openai_name_for_function_tool
// 上游返回的扁平名 → (原始 name, namespace)；未命中则原样返回、namespace 为空
func (s *ResponsesStreamState) resolveToolName(upstreamName string) (string, string) {
	if s.toolNameMap != nil {
		if m, ok := s.toolNameMap[upstreamName]; ok {
			name := m.Name
			if name == "" {
				name = upstreamName
			}
			return name, m.Namespace
		}
	}
	return upstreamName, ""
}

func (s *ResponsesStreamState) nextIndex() uint32 {
	idx := s.nextOutputIndex
	s.nextOutputIndex++
	return idx
}

// ensureResponseStarted 发出 response.created（仅一次）
func (s *ResponsesStreamState) ensureResponseStarted() string {
	if s.responseStarted {
		return ""
	}
	s.responseStarted = true
	return sseEvent("response.created", map[string]any{
		"type":     "response.created",
		"response": s.baseResponse("in_progress"),
	})
}

// ============== Reasoning ==============

// startReasoning 发出 output_item.added + reasoning_summary_part.added（仅一次）
func (s *ResponsesStreamState) startReasoning() string {
	if s.reasoningStarted {
		return ""
	}
	s.reasoningStarted = true
	idx := s.nextIndex()

	var events strings.Builder
	events.WriteString(s.ensureResponseStarted())

	events.WriteString(sseEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": idx,
		"item": map[string]any{
			"id":      s.reasoningItemID,
			"type":    "reasoning",
			"status":  "in_progress",
			"summary": []any{},
		},
	}))

	events.WriteString(sseEvent("response.reasoning_summary_part.added", map[string]any{
		"type":          "response.reasoning_summary_part.added",
		"item_id":       s.reasoningItemID,
		"output_index":  idx,
		"summary_index": 0,
		"part": map[string]any{
			"type": "summary_text",
			"text": "",
		},
	}))

	return events.String()
}

// pushReasoningDelta 发出 reasoning_summary_text.delta
// reasoning 总是第一个 output item，output_index 固定为 0
func (s *ResponsesStreamState) pushReasoningDelta(delta string) string {
	if delta == "" {
		return ""
	}
	s.reasoningText.WriteString(delta)

	var events strings.Builder
	events.WriteString(s.startReasoning())

	events.WriteString(sseEvent("response.reasoning_summary_text.delta", map[string]any{
		"type":          "response.reasoning_summary_text.delta",
		"item_id":       s.reasoningItemID,
		"output_index":  0, // reasoning 总是第一个 output item
		"summary_index": 0,
		"delta":         delta,
	}))

	return events.String()
}

// finalizeReasoning 发出 reasoning_summary_part.done + output_item.done
func (s *ResponsesStreamState) finalizeReasoning() string {
	if !s.reasoningStarted || s.reasoningDone {
		return ""
	}
	s.reasoningDone = true

	var events strings.Builder
	events.WriteString(sseEvent("response.reasoning_summary_part.done", map[string]any{
		"type":          "response.reasoning_summary_part.done",
		"item_id":       s.reasoningItemID,
		"output_index":  0,
		"summary_index": 0,
		"part": map[string]any{
			"type": "summary_text",
			"text": "",
		},
	}))

	events.WriteString(sseEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]any{
			"id":      s.reasoningItemID,
			"type":    "reasoning",
			"status":  "completed",
			"summary": []any{},
		},
	}))

	return events.String()
}

// ============== Text ==============

// startText 发出 output_item.added + content_part.added（仅一次）
func (s *ResponsesStreamState) startText() string {
	if s.textStarted {
		return ""
	}
	s.textStarted = true
	idx := s.nextIndex()

	var events strings.Builder
	events.WriteString(s.ensureResponseStarted())

	events.WriteString(sseEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": idx,
		"item": map[string]any{
			"id":      s.textItemID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	}))

	events.WriteString(sseEvent("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       s.textItemID,
		"output_index":  idx,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	}))

	return events.String()
}

// pushTextDelta 发出 output_text.delta
// output_index = if reasoning_started { 1 } else { 0 }
func (s *ResponsesStreamState) pushTextDelta(delta string) string {
	if delta == "" {
		return ""
	}
	s.textContent.WriteString(delta)

	var events strings.Builder
	events.WriteString(s.startText())

	var idx uint32
	if s.reasoningStarted {
		idx = 1
	} else {
		idx = 0
	}

	events.WriteString(sseEvent("response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       s.textItemID,
		"output_index":  idx,
		"content_index": 0,
		"delta":         delta,
	}))

	return events.String()
}

// finalizeText 发出 content_part.done + output_item.done
func (s *ResponsesStreamState) finalizeText() string {
	if !s.textStarted || s.textDone {
		return ""
	}
	s.textDone = true

	var idx uint32
	if s.reasoningStarted {
		idx = 1
	} else {
		idx = 0
	}

	var events strings.Builder
	events.WriteString(sseEvent("response.content_part.done", map[string]any{
		"type":          "response.content_part.done",
		"item_id":       s.textItemID,
		"output_index":  idx,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        s.textContent.String(),
			"annotations": []any{},
		},
	}))

	events.WriteString(sseEvent("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": idx,
		"item": map[string]any{
			"id":      s.textItemID,
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        s.textContent.String(),
					"annotations": []any{},
				},
			},
		},
	}))

	return events.String()
}

// ============== Tool Calls ==============

// startToolUseAnthropic Anthropic content_block_start 的 tool_use 分支
// 固定用 chat_index = 0，发出 output_item.added
// 对齐 codexpp：忽略 Anthropic block 的 id，自己生成 call_anth_<uuid8> / fc_<uuid24>；
// upstreamName 为扁平名，经 resolveToolName 还原为 (name, namespace)
func (s *ResponsesStreamState) startToolUseAnthropic(upstreamName string) string {
	var events strings.Builder

	// 先结束 reasoning 和 text（如果有）
	if s.reasoningStarted {
		events.WriteString(s.finalizeReasoning())
	}
	if s.textStarted {
		events.WriteString(s.finalizeText())
	}

	// 确保 response 已创建
	events.WriteString(s.ensureResponseStarted())

	idx := s.nextIndex()
	callID := "call_anth_" + genID("", 8)
	itemID := "fc_" + genID("", 24)
	displayName, namespace := s.resolveToolName(upstreamName)
	if displayName == "" {
		displayName = "unknown"
	}

	tc := &toolCallState{
		added:       true,
		callID:      callID,
		name:        displayName,
		namespace:   namespace,
		outputIndex: idx,
		itemID:      itemID,
	}
	s.tools[0] = tc // Anthropic 固定 chat_index = 0

	addedItem := map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    "in_progress",
		"call_id":   callID,
		"name":      displayName,
		"arguments": "",
	}
	if namespace != "" {
		addedItem["namespace"] = namespace
	}

	events.WriteString(sseEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": idx,
		"item":         addedItem,
	}))

	return events.String()
}

// pushToolUseArgsAnthropic Anthropic content_block_delta 的 tool_use 分支
// 累积 partial_json，发出 function_call_arguments.delta
func (s *ResponsesStreamState) pushToolUseArgsAnthropic(partial string) string {
	if partial == "" {
		return ""
	}
	tc, ok := s.tools[0]
	if !ok {
		return ""
	}
	tc.arguments.WriteString(partial)

	return sseEvent("response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      tc.itemID,
		"output_index": tc.outputIndex,
		"delta":        partial,
	})
}

// pushToolCallChat Chat Completions 的 tool_calls delta 处理
// 用 OpenAI 的 index 字段作为 key
func (s *ResponsesStreamState) pushToolCallChat(chatIndex int, id, name, args string) string {
	var events strings.Builder

	tc, exists := s.tools[chatIndex]
	if !exists {
		tc = &toolCallState{}
		s.tools[chatIndex] = tc
	}

	if id != "" {
		tc.callID = id
	}
	if name != "" {
		tc.name = name
	}
	if args != "" {
		tc.arguments.WriteString(args)
	}

	// 先确保 response 已创建
	if !s.responseStarted {
		events.WriteString(s.ensureResponseStarted())
	}

	// 第一次遇到时触发 added
	if !tc.added && (tc.callID != "" || tc.name != "") {
		tc.added = true
		idx := s.nextIndex()
		if tc.callID == "" {
			tc.callID = fmt.Sprintf("call_%d", chatIndex)
		}
		// 对齐 codexpp openai_name_for_function_tool：扁平名 → (name, namespace)
		displayName, namespace := s.resolveToolName(tc.name)
		if displayName == "" {
			displayName = "unknown_tool"
		}
		tc.name = displayName
		tc.namespace = namespace
		tc.outputIndex = idx
		tc.itemID = fmt.Sprintf("fc_%s", strings.TrimPrefix(tc.callID, "call_"))

		addedItem := map[string]any{
			"id":        tc.itemID,
			"type":      "function_call",
			"status":    "in_progress",
			"call_id":   tc.callID,
			"name":      tc.name,
			"arguments": "",
		}
		if namespace != "" {
			addedItem["namespace"] = namespace
		}

		events.WriteString(sseEvent("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": idx,
			"item":         addedItem,
		}))

		// 同时发送累积的 arguments delta
		if tc.arguments.Len() > 0 {
			events.WriteString(sseEvent("response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"item_id":      tc.itemID,
				"output_index": idx,
				"delta":        tc.arguments.String(),
			}))
		}
	} else if tc.added && args != "" {
		// 后续 chunk：发送 arguments delta
		events.WriteString(sseEvent("response.function_call_arguments.delta", map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      tc.itemID,
			"output_index": tc.outputIndex,
			"delta":        args,
		}))
	}

	return events.String()
}

// finalizeTools 发出所有未完成 tool 的 done 事件
func (s *ResponsesStreamState) finalizeTools() string {
	var events strings.Builder

	// 收集 keys（避免遍历时修改）
	keys := make([]int, 0, len(s.tools))
	for k := range s.tools {
		keys = append(keys, k)
	}

	for _, key := range keys {
		tc := s.tools[key]
		if tc.done {
			continue
		}

		// 如果工具未添加，强制添加
		if !tc.added {
			events.WriteString(s.ensureResponseStarted())
			idx := s.nextIndex()
			tc.added = true
			if tc.callID == "" {
				tc.callID = fmt.Sprintf("call_%d", key)
			}
			// 对齐 codexpp：扁平名 → (name, namespace)
			displayName, namespace := s.resolveToolName(tc.name)
			if displayName == "" {
				displayName = "unknown_tool"
			}
			tc.name = displayName
			tc.namespace = namespace
			tc.outputIndex = idx
			tc.itemID = fmt.Sprintf("fc_%s", strings.TrimPrefix(tc.callID, "call_"))

			addedItem := map[string]any{
				"id":        tc.itemID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   tc.callID,
				"name":      tc.name,
				"arguments": "",
			}
			if namespace != "" {
				addedItem["namespace"] = namespace
			}

			events.WriteString(sseEvent("response.output_item.added", map[string]any{
				"type":         "response.output_item.added",
				"output_index": idx,
				"item":         addedItem,
			}))
		}

		tc.done = true

		events.WriteString(sseEvent("response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      tc.itemID,
			"output_index": tc.outputIndex,
			"arguments":    tc.arguments.String(),
		}))

		doneItem := map[string]any{
			"id":        tc.itemID,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   tc.callID,
			"name":      tc.name,
			"arguments": tc.arguments.String(),
		}
		if tc.namespace != "" {
			doneItem["namespace"] = tc.namespace
		}

		events.WriteString(sseEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": tc.outputIndex,
			"item":         doneItem,
		}))
	}

	return events.String()
}

// finalize 发出所有 done 事件 + response.completed（顺序：reasoning → text → tools）
func (s *ResponsesStreamState) finalize() string {
	var events strings.Builder

	if s.reasoningStarted && !s.reasoningDone {
		events.WriteString(s.finalizeReasoning())
	}
	if s.textStarted && !s.textDone {
		events.WriteString(s.finalizeText())
	}
	events.WriteString(s.finalizeTools())

	events.WriteString(sseEvent("response.completed", map[string]any{
		"type":     "response.completed",
		"response": s.baseResponse("completed"),
	}))

	s.completed = true
	s.finished = true
	return events.String()
}

// baseResponse 构建完整的 response 对象（对齐 codexpp base_response）
func (s *ResponsesStreamState) baseResponse(status string) map[string]any {
	output := []any{}

	// 1. reasoning item
	if s.reasoningStarted || s.reasoningText.Len() > 0 {
		summary := []any{}
		if s.reasoningText.Len() > 0 {
			summary = append(summary, map[string]any{
				"type": "summary_text",
				"text": s.reasoningText.String(),
			})
		}
		output = append(output, map[string]any{
			"id":      s.reasoningItemID,
			"type":    "reasoning",
			"status":  ifThen(s.reasoningDone, "completed", "in_progress"),
			"summary": summary,
		})
	}

	// 2. text item
	if s.textStarted || s.textContent.Len() > 0 {
		output = append(output, map[string]any{
			"id":     s.textItemID,
			"type":   "message",
			"status": ifThen(s.textDone, "completed", "in_progress"),
			"role":   "assistant",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        s.textContent.String(),
					"annotations": []any{},
				},
			},
		})
	}

	// 3. tool calls
	for _, tc := range s.tools {
		if tc.added {
			item := map[string]any{
				"id":        tc.itemID,
				"type":      "function_call",
				"status":    ifThen(tc.done, "completed", "in_progress"),
				"call_id":   tc.callID,
				"name":      tc.name,
				"arguments": tc.arguments.String(),
			}
			if tc.namespace != "" {
				item["namespace"] = tc.namespace
			}
			output = append(output, item)
		}
	}

	return map[string]any{
		"id":         s.responseID,
		"object":     "response",
		"status":     status,
		"model":      s.model,
		"created_at": s.createdAt,
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  s.inputTokens,
			"output_tokens": s.outputTokens,
			"total_tokens":  s.inputTokens + s.outputTokens,
			"input_tokens_details": map[string]any{
				"cached_tokens": s.cachedTokens,
				"text_tokens":   0,
			},
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 0,
			},
		},
	}
}

func ifThen(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// ============== StreamAnthropicToResponses ==============

// StreamAnthropicToResponses 将 Anthropic SSE 转换为 Responses SSE
// 完全对齐 codexplusplus 的 handle_anthropic_event
// toolNameMap 为请求侧构建的工具名映射（扁平名 → name/namespace）
func (h *Handler) StreamAnthropicToResponses(w http.ResponseWriter, resp *http.Response, toolNameMap map[string]ToolNameMapping) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	state := NewResponsesStreamState(h.logger, toolNameMap)
	scanner := bufio.NewScanner(resp.Body)
	// 百炼 thinking 块可能很大，增大缓冲区
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)

	var currentEvent string

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			continue
		}

		// event: 头（支持 "event:value" 和 "event: value"）
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(line[6:])
			continue
		}

		// data: 行
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(line[5:])
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}

		var evt map[string]any
		if err := json.Unmarshal([]byte(dataStr), &evt); err != nil {
			continue
		}

		events := state.handleAnthropicEvent(evt, currentEvent)
		if events != "" {
			fmt.Fprint(w, events)
			flusher.Flush()
		}

		// message_stop 后已 finalize，无需继续处理
		if state.finished {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		h.logger.Error("anthropic stream scan error", zap.Error(err))
	}

	// 如果上游未发 message_stop（异常断流），兜底 finalize
	if !state.finished {
		fmt.Fprint(w, state.finalize())
		flusher.Flush()
	}
}

// handleAnthropicEvent 处理单个 Anthropic SSE 事件，返回要发出的 Responses SSE 事件
// 对齐 codexplusplus handle_anthropic_event
func (s *ResponsesStreamState) handleAnthropicEvent(data map[string]any, eventType string) string {
	dataType, _ := data["type"].(string)
	if dataType == "" {
		dataType = eventType
	}

	var events strings.Builder

	switch dataType {
	case "message_start":
		// 提取 model / id / usage
		if msg, ok := data["message"].(map[string]any); ok {
			if id, ok := msg["id"].(string); ok && id != "" {
				s.responseID = "resp_" + strings.TrimPrefix(id, "msg_")
			}
			if m, ok := msg["model"].(string); ok && m != "" {
				s.model = m
			}
			if usage, ok := msg["usage"].(map[string]any); ok {
				if t, ok := usage["input_tokens"].(float64); ok {
					s.inputTokens = int64(t)
				}
				if t, ok := usage["cache_read_input_tokens"].(float64); ok {
					s.cachedTokens = int64(t)
				}
			}
		}
		// 发送 response.created
		events.WriteString(sseEvent("response.created", map[string]any{
			"type":     "response.created",
			"response": s.baseResponse("in_progress"),
		}))
		s.responseStarted = true

	case "content_block_start":
		if block, ok := data["content_block"].(map[string]any); ok {
			blockType, _ := block["type"].(string)
			if blockType == "" {
				blockType = "text"
			}
			s.currentBlockType = blockType

			switch blockType {
			case "thinking":
				events.WriteString(s.startReasoning())

			case "text":
				// 先结束 reasoning（如果有）
				if s.reasoningStarted {
					events.WriteString(s.finalizeReasoning())
				}
				// 开始 text item
				events.WriteString(s.startText())

			case "tool_use":
				// 初始化 tool call（startToolUseAnthropic 内部会先结束 reasoning/text）
				// 对齐 codexpp：忽略 Anthropic block 的 id，用上游返回的 name（扁平名）
				name, _ := block["name"].(string)
				if name == "" {
					name = "unknown"
				}
				events.WriteString(s.startToolUseAnthropic(name))
			}
		}

	case "content_block_delta":
		delta, ok := data["delta"].(map[string]any)
		if !ok {
			break
		}
		blockType := s.currentBlockType
		if blockType == "" {
			blockType = "text"
		}

		switch blockType {
		case "thinking":
			if t, ok := delta["thinking"].(string); ok && t != "" {
				// reasoning 总是第一个 output item，index = 0
				events.WriteString(sseEvent("response.reasoning_summary_text.delta", map[string]any{
					"type":          "response.reasoning_summary_text.delta",
					"item_id":       s.reasoningItemID,
					"output_index":  0,
					"summary_index": 0,
					"delta":         t,
				}))
				s.reasoningText.WriteString(t)
			}

		case "text":
			if t, ok := delta["text"].(string); ok && t != "" {
				var idx uint32
				if s.reasoningStarted {
					idx = 1
				} else {
					idx = 0
				}
				events.WriteString(sseEvent("response.output_text.delta", map[string]any{
					"type":          "response.output_text.delta",
					"item_id":       s.textItemID,
					"output_index":  idx,
					"content_index": 0,
					"delta":         t,
				}))
				s.textContent.WriteString(t)
			}

		case "tool_use":
			// input_json_delta: accumulate tool arguments
			if partial, ok := delta["partial_json"].(string); ok && partial != "" {
				events.WriteString(s.pushToolUseArgsAnthropic(partial))
			}
		}

	case "content_block_stop":
		blockType := s.currentBlockType
		s.currentBlockType = ""
		switch blockType {
		case "thinking":
			events.WriteString(s.finalizeReasoning())
		case "text":
			events.WriteString(s.finalizeText())
		case "tool_use":
			// 由 message_stop 统一 finalize
		}

	case "message_delta":
		// 提取 finish reason
		if delta, ok := data["delta"].(map[string]any); ok {
			if stop, ok := delta["stop_reason"].(string); ok {
				s.finishReason = stop
			}
		}
		// 提取 usage（output_tokens）
		if usage, ok := data["usage"].(map[string]any); ok {
			if t, ok := usage["output_tokens"].(float64); ok {
				s.outputTokens = int64(t)
			}
		}

	case "message_stop":
		// 确保所有项都已结束
		if s.reasoningStarted && !s.reasoningDone {
			events.WriteString(s.finalizeReasoning())
		}
		if s.textStarted && !s.textDone {
			events.WriteString(s.finalizeText())
		}
		events.WriteString(s.finalizeTools())
		// 发送 response.completed
		events.WriteString(sseEvent("response.completed", map[string]any{
			"type":     "response.completed",
			"response": s.baseResponse("completed"),
		}))
		s.completed = true
		s.finished = true
	}

	return events.String()
}

// ============== StreamChatToResponses ==============

// StreamChatToResponses 将 OpenAI Chat Completions SSE 转换为 Responses SSE
// 对齐 codexplusplus handle_chat_chunk
// toolNameMap 为请求侧构建的工具名映射（扁平名 → name/namespace）
func (h *Handler) StreamChatToResponses(w http.ResponseWriter, resp *http.Response, toolNameMap map[string]ToolNameMapping) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	state := NewResponsesStreamState(h.logger, toolNameMap)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")

		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(line[5:])

		if dataStr == "[DONE]" {
			// 发送完成事件
			events := state.finalize()
			if events != "" {
				fmt.Fprint(w, events)
				flusher.Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			state.finished = true
			break
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		events := state.handleChatChunk(chunk)
		if events != "" {
			fmt.Fprint(w, events)
			flusher.Flush()
		}

		if state.finished {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		h.logger.Error("chat stream scan error", zap.Error(err))
	}

	// 兜底 finalize
	if !state.finished {
		fmt.Fprint(w, state.finalize())
		flusher.Flush()
	}
}

// handleChatChunk 处理 Chat Completions SSE chunk
// 对齐 codexplusplus handle_chat_chunk
func (s *ResponsesStreamState) handleChatChunk(chunk map[string]any) string {
	var events strings.Builder

	// 提取基础信息
	if id, ok := chunk["id"].(string); ok {
		s.responseID = "resp_" + strings.TrimPrefix(id, "chatcmpl-")
	}
	if m, ok := chunk["model"].(string); ok && m != "" {
		s.model = m
	}
	if created, ok := chunk["created"].(float64); ok {
		s.createdAt = int64(created)
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return events.String()
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return events.String()
	}

	if delta, ok := choice["delta"].(map[string]any); ok {
		// 处理 reasoning_content（百炼等模型的思考过程）
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			events.WriteString(s.pushReasoningDelta(reasoning))
		}

		// 处理 content（正常回复）
		if content, ok := delta["content"].(string); ok && content != "" {
			// 如果有未结束的 reasoning，先结束它
			if s.reasoningStarted && !s.reasoningDone {
				events.WriteString(s.finalizeReasoning())
			}
			events.WriteString(s.pushTextDelta(content))
		}

		// 处理 tool_calls
		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			// 先结束 reasoning
			if s.reasoningStarted && !s.reasoningDone {
				events.WriteString(s.finalizeReasoning())
			}
			for _, tc := range toolCalls {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				chatIndex := 0
				if idx, ok := tcMap["index"].(float64); ok {
					chatIndex = int(idx)
				}
				id := stringOf(tcMap["id"])
				var name, args string
				if fn, ok := tcMap["function"].(map[string]any); ok {
					name = stringOf(fn["name"])
					args = stringOf(fn["arguments"])
				}
				events.WriteString(s.pushToolCallChat(chatIndex, id, name, args))
			}
		}
	}

	// 处理 finish_reason
	if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
		switch finishReason {
		case "stop", "end_turn", "tool_calls":
			s.finishReason = finishReason
			events.WriteString(s.finalize())
			s.finished = true
		}
	}

	return events.String()
}

// stringOf 从 any 类型安全地转为 string
func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

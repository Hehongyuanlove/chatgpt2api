package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gibson042/canonicaljson-go"
	"github.com/google/uuid"
)

var toolCallPromptTmpl string

type StreamState struct {
	mu             sync.Mutex
	ConvID         string
	ParentMsgID    string
	Content        strings.Builder
	ModelSlug      string
	PlanType       string
	Finished       bool
	LastEventID    string
	MessageID      string
	ChunkIndex     int
	RoleSent       bool
	LastContent    string
	ToolCalls      []ToolCall
}

func NewStreamState() *StreamState {
	return &StreamState{
		ParentMsgID: uuid.New().String(),
		MessageID:   uuid.New().String(),
	}
}

func (st *StreamState) Reset(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.Content.Reset()
	st.Content.WriteString(msg)
	st.Finished = false
	st.ChunkIndex = 0
	st.RoleSent = false
}

func buildMessageText(messages []ChatMessage, convID string, tools []Tool, prevToolHash string) string {
	isFirstSession := convID == ""
	currentToolHash := hashTools(tools)
	toolsChanged := prevToolHash != currentToolHash

	var parts []string

	// history messages (first session only, exclude last)
	if isFirstSession && len(messages) > 1 {
		for i := 0; i < len(messages)-1; i++ {
			m := messages[i]
			switch m.Role {
			case "user":
				parts = append(parts, m.Content)
			case "assistant":
				if m.Content != "" {
					parts = append(parts, m.Content)
				}
				for _, tc := range m.ToolCalls {
					parts = append(parts, "Tool call: "+tc.Function.Name+"("+tc.Function.Arguments+")")
				}
			case "tool":
				parts = append(parts, "Tool result ("+m.ToolCallID+"): "+m.Content)
			case "system":
				parts = append(parts, m.Content)
			}
		}
	}

	// tool descriptions (first session or tools changed)
	if (isFirstSession || toolsChanged) && len(tools) > 0 {
		if desc := buildToolDescriptions(tools); desc != "" {
			parts = append(parts, desc)
		}
	}

	// last message content
	if len(messages) > 0 {
		parts = append(parts, messages[len(messages)-1].Content)
	}

	return strings.Join(parts, "\n")
}

func buildToolDescriptions(tools []Tool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		b.WriteString("### ")
		b.WriteString(t.Function.Name)
		b.WriteString("\n")
		if t.Function.Description != "" {
			b.WriteString(t.Function.Description)
			b.WriteString("\n")
		}
		if len(t.Function.Parameters) > 0 {
			b.WriteString("Parameters: ")
			b.WriteString(string(t.Function.Parameters))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	toolDefs := strings.TrimSpace(b.String())

	if toolCallPromptTmpl != "" {
		return strings.ReplaceAll(toolCallPromptTmpl, "{tool_definitions}", toolDefs)
	}

	// Fallback default
	return "You have access to the following functions:\n\n" + toolDefs +
		"\nWhen you need to call a function, respond with a JSON object in exactly this format:\n" +
		`{"tool_calls":[{"id":"call_<unique_id>","type":"function","function":{"name":"<function_name>","arguments":"<json_args>"}}]}`
}



func convertModel(model string) string {
	switch model {
	case "gpt-4o", "gpt-4o-2024-08-06", "gpt-4o-2024-05-13":
		return "auto"
	case "gpt-4o-mini", "gpt-4o-mini-2024-07-18":
		return "auto"
	case "gpt-4-turbo", "gpt-4-turbo-2024-04-09":
		return "auto"
	case "gpt-4", "gpt-4-0613":
		return "auto"
	case "gpt-3.5-turbo", "gpt-3.5-turbo-0125":
		return "auto"
	case "o1", "o1-2024-12-17":
		return "o1"
	case "o3-mini", "o3-mini-2025-01-31":
		return "auto"
	case "o4-mini":
		return "auto"
	default:
		return "auto"
	}
}

func parseToolCallsFromContent(content string) []ToolCall {
	content = strings.TrimSpace(content)
	braceIdx := strings.Index(content, "{")
	if braceIdx < 0 {
		return nil
	}
	content = content[braceIdx:]

	depth := 0
	endIdx := -1
	for i, c := range content {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				endIdx = i + 1
				goto parse
			}
		}
	}
	return nil

parse:
	content = content[:endIdx]

	var parsed struct {
		ToolCalls []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil
	}
	if len(parsed.ToolCalls) == 0 {
		return nil
	}
	for i := range parsed.ToolCalls {
		if parsed.ToolCalls[i].Type == "" {
			parsed.ToolCalls[i].Type = "function"
		}
		parsed.ToolCalls[i].Index = i
	}
	return parsed.ToolCalls
}

func sseEventToChunk(event SSEEvent, st *StreamState) []ChatCompletionChunk {
	var chunks []ChatCompletionChunk

	if event.Error != nil {
		st.mu.Lock()
		st.Finished = true
		st.mu.Unlock()
		return chunks
	}

	if event.ConversationID != "" {
		st.mu.Lock()
		st.ConvID = event.ConversationID
		st.mu.Unlock()
	}

	if event.Type == "server_ste_metadata" && event.Metadata != nil {
		st.mu.Lock()
		if st.ModelSlug == "" {
			st.ModelSlug = event.Metadata.ModelSlug
		}
		st.PlanType = event.Metadata.PlanType
		st.mu.Unlock()
	}

	if event.Message != nil && event.Message.Author != nil {
		// Skip internal tool messages (bio memory updates, commentary, etc.)
		if event.Message.Recipient == "bio" || event.Message.Channel == "commentary" {
			return chunks
		}

		role := event.Message.Author.Role
		content := ""
		if event.Message.Content != nil && len(event.Message.Content.Parts) > 0 {
			content = event.Message.Content.Parts[0]
		}

		if role == "assistant" {
			toolCalls := extractToolCalls(event)
			if len(toolCalls) == 0 && content != "" {
				toolCalls = parseToolCallsFromContent(content)
				if len(toolCalls) > 0 {
					content = "" // Don't emit raw JSON as content
				}
			}
			if content != "" || len(toolCalls) > 0 {
				st.mu.Lock()
				delta := Delta{}
				if !st.RoleSent {
					delta.Role = "assistant"
					st.RoleSent = true
				}
				if content != "" {
					// Suppress partial tool_calls JSON leaking as text content
					// during streaming. parseToolCallsFromContent already failed
					// above, so this content is either incomplete JSON or
					// genuinely not tool_calls.
					trimmed := strings.TrimSpace(content)
					isPartialToolCall := strings.HasPrefix(trimmed, `{"tool_calls":`) && len(toolCalls) == 0

					if !isPartialToolCall {
						if len(content) > len(st.LastContent) {
							delta.Content = content[len(st.LastContent):]
						} else if content != st.LastContent {
							delta.Content = content
						}
						st.Content.WriteString(delta.Content)
					}
					st.LastContent = content
				}
			if len(toolCalls) > 0 {
				for i := range toolCalls {
					toolCalls[i].Index = i
				}
				delta.ToolCalls = toolCalls
				st.ToolCalls = toolCalls
			}
				if event.Message.ID != "" {
					st.MessageID = event.Message.ID
				}
				isFinished := event.Message.Status == "finished_successfully"
				if isFinished {
					st.Finished = true
				}

				var finishReason *string
				if isFinished {
					if len(toolCalls) > 0 {
						finishReason = strPtr("tool_calls")
					} else {
						finishReason = strPtr("stop")
					}
				}

				if delta.Content != "" || delta.ToolCalls != nil || finishReason != nil {
					idx := st.ChunkIndex
					st.ChunkIndex++
					st.mu.Unlock()
					chunk := ChatCompletionChunk{
						Object: "chat.completion.chunk",
						Choices: []ChunkChoice{{
							Index:        idx,
							Delta:        delta,
							FinishReason: finishReason,
						}},
					}
					if finishReason != nil && st.ConvID != "" {
						chunk.ConversationID = st.ConvID
					}
					chunks = append(chunks, chunk)
				} else {
					st.mu.Unlock()
				}
			}
		}

		if event.Message.Status == "finished_successfully" && role == "assistant" && content == "" && len(extractToolCalls(event)) == 0 && len(parseToolCallsFromContent(content)) == 0 {
			st.mu.Lock()
			st.Finished = true
			st.mu.Unlock()
		}
	}

	if event.InputMessage != nil && event.InputMessage.Content != nil {
		st.mu.Lock()
		if st.ConvID == "" {
			if id, _ := event.InputMessage.Metadata["conversation_id"].(string); id != "" {
				st.ConvID = id
			}
		}
		st.mu.Unlock()
	}

	return chunks
}

func buildNonStreamResponse(st *StreamState, requestID string) ChatCompletionResponse {
	model := st.ModelSlug
	if model == "" {
		model = "gpt-4o"
	}
	content := st.Content.String()
	msg := ChatMessage{Role: "assistant", Content: content}
	finishReason := "stop"
	if len(st.ToolCalls) > 0 {
		msg.ToolCalls = st.ToolCalls
		finishReason = "tool_calls"
	}
	return ChatCompletionResponse{
		ID:             "chatcmpl-" + requestID,
		Object:         "chat.completion",
		Created:        time.Now().Unix(),
		Model:          model,
		ConversationID: st.ConvID,
		Choices: []ResponseChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: strPtr(finishReason),
		}},
	}
}

func buildStreamHeader(requestID, model string) ChatCompletionChunk {
	return ChatCompletionChunk{
		ID:      "chatcmpl-" + requestID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChunkChoice{{
			Index: 0,
			Delta: Delta{Role: "assistant"},
		}},
	}
}

func buildStreamDone(requestID, model string, st *StreamState) ChatCompletionChunk {
	finishReason := "stop"
	if len(st.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	return ChatCompletionChunk{
		ID:      "chatcmpl-" + requestID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChunkChoice{{
			Index:        0,
			Delta:        Delta{},
			FinishReason: strPtr(finishReason),
		}},
	}
}

func extractToolCalls(event SSEEvent) []ToolCall {
	if event.Message == nil || event.Message.Metadata == nil {
		return nil
	}

	if tcRaw, ok := event.Message.Metadata["tool_calls"]; ok {
		data, err := json.Marshal(tcRaw)
		if err == nil {
			var calls []ToolCall
			if json.Unmarshal(data, &calls) == nil && len(calls) > 0 {
				for i := range calls {
					if calls[i].Type == "" {
						calls[i].Type = "function"
					}
					calls[i].Index = i
				}
				return calls
			}
		}
	}

	msgType, _ := event.Message.Metadata["message_type"].(string)
	if msgType == "tool_call" || msgType == "tool_use" || msgType == "plugin" {
		name := event.Message.Recipient
		if name == "" {
			if n, ok := event.Message.Metadata["tool_name"].(string); ok {
				name = n
			}
		}
		if name != "" {
			args := "{}"
			if event.Message.Content != nil && len(event.Message.Content.Parts) > 0 {
				args = event.Message.Content.Parts[0]
			}
			id := event.Message.ID
			if id == "" {
				id = "call_" + makeRequestID()
			}
			return []ToolCall{{
				ID:    id,
				Type:  "function",
				Index: 0,
				Function: ToolCallFunction{
					Name:      name,
					Arguments: args,
				},
			}}
		}
	}

	return nil
}

func strPtr(s string) *string { return &s }

func makeRequestID() string { return uuid.New().String()[:12] }

func formatSSE(v interface{}) string {
	data, _ := jsonMarshal(v)
	return fmt.Sprintf("data: %s\n\n", string(data))
}

var jsonMarshal = func(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func loadToolCallPrompt(promptDir string) {
	path := filepath.Join(promptDir, "tool-call.md")
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("WARN: tool-call.md not found at %s, using default prompt", path)
		toolCallPromptTmpl = ""
		return
	}
	toolCallPromptTmpl = string(data)
	log.Printf("Loaded tool call prompt from %s (%d bytes)", path, len(data))
}

func hashTools(tools []Tool) string {
	if len(tools) == 0 {
		return ""
	}
	data, err := canonicaljson.Marshal(tools)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

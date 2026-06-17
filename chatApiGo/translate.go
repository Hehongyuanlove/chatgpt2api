package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

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

func buildChatGPTPayload(messages []ChatMessage) (msgText string, systemHint string) {
	var parts []string
	for _, m := range messages {
		switch m.Role {
		case "system":
			systemHint = m.Content
		case "user":
			parts = append(parts, m.Content)
		case "assistant":
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n"), systemHint
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
		role := event.Message.Author.Role
		content := ""
		if event.Message.Content != nil && len(event.Message.Content.Parts) > 0 {
			content = event.Message.Content.Parts[0]
		}

		if role == "assistant" && content != "" {
			st.mu.Lock()
			// ChatGPT sends full accumulated text; emit only delta
			delta := Delta{}
			if !st.RoleSent {
				delta.Role = "assistant"
				st.RoleSent = true
			}
			if len(content) > len(st.LastContent) {
				delta.Content = content[len(st.LastContent):]
			} else if content != st.LastContent {
				delta.Content = content
			}
			st.LastContent = content
			st.Content.WriteString(delta.Content)
			if event.Message.ID != "" {
				st.MessageID = event.Message.ID
			}
			idx := st.ChunkIndex
			st.ChunkIndex++
			isFinished := event.Message.Status == "finished_successfully"
			if isFinished {
				st.Finished = true
			}
			st.mu.Unlock()

			var finishReason *string
			if isFinished {
				finishReason = strPtr("stop")
			}

			if delta.Content != "" || finishReason != nil {
				chunks = append(chunks, ChatCompletionChunk{
					Object: "chat.completion.chunk",
					Choices: []ChunkChoice{{
						Index:        idx,
						Delta:        delta,
						FinishReason: finishReason,
					}},
				})
			}
		}

		if event.Message.Status == "finished_successfully" && role == "assistant" && content == "" {
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
	return ChatCompletionResponse{
		ID:      "chatcmpl-" + requestID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ResponseChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: content},
			FinishReason: strPtr("stop"),
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

func buildStreamDone(requestID, model string) ChatCompletionChunk {
	return ChatCompletionChunk{
		ID:      "chatcmpl-" + requestID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChunkChoice{{
			Index:        0,
			Delta:        Delta{},
			FinishReason: strPtr("stop"),
		}},
	}
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

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

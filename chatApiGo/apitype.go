package main

import "encoding/json"

type ChatCompletionRequest struct {
	Model            string              `json:"model"`
	Messages         []ChatMessage       `json:"messages"`
	Stream           bool                `json:"stream"`
	MaxTokens        int                 `json:"max_tokens"`
	Temperature      float64             `json:"temperature"`
	TopP             float64             `json:"top_p"`
	User             string              `json:"user,omitempty"`
	ConversationID   string              `json:"conversation_id,omitempty"`
	ParentMessageID  string              `json:"parent_message_id,omitempty"`
	Tools            []Tool              `json:"tools,omitempty"`
	ToolChoice       interface{}         `json:"tool_choice,omitempty"`
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ChatCompletionResponse struct {
	ID             string            `json:"id"`
	Object         string            `json:"object"`
	Created        int64             `json:"created"`
	Model          string            `json:"model"`
	Choices        []ResponseChoice  `json:"choices"`
	Usage          *Usage            `json:"usage,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
}

type ResponseChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type ChatCompletionChunk struct {
	ID             string         `json:"id"`
	Object         string         `json:"object"`
	Created        int64          `json:"created"`
	Model          string         `json:"model"`
	Choices        []ChunkChoice  `json:"choices"`
	ConversationID string         `json:"conversation_id,omitempty"`
}

type ChunkChoice struct {
	Index        int         `json:"index"`
	Delta        Delta       `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type Delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

type ConversationListItem struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	CreatedAt float64 `json:"created_at,omitempty"`
	UpdatedAt float64 `json:"updated_at,omitempty"`
}

type ConversationListResponse struct {
	Object  string                 `json:"object"`
	Data    []ConversationListItem `json:"data"`
	HasMore bool                   `json:"has_more"`
	FirstID string                 `json:"first_id,omitempty"`
	LastID  string                 `json:"last_id,omitempty"`
}

type HistoryMessage struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	CreateTime float64 `json:"create_time,omitempty"`
}

type ConversationHistoryResponse struct {
	ID        string           `json:"id"`
	Title     string           `json:"title,omitempty"`
	CreatedAt float64          `json:"created_at,omitempty"`
	Messages  []HistoryMessage `json:"messages"`
}

type APIError struct {
	Error APIErrorDetail `json:"error"`
}

type APIErrorDetail struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

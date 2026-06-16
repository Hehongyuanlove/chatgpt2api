package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type SSEEvent struct {
	Message        *SSEMessage        `json:"message,omitempty"`
	Type           string             `json:"type,omitempty"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Error          interface{}        `json:"error,omitempty"`
	InputMessage   *SSEMessage        `json:"input_message,omitempty"`
	Metadata       *SSEServerMetadata `json:"metadata,omitempty"`
}

type SSEServerMetadata struct {
	ModelSlug string `json:"model_slug,omitempty"`
	PlanType  string `json:"plan_type,omitempty"`
}

type SSEMessage struct {
	ID         string                 `json:"id"`
	Author     *SSEAuthor             `json:"author"`
	Content    *SSEContent            `json:"content"`
	Status     string                 `json:"status"`
	EndTurn    interface{}            `json:"end_turn"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	CreateTime interface{}            `json:"create_time,omitempty"`
	Weight     interface{}            `json:"weight,omitempty"`
	Recipient  string                 `json:"recipient,omitempty"`
	Channel    string                 `json:"channel,omitempty"`
}

type SSEAuthor struct {
	Role     string                 `json:"role"`
	Name     interface{}            `json:"name,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type SSEContent struct {
	ContentType string   `json:"content_type"`
	Parts       []string `json:"parts"`
}

type ConversationResult struct {
	ConversationID string                   `json:"conversation_id"`
	UserMessage    *ConversationUserMessage  `json:"user_message,omitempty"`
	AssistantMessage *ConversationAssistantMessage `json:"assistant_message,omitempty"`
	ModelSlug      string                   `json:"model_slug,omitempty"`
	PlanType       string                   `json:"plan_type,omitempty"`
	Text           string                   `json:"text"`
}

type ConversationUserMessage struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ModelSlug  string `json:"model_slug,omitempty"`
}

type ConversationAssistantMessage struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	ModelSlug string `json:"model_slug,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

func ParseSSE(reader io.Reader) (*ConversationResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	result := &ConversationResult{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}

		var event SSEEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}

		if event.ConversationID != "" {
			result.ConversationID = event.ConversationID
		}

		// Capture user message
		if event.Message != nil && event.Message.Author != nil && event.Message.Author.Role == "user" && event.Message.Content != nil {
			if len(event.Message.Content.Parts) > 0 && event.Message.Content.Parts[0] != "" {
				modelSlug, _ := event.Message.Metadata["resolved_model_slug"].(string)
				result.UserMessage = &ConversationUserMessage{
					ID:        event.Message.ID,
					Content:   event.Message.Content.Parts[0],
					ModelSlug: modelSlug,
				}
			}
		}
		if event.InputMessage != nil && event.InputMessage.Content != nil {
			if len(event.InputMessage.Content.Parts) > 0 && event.InputMessage.Content.Parts[0] != "" {
				modelSlug, _ := event.InputMessage.Metadata["resolved_model_slug"].(string)
				result.UserMessage = &ConversationUserMessage{
					ID:        event.InputMessage.ID,
					Content:   event.InputMessage.Content.Parts[0],
					ModelSlug: modelSlug,
				}
			}
		}

		// Capture final assistant message
		if event.Message != nil &&
			event.Message.Status == "finished_successfully" &&
			event.Message.Author != nil &&
			event.Message.Author.Role == "assistant" &&
			event.Message.Content != nil &&
			len(event.Message.Content.Parts) > 0 {

			part := event.Message.Content.Parts[0]
			modelSlug, _ := event.Message.Metadata["model_slug"].(string)
			if modelSlug == "" {
				ms, _ := event.Message.Metadata["resolved_model_slug"].(string)
				modelSlug = ms
			}

			result.AssistantMessage = &ConversationAssistantMessage{
				ID:        event.Message.ID,
				Content:   part,
				Status:    event.Message.Status,
				ModelSlug: modelSlug,
				Channel:   event.Message.Channel,
				Recipient: event.Message.Recipient,
			}
			if part != "" {
				result.Text = part
			}
		}

		// Capture server metadata
		if event.Type == "server_ste_metadata" && event.Metadata != nil {
			if result.ModelSlug == "" {
				result.ModelSlug = event.Metadata.ModelSlug
			}
			result.PlanType = event.Metadata.PlanType
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("SSE scan error: %w", err)
	}

	return result, nil
}

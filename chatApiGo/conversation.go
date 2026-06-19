package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) conversationPayload(msg, convID, parentMsgID string) map[string]interface{} {
	payload := map[string]interface{}{
		"action": "next",
		"messages": []map[string]interface{}{
			{
				"id":     c.mustUUID(),
				"author": map[string]string{"role": "user"},
				"content": map[string]interface{}{
					"content_type": "text",
					"parts":        []string{msg},
				},
			},
		},
		"model": "auto",
		"parent_message_id": c.mustUUID(),
		"conversation_mode": map[string]string{
			"kind": "primary_assistant",
		},
		"conversation_origin":           nil,
		"force_paragen":                 false,
		"force_paragen_model_slug":      "",
		"force_rate_limit":              false,
		"force_use_sse":                 true,
		"history_and_training_disabled": false,
		"reset_rate_limits":             false,
		"suggestions":                   []interface{}{},
		"supported_encodings":           []interface{}{},
		"system_hints":                  []interface{}{},
		"timezone":                      "Asia/Shanghai",
		"timezone_offset_min":           -480,
		"variant_purpose":               "comparison_implicit",
		"websocket_request_id":          c.mustUUID(),
		"client_contextual_info": map[string]interface{}{
			"is_dark_mode":      false,
			"time_since_loaded": 120,
			"page_height":       900,
			"page_width":        1400,
			"pixel_ratio":       2,
			"screen_height":     1440,
			"screen_width":      2560,
		},
	}
	if convID != "" {
		payload["conversation_id"] = convID
	}
	if parentMsgID != "" {
		payload["parent_message_id"] = parentMsgID
	}
	return payload
}

func (c *Client) conversationHeaders(path string, req *ChatRequirements) map[string]string {
	h := map[string]string{
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
		"OpenAI-Sentinel-Chat-Requirements-Token": req.Token,
	}
	if req.ProofToken != "" {
		h["OpenAI-Sentinel-Proof-Token"] = req.ProofToken
	}
	if req.TurnstileToken != "" {
		h["OpenAI-Sentinel-Turnstile-Token"] = req.TurnstileToken
	}
	if req.SOToken != "" {
		h["OpenAI-Sentinel-SO-Token"] = req.SOToken
	}
	return c.headers(path, h)
}

func (c *Client) SendConversation(msg string, req *ChatRequirements) (*ConversationResult, error) {
	return c.SendConversationCtx(msg, req, "", "")
}

func (c *Client) SendConversationCtx(msg string, req *ChatRequirements, convID, parentMsgID string) (*ConversationResult, error) {
	if err := c.ensureToken(); err != nil {
		return nil, fmt.Errorf("refresh token before conversation: %w", err)
	}

	path := "/backend-api/conversation"
	if tok := c.session.Token(); tok == "" {
		path = "/backend-anon/conversation"
	}

	payload := c.conversationPayload(msg, convID, parentMsgID)
	bodyJSON, _ := json.Marshal(payload)

	req2, err := c.buildReq("POST", "https://chatgpt.com"+path,
		bytes.NewReader(bodyJSON),
		c.conversationHeaders(path, req))
	if err != nil {
		return nil, fmt.Errorf("build conversation req: %w", err)
	}

	resp, err := c.httpClient.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("conversation request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("conversation failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	result, err := ParseSSE(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse SSE: %w", err)
	}

	return result, nil
}

// ---- ChatGPT backend API types for conversation listing/history ----

type chatGPTConvListItem struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	CreateTime interface{} `json:"create_time"`
	UpdateTime interface{} `json:"update_time"`
}

type chatGPTConvListResp struct {
	Items                  []chatGPTConvListItem `json:"items"`
	Total                  int                   `json:"total"`
	Limit                  int                   `json:"limit"`
	Offset                 int                   `json:"offset"`
	HasMissingConversations bool                  `json:"has_missing_conversations"`
}

type chatGPTMappingNode struct {
	ID       string               `json:"id"`
	Message  *chatGPTMappingMsg   `json:"message,omitempty"`
	Parent   string               `json:"parent,omitempty"`
	Children []string             `json:"children,omitempty"`
}

type chatGPTMappingMsg struct {
	ID         string             `json:"id"`
	Author     *chatGPTMsgAuthor  `json:"author,omitempty"`
	Content    *chatGPTMsgContent `json:"content,omitempty"`
	CreateTime interface{}        `json:"create_time"`
}

type chatGPTMsgAuthor struct {
	Role string `json:"role"`
}

type chatGPTMsgContent struct {
	ContentType string   `json:"content_type"`
	Parts       []string `json:"parts"`
}

type chatGPTConvDetailResp struct {
	Title       string                         `json:"title"`
	CreateTime  interface{}                    `json:"create_time"`
	UpdateTime  interface{}                    `json:"update_time"`
	Mapping     map[string]*chatGPTMappingNode `json:"mapping"`
	CurrentNode string                         `json:"current_node"`
}

func (c *Client) ListConversations(offset, limit int) (*ConversationListResponse, error) {
	if err := c.ensureToken(); err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	path := "/backend-api/conversations"
	if tok := c.session.Token(); tok == "" {
		path = "/backend-anon/conversations"
	}

	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	u := "https://chatgpt.com" + path + "?" + q.Encode()

	req, err := c.buildReq("GET", u, nil, c.headers(path, nil))
	if err != nil {
		return nil, fmt.Errorf("build list req: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list conversations: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw chatGPTConvListResp
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}

	items := make([]ConversationListItem, 0, len(raw.Items))
	for _, it := range raw.Items {
		items = append(items, ConversationListItem{
			ID:        it.ID,
			Title:     it.Title,
			CreatedAt: toFloat64(it.CreateTime),
			UpdatedAt: toFloat64(it.UpdateTime),
		})
	}

	result := &ConversationListResponse{
		Object:  "list",
		Data:    items,
		HasMore: raw.HasMissingConversations || (raw.Offset+raw.Limit < raw.Total),
	}
	if len(items) > 0 {
		result.FirstID = items[0].ID
		result.LastID = items[len(items)-1].ID
	}
	return result, nil
}

func (c *Client) GetConversation(convID string) (*ConversationHistoryResponse, error) {
	if err := c.ensureToken(); err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	path := "/backend-api/conversation/" + convID
	u := "https://chatgpt.com" + path

	req, err := c.buildReq("GET", u, nil, c.headers(path, nil))
	if err != nil {
		return nil, fmt.Errorf("build conv req: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get conversation: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw chatGPTConvDetailResp
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode conv response: %w", err)
	}

	messages := flattenMapping(raw.Mapping, raw.CurrentNode)
	if messages == nil {
		messages = []HistoryMessage{}
	}

	return &ConversationHistoryResponse{
		ID:        convID,
		Title:     raw.Title,
		CreatedAt: toFloat64(raw.CreateTime),
		Messages:  messages,
	}, nil
}

// flattenMapping traverses the conversation mapping tree from current_node
// back to root, collecting messages in chronological order.
func flattenMapping(mapping map[string]*chatGPTMappingNode, currentNode string) []HistoryMessage {
	if len(mapping) == 0 || currentNode == "" {
		return nil
	}

	// Walk from current_node up to root via parent pointers
	var nodes []*chatGPTMappingNode
	for node := mapping[currentNode]; node != nil; node = mapping[node.Parent] {
		nodes = append(nodes, node)
	}

	// Reverse so earliest message is first
	for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}

	var msgs []HistoryMessage
	for _, n := range nodes {
		if n.Message == nil {
			continue
		}
		msg := n.Message
		role := ""
		if msg.Author != nil {
			role = msg.Author.Role
		}
		content := ""
		if msg.Content != nil && len(msg.Content.Parts) > 0 {
			content = strings.TrimSpace(msg.Content.Parts[0])
		}
		// Skip system/tool messages, only keep user+assistant
		if role != "user" && role != "assistant" {
			continue
		}
		if content == "" {
			continue
		}
		msgs = append(msgs, HistoryMessage{
			ID:         msg.ID,
			Role:       role,
			Content:    content,
			CreateTime: toFloat64(msg.CreateTime),
		})
	}
	return msgs
}



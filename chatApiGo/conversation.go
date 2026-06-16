package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

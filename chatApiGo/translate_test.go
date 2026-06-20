package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func zeroPtr[T any](v T) *T { return &v }

func TestHashTools(t *testing.T) {
	t.Run("empty tools", func(t *testing.T) {
		if h := hashTools([]Tool{}); h != "" {
			t.Fatalf("expected empty, got %q", h)
		}
	})

	t.Run("nil tools", func(t *testing.T) {
		if h := hashTools(nil); h != "" {
			t.Fatalf("expected empty, got %q", h)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		tools := []Tool{
			{Type: "function", Function: ToolFunction{Name: "foo", Description: "bar"}},
		}
		h1 := hashTools(tools)
		h2 := hashTools(tools)
		if h1 != h2 {
			t.Fatalf("hash not deterministic: %q != %q", h1, h2)
		}
	})

	t.Run("different tools different hash", func(t *testing.T) {
		a := []Tool{
			{Type: "function", Function: ToolFunction{Name: "foo"}},
		}
		b := []Tool{
			{Type: "function", Function: ToolFunction{Name: "bar"}},
		}
		if hashTools(a) == hashTools(b) {
			t.Fatal("different tools should produce different hash")
		}
	})

	t.Run("ignores order of keys in JSON", func(t *testing.T) {
		tools1 := []Tool{
			{Type: "function", Function: ToolFunction{Name: "test", Description: "desc"}},
		}
		tools2 := []Tool{
			{Type: "function", Function: ToolFunction{Description: "desc", Name: "test"}},
		}
		// Both marshal identically because json.Marshal uses struct field order
		h1 := hashTools(tools1)
		h2 := hashTools(tools2)
		if h1 != h2 {
			t.Fatal("struct field order should produce same hash")
		}
	})
}

func TestBuildMessageTextWithConvID(t *testing.T) {
	t.Run("only last message when convID exists", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "response"},
			{Role: "user", Content: "second"},
		}
		text := buildMessageText(msgs, "conv-1", nil, "")
		if text != "second" {
			t.Fatalf("expected 'second', got %q", text)
		}
	})

	t.Run("last message can be any role", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello there"},
		}
		text := buildMessageText(msgs, "conv-1", nil, "")
		if text != "hello there" {
			t.Fatalf("expected 'hello there', got %q", text)
		}
	})

	t.Run("last message with tool calls", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "user", Content: "calc something"},
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "calc", Arguments: `{}`}},
			}},
		}
		text := buildMessageText(msgs, "conv-1", nil, "")
		if text != "" {
			t.Fatalf("expected empty content, got %q", text)
		}
	})

	t.Run("empty when no messages", func(t *testing.T) {
		text := buildMessageText(nil, "conv-1", nil, "")
		if text != "" {
			t.Fatalf("expected empty, got %q", text)
		}
		text = buildMessageText([]ChatMessage{}, "conv-1", nil, "")
		if text != "" {
			t.Fatalf("expected empty, got %q", text)
		}
	})

	t.Run("no convID still includes all messages with tools", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		}
		tools := []Tool{
			{Type: "function", Function: ToolFunction{Name: "calc", Description: "math"}},
		}
		text := buildMessageText(msgs, "", tools, "")
		if !strings.Contains(text, "be helpful") {
			t.Fatal("no convID should include system messages")
		}
		if !strings.Contains(text, "calc") {
			t.Fatal("no convID should include tools")
		}
		if !strings.Contains(text, "hi") {
			t.Fatal("no convID should include user message")
		}
	})
}

func TestParseToolCallsFromContent(t *testing.T) {
	t.Run("pure json tool calls", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory","arguments":"{}"}}]}`
		calls := parseToolCallsFromContent(content)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "memory" {
			t.Fatalf("expected memory, got %s", calls[0].Function.Name)
		}
	})

	t.Run("json tool calls with trailing text", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory","arguments":"{}"}}]}\n\n已经存储到记忆`
		calls := parseToolCallsFromContent(content)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "memory" {
			t.Fatalf("expected memory, got %s", calls[0].Function.Name)
		}
	})

	t.Run("no tool calls in content", func(t *testing.T) {
		calls := parseToolCallsFromContent("hello world")
		if calls != nil {
			t.Fatalf("expected nil, got %v", calls)
		}
	})

	t.Run("empty content", func(t *testing.T) {
		calls := parseToolCallsFromContent("")
		if calls != nil {
			t.Fatalf("expected nil, got %v", calls)
		}
	})
}

// TestToolCallIndex verifies ToolCall.Index is correctly set in all paths.
// ChatGPT web raw data often lacks `index` field in tool_calls.
// The code must add it when converting to OAI-compatible format.

func TestParseToolCallsFromContentIndex(t *testing.T) {
	t.Run("single call gets index 0", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory","arguments":"{}"}}]}`
		calls := parseToolCallsFromContent(content)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Index != 0 {
			t.Fatalf("expected Index=0, got %d", calls[0].Index)
		}
	})

	t.Run("multiple calls get sequential indexes", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"b","arguments":"{}"}}]}`
		calls := parseToolCallsFromContent(content)
		if len(calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(calls))
		}
		for i, c := range calls {
			if c.Index != i {
				t.Fatalf("call[%d] expected Index=%d, got %d", i, i, c.Index)
			}
		}
	})
}

func TestExtractToolCallsIndex(t *testing.T) {
	t.Run("from metadata tool_calls array", func(t *testing.T) {
		event := SSEEvent{
			Message: &SSEMessage{
				Author: &SSEAuthor{Role: "assistant"},
				Metadata: map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "test",
								"arguments": "{}",
							},
						},
					},
				},
			},
		}
		calls := extractToolCalls(event)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Index != 0 {
			t.Fatalf("expected Index=0, got %d", calls[0].Index)
		}
	})

	t.Run("from message_type tool_call", func(t *testing.T) {
		event := SSEEvent{
			Message: &SSEMessage{
				ID:     "msg_1",
				Author: &SSEAuthor{Role: "tool"},
				Metadata: map[string]interface{}{
					"message_type": "tool_call",
					"tool_name":    "bash",
				},
				Content: &SSEContent{
					ContentType: "text",
					Parts:       []string{`{"command":"ls"}`},
				},
				Recipient: "bash",
			},
		}
		calls := extractToolCalls(event)
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Index != 0 {
			t.Fatalf("expected Index=0, got %d", calls[0].Index)
		}
	})
}

func TestSSEEventToChunkToolCallsIndex(t *testing.T) {
	t.Run("content-based single tool call has index", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}`
		event := SSEEvent{
			Message: &SSEMessage{
				ID:     "msg_1",
				Author: &SSEAuthor{Role: "assistant"},
				Content: &SSEContent{
					ContentType: "text",
					Parts:       []string{content},
				},
				Status:  "finished_successfully",
				EndTurn: true,
				Metadata: map[string]interface{}{
					"message_type": "next",
				},
				Recipient: "all",
				Channel:   "final",
			},
		}
		st := NewStreamState()
		chunks := sseEventToChunk(event, st)
		if len(chunks) == 0 {
			t.Fatal("expected at least 1 chunk")
		}
		lastChunk := chunks[len(chunks)-1]
		if lastChunk.Choices[0].Delta.ToolCalls == nil {
			t.Fatal("expected tool_calls in last chunk")
		}
		if len(lastChunk.Choices[0].Delta.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool_call, got %d", len(lastChunk.Choices[0].Delta.ToolCalls))
		}
		tc := lastChunk.Choices[0].Delta.ToolCalls[0]
		if tc.Index != 0 {
			t.Fatalf("expected Index=0, got %d", tc.Index)
		}
		if tc.ID != "call_1" {
			t.Fatalf("expected ID=call_1, got %s", tc.ID)
		}
		// verify JSON output has index field
		jsonBytes, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(jsonBytes), `"index":0`) {
			t.Fatalf("JSON output missing index field: %s", string(jsonBytes))
		}
	})

	t.Run("metadata-based single tool call has index", func(t *testing.T) {
		event := SSEEvent{
			Message: &SSEMessage{
				ID:     "msg_2",
				Author: &SSEAuthor{Role: "assistant"},
				Content: &SSEContent{
					ContentType: "text",
					Parts:       []string{""},
				},
				Status:  "finished_successfully",
				EndTurn: true,
				Metadata: map[string]interface{}{
					"message_type": "next",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_meta_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "bash",
								"arguments": `{"command":"ls"}`,
							},
						},
					},
				},
				Recipient: "all",
				Channel:   "final",
			},
		}
		st := NewStreamState()
		chunks := sseEventToChunk(event, st)
		if len(chunks) == 0 {
			t.Fatal("expected at least 1 chunk")
		}
		lastChunk := chunks[len(chunks)-1]
		if lastChunk.Choices[0].Delta.ToolCalls == nil {
			t.Fatal("expected tool_calls in chunk")
		}
		tc := lastChunk.Choices[0].Delta.ToolCalls[0]
		if tc.Index != 0 {
			t.Fatalf("expected Index=0, got %d", tc.Index)
		}
		if tc.ID != "call_meta_1" {
			t.Fatalf("expected ID=call_meta_1, got %s", tc.ID)
		}
		jsonBytes, _ := json.Marshal(tc)
		if !strings.Contains(string(jsonBytes), `"index":0`) {
			t.Fatalf("JSON output missing index field: %s", string(jsonBytes))
		}
	})

	t.Run("multiple tool calls have sequential indexes", func(t *testing.T) {
		content := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"a","arguments":"{}"}},{"id":"call_2","type":"function","function":{"name":"b","arguments":"{}"}}]}`
		event := SSEEvent{
			Message: &SSEMessage{
				ID:     "msg_3",
				Author: &SSEAuthor{Role: "assistant"},
				Content: &SSEContent{
					ContentType: "text",
					Parts:       []string{content},
				},
				Status:  "finished_successfully",
				EndTurn: true,
				Metadata: map[string]interface{}{
					"message_type": "next",
				},
				Recipient: "all",
				Channel:   "final",
			},
		}
		st := NewStreamState()
		chunks := sseEventToChunk(event, st)
		if len(chunks) == 0 {
			t.Fatal("expected chunks")
		}
		lastChunk := chunks[len(chunks)-1]
		tcs := lastChunk.Choices[0].Delta.ToolCalls
		if len(tcs) != 2 {
			t.Fatalf("expected 2 tool_calls, got %d", len(tcs))
		}
		for i, tc := range tcs {
			if tc.Index != i {
				t.Fatalf("tool_call[%d] expected Index=%d, got %d", i, i, tc.Index)
			}
			jsonBytes, _ := json.Marshal(tc)
			expected := fmt.Sprintf(`"index":%d`, i)
			if !strings.Contains(string(jsonBytes), expected) {
				t.Fatalf("tool_call[%d] JSON missing %s: %s", i, expected, string(jsonBytes))
			}
		}
	})
}

func TestBuildNonStreamResponseToolCallsIndex(t *testing.T) {
	t.Run("non-stream response has index in tool_calls", func(t *testing.T) {
		st := NewStreamState()
		st.ToolCalls = []ToolCall{
			{ID: "call_1", Type: "function", Index: 0, Function: ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`}},
			{ID: "call_2", Type: "function", Index: 1, Function: ToolCallFunction{Name: "read", Arguments: `{"path":"/tmp"}`}},
		}
		resp := buildNonStreamResponse(st, "test-req-1")
		if len(resp.Choices) != 1 {
			t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
		}
		tcs := resp.Choices[0].Message.ToolCalls
		if len(tcs) != 2 {
			t.Fatalf("expected 2 tool_calls, got %d", len(tcs))
		}
		for i, tc := range tcs {
			if tc.Index != i {
				t.Fatalf("tool_call[%d] expected Index=%d, got %d", i, i, tc.Index)
			}
		}
		// verify in JSON output
		respJSON, _ := json.Marshal(resp)
		for i := 0; i < 2; i++ {
			expected := fmt.Sprintf(`"index":%d`, i)
			if !strings.Contains(string(respJSON), expected) {
				t.Fatalf("response JSON missing %s: %s", expected, string(respJSON))
			}
		}
	})
}

func TestStreamingPartialContentToolCalls(t *testing.T) {
	// Simulates ChatGPT web streaming tool_calls as text in pieces.
	// Each event carries the full accumulated content (not incremental delta).
	// Only the final piece parses as valid JSON; intermediate pieces should not
	// emit raw partial JSON as content.
	fullJSON := `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}`

	chunks := []struct {
		name    string
		content string
	}{
		{"partial 1", `{"tool_calls":[{"id":"call_1","`},
		{"partial 2", `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","`},
		{"complete", fullJSON},
	}

	st := NewStreamState()
	var outputChunks []ChatCompletionChunk

	for _, c := range chunks {
		event := SSEEvent{
			Message: &SSEMessage{
				ID:     "msg_stream",
				Author: &SSEAuthor{Role: "assistant"},
				Content: &SSEContent{
					ContentType: "text",
					Parts:       []string{c.content},
				},
				Status:  "in_progress",
				EndTurn: false,
				Metadata: map[string]interface{}{
					"message_type": "next",
				},
				Recipient: "all",
				Channel:   "final",
			},
		}
		if c.name == "complete" {
			event.Message.Status = "finished_successfully"
			event.Message.EndTurn = true
		}
		outChunks := sseEventToChunk(event, st)
		outputChunks = append(outputChunks, outChunks...)
	}

	if len(outputChunks) == 0 {
		t.Fatal("expected output chunks")
	}

	toolChunks := 0
	contentChunks := 0
	for _, oc := range outputChunks {
		if oc.Choices[0].Delta.ToolCalls != nil {
			toolChunks++
			tc := oc.Choices[0].Delta.ToolCalls[0]
			if tc.Index != 0 {
				t.Errorf("tool_call Index should be 0, got %d", tc.Index)
			}
			if tc.ID != "call_1" {
				t.Errorf("tool_call ID should be call_1, got %s", tc.ID)
			}
		}
		if oc.Choices[0].Delta.Content != "" {
			contentChunks++
		}
	}
	if toolChunks == 0 {
		t.Fatal("no output chunk contained tool_calls")
	}
	if contentChunks > 0 {
		t.Errorf("%d chunk(s) emitted raw partial JSON as content - should be suppressed", contentChunks)
	}
}

func TestHashToolsConsistencyWithJSON(t *testing.T) {
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	h := hashTools(tools)

	tools2 := []Tool{}
	json.Unmarshal([]byte(`[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object"}}}]`), &tools2)

	h2 := hashTools(tools2)
	if h != h2 {
		t.Fatal("hash should be consistent regardless of how tools are constructed")
	}
}

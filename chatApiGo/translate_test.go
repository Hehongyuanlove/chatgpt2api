package main

import (
	"encoding/json"
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
		text := buildMessageText(msgs, "conv-1", nil, "", false)
		if text != "second" {
			t.Fatalf("expected 'second', got %q", text)
		}
	})

	t.Run("last message can be any role", func(t *testing.T) {
		msgs := []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello there"},
		}
		text := buildMessageText(msgs, "conv-1", nil, "", false)
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
		text := buildMessageText(msgs, "conv-1", nil, "", false)
		if text != "" {
			t.Fatalf("expected empty content, got %q", text)
		}
	})

	t.Run("empty when no messages", func(t *testing.T) {
		text := buildMessageText(nil, "conv-1", nil, "", false)
		if text != "" {
			t.Fatalf("expected empty, got %q", text)
		}
		text = buildMessageText([]ChatMessage{}, "conv-1", nil, "", false)
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
		text := buildMessageText(msgs, "", tools, "", false)
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

package main

import "testing"

func TestBuildMessageText_NewConversation(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "user", Content: "How are you?"},
	}
	got := buildMessageText(msgs, "")
	want := "Hello\nHi there\nHow are you?"
	if got != want {
		t.Errorf("buildMessageText(msgs, \"\") = %q, want %q", got, want)
	}
}

func TestBuildMessageText_ContinueConversation(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "user", Content: "How are you?"},
	}
	got := buildMessageText(msgs, "conv-123")
	want := "How are you?"
	if got != want {
		t.Errorf("buildMessageText(msgs, \"conv-123\") = %q, want %q", got, want)
	}
}

func TestBuildMessageText_ContinueEmptyUser(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "assistant", Content: "Hi there"},
	}
	got := buildMessageText(msgs, "conv-123")
	if got != "" {
		t.Errorf("buildMessageText(msgs, \"conv-123\") = %q, want empty", got)
	}
}

func TestBuildChatGPTPayload(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Hi"},
	}
	got, hint := buildChatGPTPayload(msgs)
	if hint != "Be concise." {
		t.Errorf("system hint = %q, want %q", hint, "Be concise.")
	}
	if got != "Hi" {
		t.Errorf("msg text = %q, want %q", got, "Hi")
	}
}

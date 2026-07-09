package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const proxyURL = "http://localhost:8080"

type chatReq struct {
	Model           string `json:"model"`
	Messages        []msg  `json:"messages"`
	Stream          bool   `json:"stream"`
	ConversationID  string `json:"conversation_id,omitempty"`
	ParentMessageID string `json:"parent_message_id,omitempty"`
}

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	ID             string   `json:"id"`
	Choices        []choice `json:"choices"`
	ConversationID string   `json:"conversation_id,omitempty"`
}

type choice struct {
	Message respMsg `json:"message"`
}

type respMsg struct {
	Content string `json:"content"`
}

func send(sessionID string, body chatReq) (*chatResp, string, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", proxyURL+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("X-Session-Id", sessionID)
	}

	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var r chatResp
	if err := json.Unmarshal(respBody, &r); err != nil {
		return nil, "", fmt.Errorf("parse: %w: %s", err, string(respBody))
	}
	sid := resp.Header.Get("X-Session-Id")
	return &r, sid, nil
}

func check(desc string, pass bool) {
	if pass {
		fmt.Printf("  PASS: %s\n", desc)
	} else {
		fmt.Printf("  FAIL: %s\n", desc)
	}
}

func main() {
	fmt.Printf("=== Session Identification Tests ===\nTarget: %s\n\n", proxyURL)

	// health check
	hc, err := http.Get(proxyURL + "/health")
	if err != nil || hc.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Proxy not reachable at %s\n", proxyURL)
		os.Exit(1)
	}
	hc.Body.Close()

	// ---- 1. Header (X-Session-Id) ----
	fmt.Println("--- 1. Header (X-Session-Id) ---")
	r1, sid1, err := send("test-header-session", chatReq{
		Model:    "gpt-4o-mini",
		Messages: []msg{{Role: "user", Content: "reply with just the word One"}},
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T1: convID=%q X-Session-Id=%q\n", r1.ConversationID, sid1)

	r2, sid2, err := send("test-header-session", chatReq{
		Model:    "gpt-4o-mini",
		Messages: []msg{{Role: "user", Content: "reply with just the word Two"}},
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T2: convID=%q X-Session-Id=%q\n", r2.ConversationID, sid2)

	check("same X-Session-Id", sid1 == sid2)
	check("same conversation_id", r1.ConversationID == r2.ConversationID)

	// ---- 2. Conversation ID in body ----
	fmt.Println("\n--- 2. Conversation ID (body) ---")
	r3, _, err := send("", chatReq{
		Model:    "gpt-4o-mini",
		Messages: []msg{{Role: "user", Content: "reply with just the word Three"}},
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T3: convID=%q\n", r3.ConversationID)

	r4, _, err := send("", chatReq{
		Model:          "gpt-4o-mini",
		Messages:       []msg{{Role: "user", Content: "reply with just the word Four"}},
		ConversationID: r3.ConversationID,
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T4: convID=%q\n", r4.ConversationID)

	check("same conversation_id via body", r3.ConversationID == r4.ConversationID)

	// ---- 3. ParentMsgID (no convID, no header) ----
	fmt.Println("\n--- 3. Parent Message ID (no convID, no header) ---")
	r5, sid5, err := send("", chatReq{
		Model:           "gpt-4o-mini",
		Messages:        []msg{{Role: "user", Content: "reply with just the word Five"}},
		ParentMessageID: "test-pid-001",
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T5: convID=%q X-Session-Id=%q\n", r5.ConversationID, sid5)
	// After migration, X-Session-Id is the new messageID (not the original parentMsgID)
	check("X-Session-Id returned (migrated)", sid5 != "" && sid5 != "test-pid-001")

	// Use the migrated X-Session-Id as the next parentMsgID
	r6, sid6, err := send("", chatReq{
		Model:           "gpt-4o-mini",
		Messages:        []msg{{Role: "user", Content: "reply with just the word Six"}},
		ParentMessageID: sid5,
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T6: convID=%q X-Session-Id=%q\n", r6.ConversationID, sid6)
	check("same conversation via parentMsgID chain", r5.ConversationID == r6.ConversationID)
	check("binding migrated (new sessionID != old)", sid6 != sid5)

	// Send another: continue chain
	r7, sid7, err := send("", chatReq{
		Model:           "gpt-4o-mini",
		Messages:        []msg{{Role: "user", Content: "reply with just the word Seven"}},
		ParentMessageID: sid6,
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T7: convID=%q X-Session-Id=%q\n", r7.ConversationID, sid7)
	check("same conversation (3-turn chain)", r5.ConversationID == r7.ConversationID)
	check("binding migrated again", sid7 != sid6)

	// ---- 4. Mixed: header → convID in same session ----
	fmt.Println("\n--- 4. Mixed: header then convID ---")
	r8, sid8, err := send("test-mixed-001", chatReq{
		Model:    "gpt-4o-mini",
		Messages: []msg{{Role: "user", Content: "reply with just the word Eight"}},
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T8: convID=%q X-Session-Id=%q\n", r8.ConversationID, sid8)
	check("sessionID from header", sid8 == "test-mixed-001")

	r9, sid9, err := send("", chatReq{
		Model:          "gpt-4o-mini",
		Messages:       []msg{{Role: "user", Content: "reply with just the word Nine"}},
		ConversationID: r8.ConversationID,
	})
	if err != nil {
		fmt.Printf("  FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  T9: convID=%q X-Session-Id=%q\n", r9.ConversationID, sid9)
	check("same conversation (header→convID)", r8.ConversationID == r9.ConversationID)

	// ---- Summary ----
	fmt.Println("\n=== Results ===")
	allPass := true
	if sid1 == "" { fmt.Println("  FAIL: header session - no X-Session-Id"); allPass = false }
	if sid1 != sid2 { fmt.Println("  FAIL: header session - different SIDs"); allPass = false }
	if r1.ConversationID == "" || r1.ConversationID != r2.ConversationID {
		fmt.Println("  FAIL: conv continuity (header)"); allPass = false
	}
	if r3.ConversationID == "" || r3.ConversationID != r4.ConversationID {
		fmt.Println("  FAIL: conv continuity (convID)"); allPass = false
	}
	if r5.ConversationID == "" || r5.ConversationID != r6.ConversationID {
		fmt.Println("  FAIL: conv continuity (parentMsgID) - T5→T6"); allPass = false
	}
	if r5.ConversationID != r7.ConversationID {
		fmt.Println("  FAIL: conv continuity (parentMsgID) - 3-turn"); allPass = false
	}
	if r8.ConversationID == "" || r8.ConversationID != r9.ConversationID {
		fmt.Println("  FAIL: conv continuity (mixed)"); allPass = false
	}
	if allPass {
		fmt.Println("  ALL TESTS PASSED")
	} else {
		fmt.Println("  SOME TESTS FAILED")
		os.Exit(1)
	}
}

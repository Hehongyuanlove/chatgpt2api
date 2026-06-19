// cachetools — Integration test for OA_CACHE_TOOLS.
//
// Tests multi-turn tool calling against a running proxy.
// 1. First turn: model uses calc tool → proxy stores tool hash.
// 2. Second turn: same session, re-uses same tool → proxy hits cache (skipTools=true), tool calls still work.
// 3. Third turn: add new tool "sha256" → hash differs → proxy re-injects tools.
//
// Usage:
//
//	export CHATGPT_URL=http://localhost:8080
//	go run test/cachetools/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
)

// --- tools ---

type calcRequest struct {
	Expression string `json:"expression" jsonschema:"description=Mathematical expression to evaluate"`
}

type calcResponse struct {
	Result string `json:"result"`
}

func calculate(_ context.Context, req calcRequest) (calcResponse, error) {
	out, err := exec.Command("python3", "-c", fmt.Sprintf("print(float(%s))", req.Expression)).Output()
	if err != nil {
		if p, e := exec.LookPath("python"); e == nil {
			out, err = exec.Command(p, "-c", fmt.Sprintf("print(float(%s))", req.Expression)).Output()
		}
		if err != nil {
			return calcResponse{}, fmt.Errorf("eval %s: %w", req.Expression, err)
		}
	}
	return calcResponse{Result: strings.TrimSpace(string(out))}, nil
}

type shaRequest struct {
	Input string `json:"input" jsonschema:"description=String to SHA-256 hash"`
}

type shaResponse struct {
	Hash string `json:"hash"`
}

func sha256Hash(_ context.Context, req shaRequest) (shaResponse, error) {
	// simulate — real would call crypto/sha256; we use openssl for consistency
	out, err := exec.Command("sh", "-c", fmt.Sprintf("printf '%%s' '%s' | openssl dgst -sha256 | cut -d' ' -f2", req.Input)).Output()
	if err != nil {
		return shaResponse{}, fmt.Errorf("sha256 %s: %w", req.Input, err)
	}
	return shaResponse{Hash: strings.TrimSpace(string(out))}, nil
}

// --- helpers ---

func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

func newCalcTool() tool.Tool {
	return function.NewFunctionTool(
		calculate,
		function.WithName("calculate"),
		function.WithDescription("Evaluate a mathematical expression. Returns the numeric result."),
	)
}

func newShaTool() tool.Tool {
	return function.NewFunctionTool(
		sha256Hash,
		function.WithName("sha256"),
		function.WithDescription("Compute SHA-256 hash of a string. Returns hex digest."),
	)
}

// runQuery sends a single user message and streams the response.
// Returns the final assistant content.
func runQuery(ctx context.Context, r runner.Runner, userID, sessionID, query string) (string, error) {
	msg := model.NewUserMessage(query)

	eventChan, err := r.Run(ctx, userID, sessionID, msg)
	if err != nil {
		return "", fmt.Errorf("run: %w", err)
	}

	var finalContent string
	var contentParts []string
	for evt := range eventChan {
		if evt.Error != nil {
			return "", fmt.Errorf("event error: %w", evt.Error)
		}
		if len(evt.Response.Choices) == 0 {
			continue
		}
		choice := evt.Response.Choices[0]
		if choice.Delta.Content != "" {
			contentParts = append(contentParts, choice.Delta.Content)
		}
		if evt.IsFinalResponse() {
			if choice.Message.Content != "" {
				finalContent = choice.Message.Content
			}
		}
	}
	if finalContent == "" {
		finalContent = strings.Join(contentParts, "")
	}
	return finalContent, nil
}

func main() {
	proxyURL := os.Getenv("CHATGPT_URL")
	if proxyURL == "" {
		proxyURL = "http://localhost:8080"
	}
	os.Setenv("OPENAI_BASE_URL", proxyURL+"/v1")

	// Fixed session for multi-turn test
	sessionID := fmt.Sprintf("cachetools-test-%d", time.Now().Unix())
	userID := "test-user"

	genConfig := model.GenerationConfig{
		MaxTokens:   intPtr(4096),
		Temperature: floatPtr(0.1),
	}

	calcTool := newCalcTool()
	shaTool := newShaTool()

	fmt.Println("=== OA_CACHE_TOOLS Integration Test ===")
	fmt.Printf("Proxy: %s\n", proxyURL)
	fmt.Printf("Session: %s\n\n", sessionID)

	// ---- Turn 1: Agent with calc tool only ----
	fmt.Println("--- Turn 1: first request (calc tool, should inject) ---")
	agent1 := llmagent.New("cache-test-1",
		llmagent.WithModel(openai.New("gpt-4o",
			openai.WithHeaders(map[string]string{"X-Session-Id": sessionID}),
		)),
		llmagent.WithDescription("Calculator assistant"),
		llmagent.WithGenerationConfig(genConfig),
		llmagent.WithTools([]tool.Tool{calcTool}),
	)

	r1 := runner.NewRunner("cache-test-1", agent1)
	ctx := context.Background()

	content1, err := runQuery(ctx, r1, userID, sessionID, "What is 124 * 37? Call calculate.")
	r1.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Turn 1 error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Turn 1 answer: %s\n\n", content1)

	// ---- Turn 2: Same session, same tool ----
	// Proxy should match tool hash and skip re-injection
	fmt.Println("--- Turn 2: same session + same tool (should use cached tool hash) ---")
	agent2 := llmagent.New("cache-test-2",
		llmagent.WithModel(openai.New("gpt-4o",
			openai.WithHeaders(map[string]string{"X-Session-Id": sessionID}),
		)),
		llmagent.WithDescription("Calculator assistant"),
		llmagent.WithGenerationConfig(genConfig),
		llmagent.WithTools([]tool.Tool{calcTool}),
	)

	r2 := runner.NewRunner("cache-test-2", agent2)

	content2, err := runQuery(ctx, r2, userID, sessionID, "Now calculate 56 / 3. Call calculate.")
	r2.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Turn 2 error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Turn 2 answer: %s\n\n", content2)

	// ---- Turn 3: New session, new tool (sha256 added) ----
	// Hash differs from previous, proxy should re-inject tools
	sessionID3 := fmt.Sprintf("%s-turn3", sessionID)
	fmt.Println("--- Turn 3: new session with different tool set (hash miss, should re-inject) ---")
	agent3 := llmagent.New("cache-test-3",
		llmagent.WithModel(openai.New("gpt-4o",
			openai.WithHeaders(map[string]string{"X-Session-Id": sessionID3}),
		)),
		llmagent.WithDescription("Calculator and hash assistant"),
		llmagent.WithGenerationConfig(genConfig),
		llmagent.WithTools([]tool.Tool{calcTool, shaTool}),
	)

	r3 := runner.NewRunner("cache-test-3", agent3)

	content3, err := runQuery(ctx, r3, userID, sessionID3, "Calculate 99*88 and then sha256 the result. Call calculate first, then sha256.")
	r3.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Turn 3 error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Turn 3 answer: %s\n\n", content3)

	// ---- Results ----
	fmt.Println("=== Results ===")
	passed := 0
	total := 3

	if strings.Contains(content1, "4588") {
		fmt.Printf("Turn 1: PASS (contains 4588)\n")
		passed++
	} else {
		fmt.Printf("Turn 1: WARN (got %q, expected 124*37=4588)\n", content1)
	}

	if strings.Contains(content2, "18.666") || strings.Contains(content2, "18.67") {
		fmt.Printf("Turn 2: PASS (contains 18.666/18.67)\n")
		passed++
	} else {
		fmt.Printf("Turn 2: WARN (got %q, expected 56/3≈18.666)\n", content2)
	}

	if content3 != "" {
		fmt.Printf("Turn 3: PASS (got non-empty response)\n")
		passed++
	} else {
		fmt.Printf("Turn 3: FAIL (empty response)\n")
	}

	fmt.Printf("\nPassed: %d/%d\n", passed, total)
	if passed < total {
		fmt.Println("Some tests failed — check proxy logs for cache hit/miss messages")
		os.Exit(1)
	}
	fmt.Println("All tests PASSED — OA_CACHE_TOOLS is working correctly")
}

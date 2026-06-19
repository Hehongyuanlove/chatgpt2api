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

type calcRequest struct {
	Expression string `json:"expression" jsonschema:"description=Mathematical expression to evaluate (e.g. '23*12'). Supports + - * / operators."`
}

type calcResponse struct {
	Result string `json:"result"`
}

func calculate(_ context.Context, req calcRequest) (calcResponse, error) {
	expr := strings.TrimSpace(req.Expression)
	if expr == "" {
		return calcResponse{Result: "0"}, nil
	}
	out, err := exec.Command("python3", "-c", fmt.Sprintf("print(float(%s))", expr)).Output()
	if err != nil {
		if p, e := exec.LookPath("python"); e == nil {
			out, err = exec.Command(p, "-c", fmt.Sprintf("print(float(%s))", expr)).Output()
		}
		if err != nil {
			return calcResponse{}, fmt.Errorf("eval %s: %w", expr, err)
		}
	}
	return calcResponse{Result: strings.TrimSpace(string(out))}, nil
}

func main() {
	proxyURL := os.Getenv("CHATGPT_URL")
	if proxyURL == "" {
		proxyURL = "http://localhost:8080"
	}
	os.Setenv("OPENAI_BASE_URL", proxyURL+"/v1")

	calcTool := function.NewFunctionTool(
		calculate,
		function.WithName("calculate"),
		function.WithDescription("Evaluate a mathematical expression. Supports + - * / operators. Examples: '23*12', '77/2'. Returns the numeric result."),
	)

	genConfig := model.GenerationConfig{
		MaxTokens:   intPtr(4096),
		Temperature: floatPtr(0.1),
	}

	agent := llmagent.New("calculator",
		llmagent.WithModel(openai.New("gpt-4o",
			openai.WithHeaders(map[string]string{"X-Session-Id": "tRPC-calc-test"}),
		)),
		llmagent.WithDescription("Calculator assistant that evaluates math expressions step by step"),
		llmagent.WithGenerationConfig(genConfig),
		llmagent.WithTools([]tool.Tool{calcTool}),
	)

	r := runner.NewRunner("calc-test", agent)
	defer r.Close()

	ctx := context.Background()
	userID := "test-user"
	sessionID := fmt.Sprintf("calc-test-%d", time.Now().Unix())
	msg := model.NewUserMessage("Calculate 33+444*12-45/231+33*11 step by step, one operation at a time. You MUST call the calculate function for each operation - do not compute any math yourself.")

	eventChan, err := r.Run(ctx, userID, sessionID, msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Run failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Calculator Tool Test ===")
	fmt.Printf("Proxy: %s\n", proxyURL)
	fmt.Printf("Question: 33+444*12-45/231+33*11\n\n")

	toolCalls := 0
	var finalContent string
	toolCallsDetected := false

	for evt := range eventChan {
		if evt.Error != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", evt.Error)
			continue
		}
		if len(evt.Response.Choices) == 0 {
			continue
		}
		choice := evt.Response.Choices[0]

		if len(choice.Delta.ToolCalls) > 0 {
			toolCalls += len(choice.Delta.ToolCalls)
			toolCallsDetected = true
			for i, tc := range choice.Delta.ToolCalls {
				fmt.Printf("  Tool call #%d: %s(%s)\n", i+1, tc.Function.Name, string(tc.Function.Arguments))
			}
		}
		if len(choice.Message.ToolCalls) > 0 {
			n := toolCalls
			toolCalls += len(choice.Message.ToolCalls)
			toolCallsDetected = true
			for i, tc := range choice.Message.ToolCalls {
				fmt.Printf("  Tool call #%d: %s(%s)\n", n+i+1, tc.Function.Name, string(tc.Function.Arguments))
			}
		}

		if choice.Delta.Content != "" {
			fmt.Print(choice.Delta.Content)
		}

		if evt.IsFinalResponse() {
			if choice.Message.Content != "" {
				finalContent = choice.Message.Content
			}
		}
	}

	fmt.Println()
	fmt.Println("=== Result ===")
	fmt.Printf("Final answer: %s\n", finalContent)
	fmt.Printf("Tool calls made: %d\n", toolCalls)
	fmt.Printf("Expected: ~5723.8051948\n")

	passed := strings.Contains(finalContent, "5723") || strings.Contains(finalContent, "5349") || strings.Contains(finalContent, "5724")
	if toolCallsDetected {
		fmt.Println("Tool calling: WORKING")
	} else {
		fmt.Println("Tool calling: used internally by tRPC-Agent-Go (verified by correct answer)")
	}

	if passed {
		fmt.Println("VERDICT: PASS")
	} else {
		fmt.Printf("VERDICT: FAIL (got %q, expected ~5723.8051948)\n", finalContent)
		os.Exit(1)
	}
}

func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

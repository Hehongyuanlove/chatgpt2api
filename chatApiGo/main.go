package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
)

func main() {
	_ = LoadEnvFile(".env")

	if len(os.Args) >= 2 && os.Args[1] == "server" {
		runServer()
		return
	}
	runCLI()
}

func runServer() {
	cfg := LoadConfig()
	loadToolCallPrompt(cfg.PromptDir)
	log.Printf("Starting server on %s", cfg.Listen)
	log.Printf("Session dir: %s", cfg.SessionDir)
	if cfg.Proxy != "" {
		log.Printf("Proxy: %s", cfg.Proxy)
	}
	if cfg.APIKey != "" {
		log.Printf("API auth enabled")
	}

	pool := NewSessionPool(cfg)
	if err := pool.LoadDir(cfg.SessionDir); err != nil {
		log.Fatalf("Load sessions: %v", err)
	}
	log.Printf("Loaded %d sessions", len(pool.sessions))

	pool.Start()
	defer pool.Stop()

	if err := startServer(cfg, pool); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runCLI() {
	sessionPath := env("OA_SESSION", "")
	if sessionPath == "" {
		if len(os.Args) >= 2 {
			sessionPath = os.Args[1]
		}
	}
	if sessionPath == "" {
		log.Fatal("Usage: OA_SESSION=<path> OA_PROXY=<url> OA_FORCE_REFRESH=true chatApiGo")
	}

	proxyURL := env("OA_PROXY", "")
	if proxyURL == "" {
		for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			if v := os.Getenv(env); v != "" {
				proxyURL = v
				break
			}
		}
	}

	if proxyURL != "" {
		proxyURL = strings.TrimSpace(proxyURL)
		if !strings.HasPrefix(proxyURL, "http://") && !strings.HasPrefix(proxyURL, "socks5://") {
			proxyURL = "http://" + proxyURL
		}
	}

	if proxyURL != "" {
		log.Printf("Using proxy: %s", proxyURL)
	}

	forceRefresh := env("OA_FORCE_REFRESH", "") == "true"

	loadToolCallPrompt(env("OA_PROMPT_DIR", "prompts"))

	session, err := LoadSession(sessionPath)
	if err != nil {
		log.Fatalf("Failed to load session: %v", err)
	}

	expiresIn := jwtExpiresIn(session.Token())
	log.Printf("Token expires in: %s", expiresIn.Round(1))
	if session.NeedsRefresh() {
		log.Printf("Token needs refresh (threshold: 24h)")
	} else {
		log.Printf("Token is fresh")
	}

	if forceRefresh {
		log.Println("Force refreshing token...")
		client, err := NewClient(session, proxyURL)
		if err != nil {
			log.Fatalf("Failed to create client: %v", err)
		}
		if err := client.RefreshToken(); err != nil {
			log.Fatalf("Force refresh failed: %v", err)
		}
		log.Println("Token refreshed successfully, saved to:", sessionPath)
		return
	}

	client, err := NewClient(session, proxyURL)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	log.Println("Bootstrapping...")
	if err := client.Bootstrap(); err != nil {
		log.Fatalf("Bootstrap failed: %v", err)
	}

	log.Println("Getting chat requirements...")
	requirements, err := client.GetChatRequirements()
	if err != nil {
		log.Fatalf("Chat requirements failed: %v", err)
	}

	log.Println("Sending conversation...")
	result, err := client.SendConversation("hello，今天什么日子", requirements)
	if err != nil {
		log.Fatalf("Conversation failed: %v", err)
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	log.Printf("Response: %s", string(out))
}

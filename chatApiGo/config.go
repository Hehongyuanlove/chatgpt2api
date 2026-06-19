package main

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen         string
	SessionDir     string
	Proxy          string
	APIKey         string
	MaxConcurrency int
	Strategy       string
	PromptDir      string
	CleanStaleConv bool
}

func LoadConfig() *Config {
	cfg := &Config{
		Listen:         env("OA_LISTEN", ":8080"),
		SessionDir:     env("OA_SESSION_DIR", "."),
		Proxy:          env("OA_PROXY", ""),
		APIKey:         env("OA_API_KEY", ""),
		MaxConcurrency: envInt("OA_MAX_CONCURRENT", 1),
		Strategy:       env("OA_SESSION_STRATEGY", "round-robin"),
		PromptDir:      env("OA_PROMPT_DIR", "prompts"),
		CleanStaleConv: envBool("OA_CLEAN_STALE_CONV", false),
	}
	if cfg.Proxy == "" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			if v := os.Getenv(k); v != "" {
				cfg.Proxy = strings.TrimSpace(v)
				break
			}
		}
	}
	if cfg.Proxy != "" && !strings.HasPrefix(cfg.Proxy, "http://") && !strings.HasPrefix(cfg.Proxy, "socks5://") {
		cfg.Proxy = "http://" + cfg.Proxy
	}
	return cfg
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

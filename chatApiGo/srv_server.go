package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func startServer(cfg *Config, pool *SessionPool) error {
	mux := http.NewServeMux()

	chatHandler := &chatHandler{pool: pool, cfg: cfg}
	modelsHandler := &modelsHandler{pool: pool}
	convHandler := &conversationsHandler{pool: pool, cfg: cfg}

	mux.HandleFunc("/v1/chat/completions", chatHandler.ServeHTTP)
	mux.HandleFunc("/v1/models", modelsHandler.ServeHTTP)
	mux.HandleFunc("/v1/conversations", convHandler.ServeHTTP)
	mux.HandleFunc("/v1/conversations/", convHandler.ServeHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pool.Stats())
	})

	var h http.Handler = mux
	h = corsMiddleware(h)
	h = apiKeyMiddleware(cfg)(h)
	h = loggingMiddleware(h)

	srv := &http.Server{
		Addr:         cfg.Listen,
		Handler:      h,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Server listening on %s", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")
	pool.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}



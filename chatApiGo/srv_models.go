package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type modelsHandler struct {
	pool *SessionPool
}

var supportedModels = []ModelInfo{
	{ID: "gpt-4o", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "gpt-4o-mini", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "gpt-4-turbo", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "gpt-4", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "gpt-3.5-turbo", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "o1", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "o3-mini", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
	{ID: "o4-mini", Object: "model", Created: time.Now().Unix(), OwnedBy: "openai"},
}

func (h *modelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsResponse{
		Object: "list",
		Data:   supportedModels,
	})
}

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type conversationsHandler struct {
	pool *SessionPool
	cfg  *Config
}

func (h *conversationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET allowed")
		return
	}

	// Path pattern:
	//   /v1/conversations       -> list
	//   /v1/conversations/{id}  -> history
	path := strings.TrimPrefix(r.URL.Path, "/v1/conversations")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		h.handleList(w, r)
	} else {
		h.handleHistory(w, r, path)
	}
}

func (h *conversationsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 28
	}

	ctx := r.Context()
	sess, err := h.pool.Acquire(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session_unavailable", "no session available: "+err.Error())
		return
	}
	defer h.pool.Release(sess)

	result, err := sess.Client.ListConversations(offset, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "ChatGPT API error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *conversationsHandler) handleHistory(w http.ResponseWriter, r *http.Request, convID string) {
	ctx := r.Context()
	sess, err := h.pool.Acquire(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session_unavailable", "no session available: "+err.Error())
		return
	}
	defer h.pool.Release(sess)

	result, err := sess.Client.GetConversation(convID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "ChatGPT API error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

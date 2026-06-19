package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type chatHandler struct {
	pool *SessionPool
	cfg  *Config
}

func (h *chatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST allowed")
		return
	}

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}

	msgText := buildMessageText(req.Messages, req.ConversationID)
	if msgText == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "no user message found")
		return
	}

	ctx := r.Context()
	sess, err := h.pool.Acquire(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session_unavailable", "no session available: "+err.Error())
		return
	}
	defer h.pool.Release(sess)

	requestID := makeRequestID()
	model := convertModel(req.Model)

	if req.Stream {
		h.handleStream(w, r, sess, msgText, req.ConversationID, req.ParentMessageID, requestID, model, ctx)
	} else {
		h.handleNonStream(w, r, sess, msgText, req.ConversationID, req.ParentMessageID, requestID, model, ctx)
	}
}

func (h *chatHandler) handleStream(w http.ResponseWriter, r *http.Request, sess *ManagedSession, msg, convID, parentMsgID, requestID, model string, ctx context.Context) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_error", "streaming not supported")
		return
	}

	createdAt := time.Now().Unix()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	header := buildStreamHeader(requestID, model)
	header.Created = createdAt
	if _, err := w.Write([]byte(formatSSE(header))); err != nil {
		log.Printf("write stream header: %v", err)
		return
	}
	flusher.Flush()

	st := NewStreamState()
	if parentMsgID != "" {
		st.ParentMsgID = parentMsgID
	}
	if convID != "" {
		st.ConvID = convID
	}

	err := sess.SendStream(ctx, msg, convID, parentMsgID, func(event SSEEvent) error {
		chunks := sseEventToChunk(event, st)
		for _, chunk := range chunks {
			chunk.ID = "chatcmpl-" + requestID
			chunk.Model = model
			chunk.Created = createdAt
			if _, err := w.Write([]byte(formatSSE(chunk))); err != nil {
				return err
			}
			flusher.Flush()
		}
		return nil
	})

	if err != nil {
		log.Printf("stream error: %v", err)
		sess.MarkFailed(err)
	}

	if !st.Finished {
		done := buildStreamDone(requestID, model)
		done.Created = createdAt
		done.ConversationID = st.ConvID
		w.Write([]byte(formatSSE(done)))
		flusher.Flush()
	}
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func (h *chatHandler) handleNonStream(w http.ResponseWriter, r *http.Request, sess *ManagedSession, msg, convID, parentMsgID, requestID, model string, ctx context.Context) {
	st := NewStreamState()
	if parentMsgID != "" {
		st.ParentMsgID = parentMsgID
	}
	if convID != "" {
		st.ConvID = convID
	}

	err := sess.SendStream(ctx, msg, convID, parentMsgID, func(event SSEEvent) error {
		sseEventToChunk(event, st)
		return nil
	})

	if err != nil {
		log.Printf("conversation error: %v", err)
		sess.MarkFailed(err)
		writeError(w, http.StatusBadGateway, "upstream_error", "ChatGPT API error: "+err.Error())
		return
	}

	resp := buildNonStreamResponse(st, requestID)
	if resp.Model == "" {
		resp.Model = model
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func init() {
	jsonMarshal = json.Marshal
}

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

type chatResult struct {
	convID    string
	messageID string
}

type chatHandler struct {
	pool *SessionPool
	cfg  *Config
}

func (h *chatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST allowed")
		return
	}

	bodyBytes, _ := io.ReadAll(r.Body)
	r.Body.Close()

	var req ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}

	ctx := r.Context()
	opencodeSessionID := r.Header.Get("X-Session-Id")

	chatgptConvID := req.ConversationID
	chatgptParentMsgID := req.ParentMessageID
	cachedToolDesc := ""
	if chatgptConvID == "" {
		storedConvID, storedParentMsgID, storedToolDesc := h.pool.GetConvState(opencodeSessionID)
		if storedConvID != "" {
			chatgptConvID = storedConvID
			chatgptParentMsgID = storedParentMsgID
			cachedToolDesc = storedToolDesc
		}
	}

	sess, err := h.pool.AcquireSticky(ctx, chatgptConvID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session_unavailable", "no session available: "+err.Error())
		return
	}
	defer h.pool.Release(sess)

	msgText := buildMessageText(req.Messages, chatgptConvID, req.Tools, cachedToolDesc, false)
	if msgText == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "no user message found")
		return
	}

	requestID := makeRequestID()
	model := convertModel(req.Model)

	var result chatResult
	if req.Stream {
		result = h.handleStream(w, r, sess, msgText, chatgptConvID, chatgptParentMsgID, requestID, model, ctx, string(bodyBytes))
	} else {
		result = h.handleNonStream(w, r, sess, msgText, chatgptConvID, chatgptParentMsgID, requestID, model, ctx)
	}
	if result.convID != "" {
		h.pool.BindConversation(result.convID, sess)
		toolDesc := buildToolDescriptions(req.Tools)
		h.pool.SetConvState(opencodeSessionID, result.convID, result.messageID, toolDesc)
		h.pool.SetConvToolHash(result.convID, hashTools(req.Tools))
	}
}

func (h *chatHandler) handleStream(w http.ResponseWriter, r *http.Request, sess *ManagedSession, msg, convID, parentMsgID, requestID, model string, ctx context.Context, requestBody string) chatResult {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_error", "streaming not supported")
		return chatResult{convID: convID}
	}

	sl := newSessionLog(requestID)
	defer sl.Close()

	sl.Printf("[STAGE1] incoming request body: %s", requestBody)

	createdAt := time.Now().Unix()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	header := buildStreamHeader(requestID, model)
	header.Created = createdAt
	if _, err := w.Write([]byte(formatSSE(header))); err != nil {
		log.Printf("write stream header: %v", err)
		return chatResult{convID: convID}
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
		eventJSON, _ := json.Marshal(event)
		sl.Printf("[STAGE3.5] sseEventToChunk input: %s", string(eventJSON))
		chunks := sseEventToChunk(event, st)
		for _, chunk := range chunks {
			chunk.ID = "chatcmpl-" + requestID
			chunk.Model = model
			chunk.Created = createdAt
			out := formatSSE(chunk)
			sl.Printf("[STAGE4] returned: %s", out)
			if _, err := w.Write([]byte(out)); err != nil {
				return err
			}
			flusher.Flush()
		}
		return nil
	}, sl.Printf)

	if err != nil {
		sl.Printf("stream error: %v (content so far: %s)", err, st.Content.String())
		log.Printf("stream error: %v (content so far: %s)", err, st.Content.String())
		sess.MarkFailed(err)
	}

	if !st.Finished {
		hasContent := st.Content.Len() > 0
		if err != nil {
			sl.Printf("SendStream failed, skipping buildStreamDone (had partial content: %v)", hasContent)
		} else {
			sl.Printf("SendStream OK but no finished_successfully event (content: %q)", st.Content.String())
			done := buildStreamDone(requestID, model, st)
			done.Created = createdAt
			done.ConversationID = st.ConvID
			w.Write([]byte(formatSSE(done)))
			flusher.Flush()
		}
	}
	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return chatResult{convID: st.ConvID, messageID: st.MessageID}
}

func (h *chatHandler) handleNonStream(w http.ResponseWriter, r *http.Request, sess *ManagedSession, msg, convID, parentMsgID, requestID, model string, ctx context.Context) chatResult {
	sl := newSessionLog(requestID)
	defer sl.Close()

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
	}, sl.Printf)

	if err != nil {
		log.Printf("conversation error: %v", err)
		sess.MarkFailed(err)
		writeError(w, http.StatusBadGateway, "upstream_error", "ChatGPT API error: "+err.Error())
		return chatResult{convID: st.ConvID, messageID: st.MessageID}
	}

	resp := buildNonStreamResponse(st, requestID)
	if resp.Model == "" {
		resp.Model = model
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
	return chatResult{convID: st.ConvID, messageID: st.MessageID}
}

func init() {
	jsonMarshal = json.Marshal
}

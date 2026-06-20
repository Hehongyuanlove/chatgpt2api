package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type SessionState int

const (
	StateActive SessionState = iota
	StateExpired
	StateCooldown
	StateBanned
)

type ManagedSession struct {
	Raw     *Session
	Client  *Client
	State   SessionState
	InFlight int64
	Sem     chan struct{}
	ProxyURL string
	mu       sync.Mutex

	lastUsed  time.Time
	lastError error
	failCount int

	bootstrapped bool
}

func (ms *ManagedSession) available() bool {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.State == StateActive || ms.State == StateExpired
}

type convBinding struct {
	ConversationID string    `json:"conversationID"`
	AccountID      string    `json:"accountID"`
	LastUsed       time.Time `json:"lastUsed"`
	ToolHash       string    `json:"toolHash"`
}

type convStateFile struct {
	ConvSessions map[string]*convBinding `json:"convSessions"`
}

type SessionPool struct {
	sessions []*ManagedSession
	mu       sync.RWMutex
	next     uint64
	cfg      *Config
	stopCh   chan struct{}
	closeOnce sync.Once
	wg       sync.WaitGroup

	convSessions    map[string]*convBinding        // sessionID → binding{conversationID, accountID, lastUsed, toolHash}
	accountSessions map[string]*ManagedSession     // accountID → ManagedSession
	statePath       string
}

func NewSessionPool(cfg *Config) *SessionPool {
	return &SessionPool{
		cfg:             cfg,
		stopCh:          make(chan struct{}),
		convSessions:    make(map[string]*convBinding),
		accountSessions: make(map[string]*ManagedSession),
		statePath:       filepath.Join(cfg.SessionDir, ".conv_state.json"),
	}
}

func (sp *SessionPool) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read session dir: %w", err)
	}
	fmt.Printf("FILE                      EMAIL                              REMAIN     EXPIRES                       PLAN\n")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := sp.LoadFile(path); err != nil {
			log.Printf("WARN: skip %s: %v", e.Name(), err)
			continue
		}
	}
	if len(sp.sessions) == 0 {
		return fmt.Errorf("no valid session files found in %s", dir)
	}
	fmt.Printf("Total: %d session(s)\n", len(sp.sessions))
	sp.loadConvState()
	return nil
}

func (sp *SessionPool) LoadFile(path string) error {
	sess, err := LoadSession(path)
	if err != nil {
		return err
	}

	email := ""
	if sess.User != nil {
		email = sess.User.Email
	}
	plan := ""
	if sess.Account != nil {
		plan = sess.Account.PlanType
	}
	remain := calcRemain(sess.Expires)
	fmt.Printf("%-25s %-35s %-10s %-28s %s\n",
		filepath.Base(path), email, remain, sess.Expires, plan)

	client, err := NewClient(sess, sp.cfg.Proxy)
	if err != nil {
		return fmt.Errorf("create client for %s: %w", path, err)
	}
	ms := &ManagedSession{
		Raw:     sess,
		Client:  client,
		State:   StateActive,
		Sem:     make(chan struct{}, sp.cfg.MaxConcurrency),
		ProxyURL: sp.cfg.Proxy,
		lastUsed: time.Now(),
	}
	sp.mu.Lock()
	sp.sessions = append(sp.sessions, ms)
	if sess.Account != nil && sess.Account.ID != "" {
		sp.accountSessions[sess.Account.ID] = ms
	}
	sp.mu.Unlock()
	return nil
}

func (sp *SessionPool) Start() {
	sp.wg.Add(2)
	go sp.refreshLoop()
	go sp.healthLoop()
}

func (sp *SessionPool) Stop() {
	sp.closeOnce.Do(func() {
		close(sp.stopCh)
	})
	sp.wg.Wait()
}

func (sp *SessionPool) refreshLoop() {
	defer sp.wg.Done()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-sp.stopCh:
			return
		case <-ticker.C:
			sp.refreshAll()
		}
	}
}

func (sp *SessionPool) healthLoop() {
	defer sp.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-sp.stopCh:
			return
		case <-ticker.C:
			sp.checkHealth()
		}
	}
}

func (sp *SessionPool) refreshAll() {
	sp.mu.RLock()
	sessions := make([]*ManagedSession, len(sp.sessions))
	copy(sessions, sp.sessions)
	sp.mu.RUnlock()

	for _, ms := range sessions {
		ms.mu.Lock()
		if ms.Raw.NeedsRefresh() && ms.Raw.SessionToken != "" {
			ms.mu.Unlock()
			log.Printf("Refreshing token for session...")
			if err := ms.Client.RefreshToken(); err != nil {
				log.Printf("Token refresh failed: %v", err)
				ms.mu.Lock()
				ms.lastError = err
				ms.failCount++
				ms.mu.Unlock()
			} else {
				ms.mu.Lock()
				ms.failCount = 0
				ms.State = StateActive
				ms.mu.Unlock()
				log.Printf("Token refreshed successfully")
			}
		} else {
			ms.mu.Unlock()
		}
	}
}

func (sp *SessionPool) checkHealth() {
	sp.mu.RLock()
	sessions := make([]*ManagedSession, len(sp.sessions))
	copy(sessions, sp.sessions)
	sp.mu.RUnlock()

	for _, ms := range sessions {
		ms.mu.Lock()
		if ms.State == StateCooldown {
			ms.State = StateActive
			ms.failCount = 0
			ms.mu.Unlock()
			continue
		}
		ms.mu.Unlock()
	}
}

func (sp *SessionPool) Acquire(ctx context.Context) (*ManagedSession, error) {
	for {
		sp.mu.RLock()
		candidates := make([]*ManagedSession, 0, len(sp.sessions))
		for _, ms := range sp.sessions {
			if ms.available() {
				candidates = append(candidates, ms)
			}
		}
		sp.mu.RUnlock()

		if len(candidates) == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		idx := atomic.AddUint64(&sp.next, 1) % uint64(len(candidates))
		ms := candidates[idx]

		select {
		case ms.Sem <- struct{}{}:
			atomic.AddInt64(&ms.InFlight, 1)
			ms.lastUsed = time.Now()
			ms.mu.Lock()
			if ms.Raw.NeedsRefresh() && ms.Raw.SessionToken != "" {
				log.Printf("Refreshing expired token before use...")
				if err := ms.Client.RefreshToken(); err != nil {
					ms.mu.Unlock()
					<-ms.Sem
					atomic.AddInt64(&ms.InFlight, -1)
					ms.mu.Lock()
					ms.lastError = err
					ms.failCount++
					if ms.failCount >= 3 {
						ms.State = StateCooldown
					}
					ms.mu.Unlock()
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(500 * time.Millisecond):
						continue
					}
				}
			}
			ms.mu.Unlock()
			return ms, nil
		default:
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				continue
			}
		}
	}
}

func (sp *SessionPool) Release(ms *ManagedSession) {
	<-ms.Sem
	atomic.AddInt64(&ms.InFlight, -1)
}

func (sp *SessionPool) AcquireSticky(ctx context.Context, sessionID string) (*ManagedSession, error) {
	if sessionID != "" {
		sp.mu.RLock()
		b, ok := sp.convSessions[sessionID]
		sp.mu.RUnlock()
		if ok && b.AccountID != "" {
			sp.mu.RLock()
			ms, hasSession := sp.accountSessions[b.AccountID]
			sp.mu.RUnlock()
			if !hasSession {
				sp.mu.Lock()
				delete(sp.convSessions, sessionID)
				sp.mu.Unlock()
				sp.saveConvState()
			} else if ms.available() {
				select {
				case ms.Sem <- struct{}{}:
					atomic.AddInt64(&ms.InFlight, 1)
					ms.lastUsed = time.Now()
					b.LastUsed = time.Now()
					return ms, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
	}
	return sp.Acquire(ctx)
}

func (sp *SessionPool) BindConversation(sessionID, convID string, ms *ManagedSession) {
	if sessionID == "" || convID == "" {
		return
	}
	accountID := ""
	if ms.Raw != nil && ms.Raw.Account != nil {
		accountID = ms.Raw.Account.ID
	}
	if accountID == "" {
		return
	}
	sp.mu.Lock()
	if b, ok := sp.convSessions[sessionID]; ok {
		b.ConversationID = convID
		b.AccountID = accountID
		b.LastUsed = time.Now()
	} else {
		sp.convSessions[sessionID] = &convBinding{
			ConversationID: convID,
			AccountID:      accountID,
			LastUsed:       time.Now(),
		}
	}
	sp.mu.Unlock()
	sp.saveConvState()
}

func (sp *SessionPool) saveConvState() {
	if sp.cfg.DisableConvState {
		return
	}
	sp.mu.RLock()
	convSessions := make(map[string]*convBinding, len(sp.convSessions))
	for convID, b := range sp.convSessions {
		convSessions[convID] = &convBinding{
			ConversationID: b.ConversationID,
			AccountID:      b.AccountID,
			LastUsed:       b.LastUsed,
			ToolHash:       b.ToolHash,
		}
	}
	sp.mu.RUnlock()

	data, err := json.MarshalIndent(convStateFile{
		ConvSessions: convSessions,
	}, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal conv state: %v", err)
		return
	}
	if err := os.WriteFile(sp.statePath, data, 0644); err != nil {
		log.Printf("WARN: write conv state: %v", err)
	}
}

func (sp *SessionPool) loadConvState() {
	if sp.cfg.DisableConvState {
		return
	}
	data, err := os.ReadFile(sp.statePath)
	if err != nil {
		return
	}
	var f convStateFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("WARN: parse conv state: %v", err)
		return
	}

	sp.mu.Lock()

	stale := 0
	for convID, b := range f.ConvSessions {
		if b == nil || b.AccountID == "" {
			stale++
			continue
		}
		if _, ok := sp.accountSessions[b.AccountID]; ok {
			sp.convSessions[convID] = b
		} else {
			stale++
		}
	}

	restored := len(sp.convSessions)
	sp.mu.Unlock()

	if restored > 0 {
		log.Printf("Restored %d conv mappings from state file", restored)
	}
	if stale > 0 {
		log.Printf("Skipped %d stale conv mappings (session file not loaded)", stale)
		if sp.cfg.CleanStaleConv {
			sp.saveConvState()
			log.Printf("Cleaned %d stale entries from state file", stale)
		}
	}
}

func (sp *SessionPool) GetBinding(sessionID string) *convBinding {
	if sessionID == "" {
		return nil
	}
	sp.mu.RLock()
	b, ok := sp.convSessions[sessionID]
	sp.mu.RUnlock()
	if ok {
		return b
	}
	return nil
}

func (sp *SessionPool) GetConvToolHash(convID string) string {
	if convID == "" {
		return ""
	}
	sp.mu.RLock()
	b, ok := sp.convSessions[convID]
	sp.mu.RUnlock()
	if ok {
		return b.ToolHash
	}
	return ""
}

func (sp *SessionPool) SetConvToolHash(convID, toolHash string) {
	if convID == "" {
		return
	}
	sp.mu.Lock()
	if b, ok := sp.convSessions[convID]; ok {
		b.ToolHash = toolHash
	}
	sp.mu.Unlock()
	sp.saveConvState()
}

func (ms *ManagedSession) MarkFailed(err error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.lastError = err
	ms.failCount++
	if ms.failCount >= 3 {
		ms.State = StateCooldown
	}
}

func (sp *SessionPool) Stats() map[string]interface{} {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	stats := map[string]interface{}{
		"total":    len(sp.sessions),
		"active":   0,
		"cooldown": 0,
		"expired":  0,
		"banned":   0,
	}
	for _, ms := range sp.sessions {
		switch ms.State {
		case StateActive:
			stats["active"] = stats["active"].(int) + 1
		case StateCooldown:
			stats["cooldown"] = stats["cooldown"].(int) + 1
		case StateExpired:
			stats["expired"] = stats["expired"].(int) + 1
		case StateBanned:
			stats["banned"] = stats["banned"].(int) + 1
		}
	}
	return stats
}

type deltaProcessor struct {
	messages       map[string]*SSEMessage
	convID         string
	events         []SSEEvent
	lastPath       string
	lastOp         string
	lastMsgID      string
}

func newDeltaProcessor() *deltaProcessor {
	return &deltaProcessor{messages: make(map[string]*SSEMessage)}
}

func (dp *deltaProcessor) feed(payload []byte) ([]SSEEvent, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, false
	}

	_, hasV := raw["v"]
	_, hasO := raw["o"]
	_, hasP := raw["p"]
	if !hasV && !hasO && !hasP {
		return nil, false // not delta format
	}

	dp.events = dp.events[:0]

	// Batch patch: {"o": "patch", "v": [{...}, ...]}
	if opRaw, ok := raw["o"]; ok {
		var opStr string
		if json.Unmarshal(opRaw, &opStr) == nil && opStr == "patch" {
			var batch struct {
				Patches []struct {
					Path string          `json:"p"`
					Op   string          `json:"o"`
					Val  json.RawMessage `json:"v"`
				} `json:"v"`
			}
			if json.Unmarshal(payload, &batch) == nil {
				for _, p := range batch.Patches {
					dp.lastPath = p.Path
					dp.lastOp = p.Op
					dp.applySingle(p.Path, p.Op, p.Val)
				}
			}
			dp.flushFinished()
			return dp.events, true
		}
	}

	// Full add / full delta: "v" contains SSEEvent-like structure
		if val, ok := raw["v"]; ok {
			var inner map[string]json.RawMessage
			if json.Unmarshal(val, &inner) == nil {
				if _, hasMsg := inner["message"]; hasMsg {
					var event SSEEvent
					if json.Unmarshal(val, &event) == nil {
						if event.ConversationID != "" {
							dp.convID = event.ConversationID
						}
						if event.Message != nil && event.Message.ID != "" {
							// Skip internal bio/commentary messages entirely
							if event.Message.Recipient == "bio" || event.Message.Channel == "commentary" {
								return dp.events, true
							}
							dp.messages[event.Message.ID] = event.Message
							dp.lastMsgID = event.Message.ID
						}
					}
					dp.flushFinished()
					return dp.events, true
				}
			}

		// "v" might be a simple value for a patch
		if pathStr, hasPath := raw["p"]; hasPath {
			var ps string
			json.Unmarshal(pathStr, &ps)
			op := opStr(raw)
			dp.lastPath = ps
			dp.lastOp = op
			dp.applySingle(ps, op, val)
			dp.flushFinished()
			return dp.events, true
		}

		// Bare value continuation: {"v": "..."} — continue last path/op
		if !hasO && !hasP && dp.lastPath != "" {
			dp.applySingle(dp.lastPath, dp.lastOp, val)
			dp.flushFinished()
			return dp.events, true
		}
	}

	// Single patch with "p" and "o"
	if pathStr, hasP := raw["p"]; hasP {
		var ps string
		json.Unmarshal(pathStr, &ps)
		op := opStr(raw)
		dp.lastPath = ps
		dp.lastOp = op
		var val json.RawMessage
		if v, ok := raw["v"]; ok {
			val = v
		}
		dp.applySingle(ps, op, val)
		dp.flushFinished()
		return dp.events, true
	}

	return dp.events, true
}

func opStr(raw map[string]json.RawMessage) string {
	if o, ok := raw["o"]; ok {
		var s string
		json.Unmarshal(o, &s)
		return s
	}
	return ""
}

func (dp *deltaProcessor) applySingle(path, op string, val json.RawMessage) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "message" {
		return
	}

	if dp.lastMsgID == "" {
		return
	}
	msg, ok := dp.messages[dp.lastMsgID]
	if !ok {
		return
	}

	switch {
	case len(parts) == 2 && parts[1] == "status":
		var s string
		if json.Unmarshal(val, &s) == nil {
			msg.Status = s
		}

	case len(parts) == 2 && parts[1] == "end_turn":
		var et interface{}
		if json.Unmarshal(val, &et) == nil {
			msg.EndTurn = et
		}

	case len(parts) == 2 && parts[1] == "recipient":
		var r string
		if json.Unmarshal(val, &r) == nil {
			msg.Recipient = r
		}

	case len(parts) >= 4 && parts[1] == "content" && parts[2] == "parts" && parts[3] == "0":
		if msg.Content == nil {
			msg.Content = &SSEContent{ContentType: "text"}
		}
		if op == "append" {
			var s string
			if json.Unmarshal(val, &s) == nil {
				if len(msg.Content.Parts) == 0 {
					msg.Content.Parts = []string{s}
				} else {
					msg.Content.Parts[0] += s
				}
			}
		} else if op == "replace" {
			var s string
			if json.Unmarshal(val, &s) == nil {
				msg.Content.Parts = []string{s}
			}
		}

	case len(parts) == 3 && parts[1] == "metadata":
		var meta map[string]interface{}
		if json.Unmarshal(val, &meta) == nil {
			if msg.Metadata == nil {
				msg.Metadata = meta
			} else {
				for k, v := range meta {
					msg.Metadata[k] = v
				}
			}
		}
	}
}

func (dp *deltaProcessor) flushFinished() {
	for id, msg := range dp.messages {
		if msg.Status == "finished_successfully" {
			if msg.Recipient == "bio" || msg.Channel == "commentary" {
				delete(dp.messages, id)
				continue
			}
			event := SSEEvent{
				ConversationID: dp.convID,
				Message:        msg,
			}
			dp.events = append(dp.events, event)
			delete(dp.messages, id)
		}
	}
}

func (ms *ManagedSession) SendStream(
	ctx context.Context,
	msg string,
	convID, parentMsgID string,
	onEvent func(SSEEvent) error,
	logFn func(string, ...interface{}),
) error {
	if logFn == nil {
		logFn = log.Printf
	}

	if err := ms.Client.ensureToken(); err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}

	if !ms.bootstrapped {
		log.Println("Bootstrapping session...")
		if err := ms.Client.Bootstrap(); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		ms.bootstrapped = true
	}

	req, err := ms.Client.GetChatRequirements()
	if err != nil {
		log.Printf("SendStream: GetChatRequirements failed: %v, retrying with re-bootstrap", err)
		if err2 := ms.Client.Bootstrap(); err2 != nil {
			return fmt.Errorf("chat requirements after retry bootstrap: %w (original err: %v)", err2, err)
		}
		req, err = ms.Client.GetChatRequirements()
		if err != nil {
			return fmt.Errorf("chat requirements: %w (after retry)", err)
		}
	}

	path := "/backend-api/conversation"
	if tok := ms.Raw.Token(); tok == "" {
		path = "/backend-anon/conversation"
	}

	payload := ms.Client.conversationPayload(msg, convID, parentMsgID)
	bodyJSON, _ := json.Marshal(payload)
	logFn("[STAGE2] sent to ChatGPT: %s", string(bodyJSON))

	httpReq, err := ms.Client.buildReq("POST", "https://chatgpt.com"+path,
		bytes.NewReader(bodyJSON),
		ms.Client.conversationHeaders(path, req))
	if err != nil {
		return fmt.Errorf("build req: %w", err)
	}

	resp, err := ms.Client.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("conversation req: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("conversation failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	deltaEncoded := false
	dp := newDeltaProcessor()

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			// Check for delta_encoding header
			if strings.HasPrefix(line, "event: delta_encoding") {
				deltaEncoded = true
			}
			continue
		}
		pl := strings.TrimSpace(line[5:])
		if pl == "" {
			continue
		}
		if pl == "[DONE]" {
			logFn("[STAGE3] received: [DONE]")
			break
		}

		logFn("[STAGE3] received raw: %s", pl)

		if deltaEncoded {
			events, handled := dp.feed([]byte(pl))
			if handled {
				for _, event := range events {
					if err := onEvent(event); err != nil {
						return err
					}
				}
				continue
			}
			// Not delta format — fall through to flat SSEEvent
		}

		// Flat SSEEvent format (type, input_message, server_ste_metadata, etc.)
		var event SSEEvent
		if err := json.Unmarshal([]byte(pl), &event); err != nil {
			continue
		}

		if err := onEvent(event); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func calcRemain(expires string) string {
	if expires == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return "???"
	}
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

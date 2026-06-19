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

type chatgptConvState struct {
	ConvID      string `json:"convID"`
	ParentMsgID string `json:"parentMsgID"`
	ToolDesc    string `json:"toolDesc"`
}

type convBinding struct {
	AccountID string    `json:"accountID"`
	LastUsed  time.Time `json:"lastUsed"`
}

type convStateFile struct {
	ConvSessions   map[string]*convBinding        `json:"convSessions"`
	OpenCodeStates map[string]*chatgptConvState   `json:"opencodeStates"`
}

type SessionPool struct {
	sessions []*ManagedSession
	mu       sync.RWMutex
	next     uint64
	cfg      *Config
	stopCh   chan struct{}
	closeOnce sync.Once
	wg       sync.WaitGroup

	convSessions    map[string]*convBinding        // convID → binding{accountID, lastUsed}
	accountSessions map[string]*ManagedSession     // accountID → ManagedSession
	opencodeStates  map[string]*chatgptConvState
	statePath       string
}

func NewSessionPool(cfg *Config) *SessionPool {
	return &SessionPool{
		cfg:             cfg,
		stopCh:          make(chan struct{}),
		convSessions:    make(map[string]*convBinding),
		accountSessions: make(map[string]*ManagedSession),
		opencodeStates:  make(map[string]*chatgptConvState),
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

func (sp *SessionPool) AcquireSticky(ctx context.Context, convID string) (*ManagedSession, error) {
	if convID != "" {
		sp.mu.RLock()
		b, ok := sp.convSessions[convID]
		sp.mu.RUnlock()
		if ok {
			sp.mu.RLock()
			ms, hasSession := sp.accountSessions[b.AccountID]
			sp.mu.RUnlock()
			if !hasSession {
				sp.mu.Lock()
				delete(sp.convSessions, convID)
				for sid, st := range sp.opencodeStates {
					if st.ConvID == convID {
						delete(sp.opencodeStates, sid)
					}
				}
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

func (sp *SessionPool) BindConversation(convID string, ms *ManagedSession) {
	if convID == "" {
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
	sp.convSessions[convID] = &convBinding{AccountID: accountID, LastUsed: time.Now()}
	sp.mu.Unlock()
	sp.saveConvState()
}

func (sp *SessionPool) saveConvState() {
	sp.mu.RLock()
	convSessions := make(map[string]*convBinding, len(sp.convSessions))
	for convID, b := range sp.convSessions {
		convSessions[convID] = &convBinding{AccountID: b.AccountID, LastUsed: b.LastUsed}
	}
	opencodeStates := make(map[string]*chatgptConvState, len(sp.opencodeStates))
	for k, v := range sp.opencodeStates {
		cp := *v
		opencodeStates[k] = &cp
	}
	sp.mu.RUnlock()

	data, err := json.MarshalIndent(convStateFile{
		ConvSessions:   convSessions,
		OpenCodeStates: opencodeStates,
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

	for sessID, st := range f.OpenCodeStates {
		if _, ok := sp.convSessions[st.ConvID]; ok {
			sp.opencodeStates[sessID] = st
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

func (sp *SessionPool) GetConvState(opencodeSessionID string) (string, string, string) {
	if opencodeSessionID == "" {
		return "", "", ""
	}
	sp.mu.RLock()
	s, ok := sp.opencodeStates[opencodeSessionID]
	sp.mu.RUnlock()
	if ok {
		return s.ConvID, s.ParentMsgID, s.ToolDesc
	}
	return "", "", ""
}

func (sp *SessionPool) SetConvState(opencodeSessionID, convID, parentMsgID, toolDesc string) {
	if opencodeSessionID == "" || convID == "" {
		return
	}
	sp.mu.Lock()
	sp.opencodeStates[opencodeSessionID] = &chatgptConvState{ConvID: convID, ParentMsgID: parentMsgID, ToolDesc: toolDesc}
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

func (ms *ManagedSession) SendStream(
	ctx context.Context,
	msg string,
	convID, parentMsgID string,
	onEvent func(SSEEvent) error,
) error {
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
		return fmt.Errorf("chat requirements: %w", err)
	}

	path := "/backend-api/conversation"
	if tok := ms.Raw.Token(); tok == "" {
		path = "/backend-anon/conversation"
	}

	payload := ms.Client.conversationPayload(msg, convID, parentMsgID)
	bodyJSON, _ := json.Marshal(payload)

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

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}

		var event SSEEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
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

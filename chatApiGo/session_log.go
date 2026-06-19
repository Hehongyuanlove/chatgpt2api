package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type sessionLog struct {
	mu     sync.Mutex
	buf    []byte
	id     string
	closed bool
}

func newSessionLog(requestID string) *sessionLog {
	now := time.Now()
	sl := &sessionLog{id: requestID}
	sl.buf = append(sl.buf, fmt.Sprintf("=== Request %s | %s ===\n", requestID, now.Format(time.RFC3339))...)
	return sl
}

func (sl *sessionLog) Printf(format string, args ...interface{}) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.closed {
		return
	}
	s := fmt.Sprintf(format, args...)
	sl.buf = append(sl.buf, s...)
	if len(s) == 0 || s[len(s)-1] != '\n' {
		sl.buf = append(sl.buf, '\n')
	}
}

func (sl *sessionLog) Close() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if sl.closed {
		return nil
	}
	sl.closed = true

	dir := "session_logs"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	filename := filepath.Join(dir, fmt.Sprintf("%s_%s.log",
		time.Now().Format("20060102_150405"),
		sl.id))
	return os.WriteFile(filename, sl.buf, 0644)
}

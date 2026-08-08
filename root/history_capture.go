package root

import (
	"fmt"
	"sync"
	"time"

	"github.com/faustbrian/vuja/integration"
)

type commandExecutionTracker struct {
	mu        sync.Mutex
	now       func() time.Time
	sessionID string
	host      string
	startedAt time.Time
	sequence  uint64
}

func newCommandExecutionTracker(now func() time.Time, sessionID, host string) *commandExecutionTracker {
	if now == nil {
		now = time.Now
	}
	return &commandExecutionTracker{now: now, sessionID: sessionID, host: host}
}

func (t *commandExecutionTracker) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startedAt = t.now()
}

func (t *commandExecutionTracker) Finish(command, cwd string, exitCode int) integration.HistoryEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	finishedAt := t.now()
	startedAt := t.startedAt
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	t.startedAt = time.Time{}
	t.sequence++
	return integration.HistoryEntry{
		ID:          fmt.Sprintf("vuja:%s:%d:%d", t.sessionID, startedAt.UnixNano(), t.sequence),
		Command:     command,
		Cwd:         cwd,
		StartedAt:   startedAt,
		Duration:    max(finishedAt.Sub(startedAt), 0),
		ExitCode:    exitCode,
		HasExitCode: true,
		Source:      "vuja",
		Host:        t.host,
		SessionID:   t.sessionID,
	}
}

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
	active    integration.HistoryEntry
}

func newCommandExecutionTracker(now func() time.Time, sessionID, host string) *commandExecutionTracker {
	if now == nil {
		now = time.Now
	}
	return &commandExecutionTracker{now: now, sessionID: sessionID, host: host}
}

func (t *commandExecutionTracker) Start(command, cwd, shell string) integration.HistoryEntry {
	return t.StartRaw(command, command, cwd, shell)
}

func (t *commandExecutionTracker) StartRaw(command, normalizedCommand, cwd, shell string) integration.HistoryEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.startedAt = t.now()
	t.sequence++
	t.active = integration.HistoryEntry{
		ID:                fmt.Sprintf("vuja:%s:%d:%d", t.sessionID, t.startedAt.UnixNano(), t.sequence),
		Command:           command,
		NormalizedCommand: normalizedCommand,
		Cwd:               cwd,
		SubmittedAt:       t.startedAt,
		StartedAt:         t.startedAt,
		Source:            "vuja",
		Host:              t.host,
		SessionID:         t.sessionID,
		Shell:             shell,
		State:             integration.HistoryStateRunning,
	}
	return t.active
}

func (t *commandExecutionTracker) Finish(exitCode int) integration.HistoryEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	finishedAt := t.now()
	startedAt := t.startedAt
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	t.startedAt = time.Time{}
	entry := t.active
	if entry.ID == "" {
		t.sequence++
		entry = integration.HistoryEntry{
			ID:        fmt.Sprintf("vuja:%s:%d:%d", t.sessionID, startedAt.UnixNano(), t.sequence),
			StartedAt: startedAt, Source: "vuja", Host: t.host, SessionID: t.sessionID,
		}
	}
	entry.CompletedAt = finishedAt
	entry.Duration = max(finishedAt.Sub(startedAt), 0)
	entry.ExitCode = exitCode
	entry.HasExitCode = true
	entry.State = integration.HistoryStateCompleted
	if exitCode != 0 {
		entry.State = integration.HistoryStateFailed
	}
	t.active = integration.HistoryEntry{}
	return entry
}

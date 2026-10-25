package root

import (
	"io"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
	"github.com/faustbrian/vuja/v2/integration"
	"github.com/faustbrian/vuja/v2/internal/scoring"
)

type historyRecordingSession struct {
	paused atomic.Bool
}

type historySubmissionAdmission struct {
	paused bool
}

// Preserve a display-only warning when ordinary chrome cannot fit. It owns
// one row, not shell input bytes; narrow panes use an abbreviated label.
func (c *terminalCompositor) compactHistoryPauseWarning() bool {
	return c.historyRecordingPaused && (c.inputBoxTheme.SurfaceBackground == "" || c.width < 12 || c.height < 5)
}

func (c *terminalCompositor) historyPauseWarningLine() string {
	return historyPauseWarningLabel(c.width)
}

func historyPauseWarningLabel(width int) string {
	label := "History paused"
	if width < ansi.StringWidth(label) {
		label = "PAUSED"
	}
	return ansi.Cut(label, 0, max(width, 1))
}

func (c *terminalCompositor) appendHistoryRecordingWarning(segments []terminalStatusSegment) []terminalStatusSegment {
	if c.historyRecordingPaused {
		label := historyPauseWarningLabel(c.width - 2*terminalInputHorizontalPadding)
		segments = append(segments, terminalStatusSegment{name: "session-warning", text: label, priority: 130, alignment: terminalStatusLeft})
	}
	return segments
}

func (s *historyRecordingSession) toggle() bool {
	for {
		old := s.paused.Load()
		if s.paused.CompareAndSwap(old, !old) {
			return !old
		}
	}
}

func (s *historyRecordingSession) capture() historySubmissionAdmission {
	// Capture on Enter, not on the later shell start/completion messages.
	return historySubmissionAdmission{paused: s.paused.Load()}
}

func (a historySubmissionAdmission) command(raw string, shellIgnored bool) (string, bool) {
	if a.paused {
		return "", false
	}
	return historyRecordableCommand(raw, shellIgnored)
}

func (a historySubmissionAdmission) persist(store *scoring.FrecencyStore, entry integration.HistoryEntry, ignored bool, observe func(bool, bool)) (bool, bool) {
	if _, allowed := a.command(entry.Command, ignored); !allowed {
		return false, false
	}
	return persistHistorySubmissionDurablyObserved(store, entry, observe)
}

func handleHistoryRecordingControl(data []byte, index int, bindings inputKeybindings, session *historyRecordingSession, shellInput, pasting bool) int {
	if !shellInput || pasting || session == nil {
		return 0
	}
	if length := bindings.match(inputHistoryPause, data, index); length > 0 {
		session.toggle()
		return length
	}
	return 0
}

func (c *terminalCompositor) SetHistoryRecordingPaused(paused bool) {
	defer c.containVisualFailure()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.historyRecordingPaused == paused {
		return
	}
	c.historyRecordingPaused = paused
	c.titleLinesCache = nil
	c.statusLinesCache = nil
	if c.layout && c.phase == terminalInput {
		c.renderPinned()
	}
}

func handleHistoryRecordingMessage(message string, session *historyRecordingSession, reply io.Writer) (bool, error) {
	var paused bool
	switch message {
	case "VUJA_HISTORY_RECORDING_PAUSE":
		paused = true
	case "VUJA_HISTORY_RECORDING_RESUME":
	default:
		return false, nil
	}
	// The shell owns when its prompt can be redrawn. Use the existing private
	// IPC/ack pair; this does not submit text, create files, or touch history.
	session.paused.Store(paused)
	value := byte('0')
	if paused {
		value = '1'
	}
	n, err := reply.Write([]byte{value})
	if err == nil && n != 1 {
		err = io.ErrShortWrite
	}
	return true, err
}

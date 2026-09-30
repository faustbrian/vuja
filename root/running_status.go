package root

import (
	"fmt"
	"strings"
	"time"
)

// Zero disables the timer when no command is active. The next deadline is
// relative to command start, never the lifetime of the wrapper process.
func (c *terminalCompositor) NextRunningStatusDelay() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.commandStartedAt.IsZero() {
		return 0
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	elapsed := max(now().Sub(c.commandStartedAt), 0)
	return time.Second - elapsed%time.Second
}

// RefreshRunningStatus is driven by the wrapper's input loop, independently of
// PTY output and optional CPU/memory sampling. It never owns a worker or timer.
func (c *terminalCompositor) RefreshRunningStatus() {
	defer c.containVisualFailure()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.commandStartedAt.IsZero() || c.phase != terminalOutput {
		return
	}
	c.renderCommandStatus()
}

func (c *terminalCompositor) commandStatusLines() (int, []string) {
	if !c.outputViewportEnabled() || !c.viewportCommand || c.surfaceStatusRows <= 0 || c.surfaceStatusRows > len(c.renderedLines) {
		return 0, nil
	}
	bar := c.chatboxConfig.Status
	segments := c.inputBoxBarSegments(bar)
	if !c.commandStartedAt.IsZero() {
		bar = filterChatboxBar(bar, func(name string) bool { return !isCommandOutcomeStatus(name) })
		segments = c.inputBoxBarSegmentsForSnapshot(bar, c.commandStatusSnapshot)
		now := time.Now
		if c.now != nil {
			now = c.now
		}
		seconds := int64(max(now().Sub(c.commandStartedAt), 0) / time.Second)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		segments = append(segments, terminalStatusSegment{name: "running", text: fmt.Sprintf("%s Running · %ds", frames[seconds%int64(len(frames))], seconds), priority: 110, alignment: terminalStatusRight})
	}
	lines := c.renderBarLinesWithBackground(segments, c.surfaceStatusRows, terminalDefaultBackground)
	for len(lines) < c.surfaceStatusRows {
		lines = append(lines, c.renderBarLinesWithBackground(nil, 1, terminalDefaultBackground)[0])
	}
	return len(c.renderedLines) - c.surfaceStatusRows, lines
}

// Only the reserved status rows are repainted. Silent ticks must not redraw
// scrollback, disturb a frozen reading position, or repaint a native app.
func (c *terminalCompositor) renderCommandStatus() {
	start, lines := c.commandStatusLines()
	var frame strings.Builder
	for i, line := range lines {
		if c.renderedLines[start+i] == line {
			continue
		}
		if frame.Len() == 0 {
			frame.WriteString(terminalSyncStart + "\x1b7\x1b[?7l")
		}
		c.renderedLines[start+i] = line
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", c.height-c.surfaceRows+start+i+1, line)
	}
	if frame.Len() > 0 {
		frame.WriteString("\x1b8\x1b[?7h" + terminalSyncEnd)
		data := []byte(frame.String())
		_, _ = c.out.Write(data)
		c.recordBackdrop(data)
	}
}

package root

import (
	"github.com/charmbracelet/x/ansi"
)

// Clear utilities use home plus ED 2, optionally ED 3 (scrollback). Other
// cursor/mode controls still belong to native applications, never capture.
func viewportClearControl(cmd ansi.Cmd, params ansi.Params) (mode int, allowed bool) {
	if cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
		return 0, false
	}
	for _, p := range params {
		if p.HasMore() {
			return 0, false
		}
	}
	switch cmd.Final() {
	case 'H':
		if len(params) > 2 {
			return 0, false
		}
		for _, p := range params {
			if p.Param(1) > 1 {
				return 0, false
			}
		}
		return 0, true
	case 'J':
		if len(params) == 1 {
			n := params[0].Param(0)
			return n, n == 2 || n == 3
		}
	case 'm':
		return 0, true
	}
	return 0, false
}

// Prompt redraws are shell-owned, unlike command output. Extract their erase
// mode from parsed CSI tokens rather than interpreting arbitrary parameters.
func promptViewportClearMode(data []byte) int {
	mode := 0
	p := ansi.NewParser()
	p.SetDataSize(terminalModelSequenceLimit)
	p.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd.Final() == 'J' {
			if n, ok := viewportClearControl(cmd, params); ok {
				mode = max(mode, n)
			}
		}
	}})
	for _, b := range data {
		p.Advance(b)
	}
	return mode
}

func (c *terminalCompositor) clearOutputViewport(mode int) {
	v := c.viewport
	if v == nil {
		return
	}
	controls := "\x1b[H\x1b[2J"
	if mode == 3 {
		controls += "\x1b[3J"
	}
	v.write([]byte(controls))
	v.setFrozen(nil)
	v.cards.releasePointer()
	v.cards.focus, v.cards.focused = nil, nil
	v.cards.history, v.cards.total = 0, 0
	v.cards.browsing = false
	v.cards.clearAt = v.cursorLogicalRow()
	v.cards.pending = nil
	if mode == 3 {
		v.blocks = nil
		v.cards.completed = nil
		v.cards.offsets = nil
		v.cards.layouts = nil
		v.cards.document = nil
		if v.blockActive {
			// ED3 discards earlier command rows, not its pending outcome.
			v.blockStart, v.blockEnd = v.cursorLogicalRow(), 0
		}
	} else {
		// ED2 retains the previous canvas in scrollback; its presentation
		// boundaries have the same lifetime as that retained text.
		v.pruneBlockStates()
	}
	v.search = nil
	v.executions.selected = nil
	v.executions.notice = ""
	v.top, v.newOutput, v.dirty = 0, false, true
	if mode == 3 {
		v.restoredTruncated = false
	}
	if c.outputRecovery != nil {
		c.outputRecovery.dirty = true
	}
}

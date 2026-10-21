package root

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	ansiparser "github.com/charmbracelet/x/ansi/parser"
)

// Once a cursor-driven application takes the display, its canvas is not a
// transcript. Keep the retained model at the last Vuja-owned text instead of
// serializing the application's primary or alternate screen at completion.
// The SDK parser owns fragmented controls; only an observed alternate-screen
// exit admits subsequent line-oriented text again. Primary-screen applications
// have no equivalent text boundary, except the existing finite Codex resume
// recognizer, whose result is a session ID, never arbitrary screen contents.
type nativeOutputTail struct {
	parser    *ansi.Parser
	accepting bool
	alternate bool
	resumed   bool
	output    strings.Builder
	resume    *codexResumeLinkifier
	resumeIDs map[string]bool
	clearOnly bool
	clearMode int
}

func newNativeOutputTail(linkFor func(string) string) *nativeOutputTail {
	s := &nativeOutputTail{accepting: true, clearOnly: true}
	p := ansi.NewParser()
	p.SetDataSize(4096)
	p.SetHandler(ansi.Handler{
		Print: func(r rune) {
			s.clearOnly = false
			if s.accepting {
				s.output.WriteRune(r)
			}
		},
		Execute: func(b byte) {
			s.clearOnly = false
			if s.accepting && (b == '\r' || b == '\n' || b == '\t' || b == '\b') {
				s.output.WriteByte(b)
			}
		},
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			if mode, allowed := viewportClearControl(cmd, params); allowed {
				s.clearMode = max(s.clearMode, mode)
			} else {
				s.clearOnly = false
			}
			s.csi(cmd, params)
		},
		HandleEsc: func(cmd ansi.Cmd) {
			s.clearOnly = false
			if cmd.Final() != '\\' { // ST ends an OSC, not text ownership.
				s.accepting = false
			}
		},
		HandleOsc: func(cmd int, data []byte) {
			s.clearOnly = false
			if s.accepting && cmd == 8 {
				s.output.WriteString("\x1b]")
				s.output.Write(data)
				s.output.WriteString("\x1b\\")
			}
		},
		HandleDcs: func(ansi.Cmd, ansi.Params, []byte) { s.accepting, s.clearOnly = false, false },
		HandlePm:  func([]byte) { s.accepting, s.clearOnly = false, false },
		HandleApc: func([]byte) { s.accepting, s.clearOnly = false, false },
		HandleSos: func([]byte) { s.accepting, s.clearOnly = false, false },
	})
	s.parser = p
	s.resume = newCodexResumeLinkifier(func(id string) string {
		id = strings.ToLower(id)
		if !s.accepting && isCodexResumeID(id) && !s.resumeIDs[id] && len(s.resumeIDs) < 64 {
			if s.resumeIDs == nil {
				s.resumeIDs = make(map[string]bool)
			}
			s.resumeIDs[id] = true
			// Live retained rows need the same owner capability as direct output.
			// Checkpoint serialization strips it; without an owner the bare target
			// is metadata for recovery only and is suppressed by the renderer.
			target := "vuja://codex-resume/" + id
			if linkFor != nil {
				target = linkFor(id)
			}
			fmt.Fprintf(&s.output, "\r\nTo continue this session, run codex resume \x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\\r\n", target, id)
		}
		return "" // Observation only: no terminal action or execution.
	})
	return s
}

func (s *nativeOutputTail) csi(cmd ansi.Cmd, params ansi.Params) {
	if cmd.Prefix() == '?' && cmd.Intermediate() == 0 && (cmd.Final() == 'h' || cmd.Final() == 'l') {
		for _, param := range params {
			if param.HasMore() {
				s.accepting = false
				return
			}
			switch param.Param(0) {
			case 47, 1047, 1049:
				if cmd.Final() == 'h' {
					s.alternate, s.accepting, s.resumed = true, false, false
				} else if s.alternate {
					s.alternate, s.accepting, s.resumed = false, true, true
				}
			case 25, 1000, 1002, 1003, 1006, 2004, 2026:
				// Cursor visibility, pointer reporting, paste and synchronized
				// updates do not turn an exit summary back into a canvas.
				if !s.resumed {
					s.accepting = false
				}
			default:
				s.accepting = false
			}
		}
		return
	}
	if cmd.Prefix() == 0 && cmd.Intermediate() == 0 && (cmd.Final() == 'm' || cmd.Final() == 'K') {
		if s.accepting {
			// Reconstruct parsed formatting, never replay a partial sequence.
			s.output.WriteString("\x1b[")
			for i, param := range params {
				if i > 0 {
					if params[i-1].HasMore() {
						s.output.WriteByte(':')
					} else {
						s.output.WriteByte(';')
					}
				}
				if value := param.Param(-1); value >= 0 {
					fmt.Fprint(&s.output, value)
				}
			}
			s.output.WriteByte(cmd.Final())
		}
		return
	}
	s.accepting = false
}

func (s *nativeOutputTail) write(data []byte) []byte {
	for _, b := range data {
		s.parser.Advance(b)
	}
	_ = s.resume.Transform(data)
	return s.take()
}

func (s *nativeOutputTail) finish() []byte {
	// A recognised final instruction may be unterminated. Flush only its
	// bounded recognizer; arbitrary native bytes never enter retained output.
	_ = s.resume.linkLine(s.resume.Flush())
	return s.take()
}

func (s *nativeOutputTail) take() []byte {
	data := []byte(s.output.String())
	s.output.Reset()
	return data
}

func (c *terminalCompositor) recordNativeOutput(data []byte) {
	if v := c.viewport; v != nil && v.native != nil {
		if retained := v.native.write(data); len(retained) > 0 {
			c.recordViewportOutput(retained)
		}
	}
}

func (c *terminalCompositor) finishNativeOutput() {
	if v := c.viewport; v != nil && v.native != nil {
		// A primary-screen application can begin with exactly clear's home/ED
		// bytes. Without explicit shell intent, only classify a control-only
		// command after completion. Zsh's external-clear marker also admits its
		// PROMPT_SP/precmd decoration, without retaining arbitrary canvas text.
		if (v.native.clearOnly || c.commandClearIntent) && v.native.clearMode > 0 && v.native.parser.State() == ansiparser.GroundState {
			c.clearOutputViewport(v.native.clearMode)
		}
		if retained := v.native.finish(); len(retained) > 0 {
			c.recordViewportOutput(retained)
		}
		v.native = nil
	}
}

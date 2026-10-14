package root

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
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
}

func newNativeOutputTail() *nativeOutputTail {
	s := &nativeOutputTail{accepting: true}
	p := ansi.NewParser()
	p.SetDataSize(4096)
	p.SetHandler(ansi.Handler{
		Print: func(r rune) {
			if s.accepting {
				s.output.WriteRune(r)
			}
		},
		Execute: func(b byte) {
			if s.accepting && (b == '\r' || b == '\n' || b == '\t' || b == '\b') {
				s.output.WriteByte(b)
			}
		},
		HandleCsi: s.csi,
		HandleEsc: func(cmd ansi.Cmd) {
			if cmd.Final() != '\\' { // ST ends an OSC, not text ownership.
				s.accepting = false
			}
		},
		HandleOsc: func(cmd int, data []byte) {
			if s.accepting && cmd == 8 {
				s.output.WriteString("\x1b]")
				s.output.Write(data)
				s.output.WriteString("\x1b\\")
			}
		},
		HandleDcs: func(ansi.Cmd, ansi.Params, []byte) { s.accepting = false },
		HandlePm:  func([]byte) { s.accepting = false },
		HandleApc: func([]byte) { s.accepting = false },
		HandleSos: func([]byte) { s.accepting = false },
	})
	s.parser = p
	s.resume = newCodexResumeLinkifier(func(id string) string {
		id = strings.ToLower(id)
		if !s.accepting && isCodexResumeID(id) && !s.resumeIDs[id] && len(s.resumeIDs) < 64 {
			if s.resumeIDs == nil {
				s.resumeIDs = make(map[string]bool)
			}
			s.resumeIDs[id] = true
			// Persist only the recognised metadata and a credential-free link.
			// Restore obtains a fresh action from the current session's owner.
			fmt.Fprintf(&s.output, "\r\nTo continue this session, run codex resume \x1b]8;;vuja://codex-resume/%s\x1b\\%s\x1b]8;;\x1b\\\r\n", id, id)
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
		if retained := v.native.finish(); len(retained) > 0 {
			c.recordViewportOutput(retained)
		}
		v.native = nil
	}
}

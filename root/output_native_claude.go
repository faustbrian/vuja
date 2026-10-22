package root

import (
	"strings"
	"unicode/utf8"
)

const claudeResumeHeader = "Resume this session with:"

// Claude 2.1.304's terminal exit formatter (qYs/resumeHintPrinted) emits this
// header and a UUID-form command, optionally with quoted worktree flags.
// Observe only that finite metadata: primary-screen canvas text must not be
// reclassified as a transcript merely because the next shell prompt is ready.
func (s *nativeOutputTail) observeClaudeRune(r rune) {
	if s.accepting || s.alternate {
		s.resetClaudeLine()
		return
	}
	if s.claudeOverflow {
		return
	}
	if len(s.claudeLine)+utf8.RuneLen(r) > codexResumeLineLimit {
		s.claudeLine, s.claudeHeader, s.claudeOverflow = nil, false, true
		return
	}
	s.claudeLine = utf8.AppendRune(s.claudeLine, r)
}

func (s *nativeOutputTail) observeClaudeControl(b byte) {
	switch b {
	case '\n':
		s.finishClaudeLine()
	case '\r', '\t':
		s.observeClaudeRune(rune(b))
	default:
		s.resetClaudeLine()
	}
}

func (s *nativeOutputTail) resetClaudeLine() {
	s.claudeLine, s.claudeHeader, s.claudeOverflow = nil, false, false
}

func (s *nativeOutputTail) finishClaudeLine() {
	line, header := strings.TrimSpace(string(s.claudeLine)), s.claudeHeader
	overflow := s.claudeOverflow
	s.resetClaudeLine()
	if s.accepting || s.alternate || overflow {
		return
	}
	if line == claudeResumeHeader {
		s.claudeHeader = true
		return
	}
	if !header || !isClaudeResumeCommand(line) {
		return
	}
	// Share the existing native metadata budget across providers; duplicate
	// notices and repeated redraws cannot grow a second retention registry.
	key := "claude:" + line
	if s.resumeIDs[key] || len(s.resumeIDs) >= 64 {
		return
	}
	if s.resumeIDs == nil {
		s.resumeIDs = make(map[string]bool)
	}
	s.resumeIDs[key] = true
	s.output.WriteString("\r\n" + claudeResumeHeader + "\r\n" + line + "\r\n")
}

func isClaudeResumeCommand(line string) bool {
	command, id, ok := strings.Cut(line, " --resume ")
	if !ok {
		command, id, ok = strings.Cut(line, " -r ")
	}
	if !ok || !isCodexResumeID(id) { // Both CLI session IDs use UUID syntax.
		return false
	}
	if command == "claude" {
		return true
	}
	worktree, ok := strings.CutPrefix(command, "claude --worktree \"")
	if !ok {
		worktree, ok = strings.CutPrefix(command, "claude -w \"")
	}
	if !ok || !strings.HasSuffix(worktree, "\"") {
		return false
	}
	name := strings.TrimSuffix(worktree, "\"")
	// Match Claude 2.1.304 qYs/Kjr, not arbitrary quoted canvas content:
	// its formatter rejects long, option-like, unsafe and reserved names.
	if name == "" || len(name) > 64 || strings.HasPrefix(name, "-") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.TrimRight(strings.ToLower(segment), ".") == ".git" {
			return false
		}
		for _, b := range []byte(segment) {
			if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-') {
				return false
			}
		}
	}
	return true
}

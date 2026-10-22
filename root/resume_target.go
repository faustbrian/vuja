package root

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The observed command, not a URL-supplied executable, is the action identity.
// Keep providers/worktrees distinct even when their session UUIDs coincide.
type resumeTarget struct {
	id, command string
	claude      bool
}

func parseResumeTarget(value string) (resumeTarget, bool) {
	if isCodexResumeID(value) {
		return resumeTarget{id: strings.ToLower(value), command: codexResumeCommand(value)}, true
	}
	if !isClaudeResumeCommand(value) {
		return resumeTarget{}, false
	}
	words := strings.Fields(value) // Accepted worktree names contain no whitespace.
	return resumeTarget{id: strings.ToLower(words[len(words)-1]), command: value, claude: true}, true
}

func (t resumeTarget) key() string {
	if t.claude {
		return "claude:" + t.command
	}
	return t.id
}

func (t resumeTarget) value() string {
	if t.claude {
		return t.command
	}
	return t.id
}

func (t resumeTarget) uri() *url.URL {
	u := &url.URL{Scheme: "vuja", Host: "codex-resume", Path: "/" + t.id}
	if t.claude {
		u.Host = "claude-resume"
		u.RawQuery = url.Values{"command": {t.command}}.Encode()
	}
	return u
}

func resumeTargetFromURL(u *url.URL) (resumeTarget, bool) {
	if u.Scheme != "vuja" || !isCodexResumeID(strings.TrimPrefix(u.Path, "/")) {
		return resumeTarget{}, false
	}
	id := strings.TrimPrefix(u.Path, "/")
	if u.Host == "codex-resume" {
		return parseResumeTarget(id)
	}
	if u.Host == "claude-resume" {
		t, ok := parseResumeTarget(u.Query().Get("command"))
		return t, ok && t.claude && strings.EqualFold(t.id, id)
	}
	return resumeTarget{}, false
}

func linkClaudeResumeLine(line []byte, linkFor func(string) string) []byte {
	visible := ansi.Strip(string(line))
	command := strings.TrimSpace(visible)
	if linkFor == nil || !isClaudeResumeCommand(command) {
		return line
	}
	// The terminal UUID is the final token, never a UUID-like worktree name.
	id := command[strings.LastIndexByte(command, ' ')+1:]
	// Validation uses display text; map its UUID back to the original bytes
	// so styling inside the ID survives within the hyperlink.
	visibleStart := strings.LastIndex(visible, id)
	start, end, position := -1, -1, 0
	var state byte
	for offset := 0; offset < len(line); {
		sequence, _, size, next := ansi.DecodeSequence(line[offset:], state, nil)
		if size == 0 {
			return line
		}
		plain := ansi.Strip(string(sequence))
		if len(plain) > 0 {
			if position == visibleStart {
				start = offset
			}
			position += len(plain)
			if position == visibleStart+len(id) {
				end = offset + size
				break
			}
		}
		offset, state = offset+size, next
	}
	if start < 0 || end < start {
		return line
	}
	target := linkFor(command)
	if target == "" {
		return line
	}
	return []byte(fmt.Sprintf("%s\x1b]8;id=vuja-claude-%s;%s\x1b\\%s\x1b]8;;\x1b\\%s", line[:start], id, target, line[start:end], line[end:]))
}

func (a *codexResumeAction) commandText() string {
	if a.command == "" {
		return codexResumeCommand(a.id)
	}
	t, ok := parseResumeTarget(a.command)
	if !ok || !t.claude || !strings.EqualFold(t.id, a.id) {
		return ""
	}
	return t.command
}

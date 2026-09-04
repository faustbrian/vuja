package policy

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/faustbrian/vuja/internal/config"
)

// HistoryCommand is the single privacy gate for command-history data. It
// preserves the leading-whitespace signal until the decision is made, rejects
// shell policy exclusions and terminal control data, and returns the
// normalized command only after every exclusion has passed.
func HistoryCommand(raw string, shellIgnored bool) (string, bool) {
	if raw == "" || shellIgnored {
		return "", false
	}
	first, _ := utf8.DecodeRuneInString(raw)
	if unicode.IsSpace(first) {
		return "", false
	}
	command := strings.TrimSpace(raw)
	if command == "" || strings.IndexFunc(command, unsafeHistoryControl) != -1 || IsSensitive(command) {
		return "", false
	}
	return command, true
}

func unsafeHistoryControl(value rune) bool {
	// Newlines and tabs are shell syntax in a multiline command, not terminal
	// control traffic. Preserve them in the exact event while rejecting NUL,
	// escape, delete, and every other control rune that could corrupt storage or
	// terminal rendering if it reached a history surface.
	return unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t'
}

var sensitiveAssignment = regexp.MustCompile(`(?i)(api[_-]?key|token|password|passwd|secret|private[_-]?key)\s*=\s*\S+`)

func IsSensitive(command string) bool {
	lower := strings.ToLower(command)
	if sensitiveAssignment.MatchString(command) {
		return true
	}
	for _, flag := range []string{"--password ", "--password=", "--token ", "--token=", "--api-key ", "--api-key=", "authorization: bearer "} {
		if strings.Contains(lower, flag) {
			return true
		}
	}
	for _, pattern := range config.Get().Suggestions.IgnorePatterns {
		if matches(pattern, command) {
			return true
		}
	}
	return false
}

func IsDestructive(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	patterns := []string{
		"rm -rf", "rm -fr", "git reset --hard", "git clean -f",
		"kubectl delete", "docker system prune", "drop database",
		"truncate table", "terraform destroy",
	}
	for _, pattern := range patterns {
		if strings.HasPrefix(command, pattern) || strings.Contains(command, " "+pattern) {
			return true
		}
	}
	return false
}

func Blocked(command string) bool {
	for _, pattern := range config.Get().Suggestions.Blocks {
		if matches(pattern, command) {
			return true
		}
	}
	return false
}

func matches(pattern, value string) bool {
	pattern, value = strings.TrimSpace(pattern), strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if pattern == value {
		return true
	}
	ok, _ := filepath.Match(pattern, value)
	return ok
}

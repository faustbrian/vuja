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
	// A forgetting request can contain the very command text the user wants
	// removed. Apply this exclusion to native recording, imports and feedback.
	if historyForgettingText(command) {
		return "", false
	}
	if command == "" || strings.IndexFunc(command, unsafeHistoryControl) != -1 || IsSensitive(command) {
		return "", false
	}
	return command, true
}

// This is intentionally a conservative text exclusion, not a shell parser.
// Quoted examples, comments and nonexecuting arguments can contain the same
// private target text. Root options and wrapper words do not weaken the rule.
// Do not evaluate expansions or load aliases/functions to classify a command.
func historyForgettingText(command string) bool {
	plain := strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, command)
	words := strings.FieldsFunc(plain, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>#`", r)
	})
	stage := 0
	for _, word := range words {
		switch stage {
		case 0:
			if filepath.Base(word) == "vuja" {
				stage = 1
			}
		case 1:
			if word == "history" {
				stage = 2
			}
		case 2:
			if word == "forget" {
				return true
			}
		}
	}
	return false
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
	preferences := config.Get().Suggestions
	return BlockedBy(preferences.Blocks, preferences.ExactBlocks, command)
}

func BlockedBy(patterns, literals []string, command string) bool {
	command = strings.TrimSpace(command)
	for _, literal := range literals {
		if command != "" && strings.TrimSpace(literal) == command {
			return true
		}
	}
	// Manual blocks retain the existing exact-or-glob meaning. UI-created
	// blocks have separate literal storage, so escaping cannot hide a neighbor.
	for _, pattern := range patterns {
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

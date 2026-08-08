package root

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	commandStartMessage       = "VUJA_CMD_START"
	commandStartIgnoreMessage = "VUJA_CMD_START:IGNORE"
)

func parseCommandStartMessage(message string) (match, ignored bool) {
	switch message {
	case commandStartMessage:
		return true, false
	case commandStartIgnoreMessage:
		return true, true
	default:
		return false, false
	}
}

func historyRecordableCommand(raw string, shellIgnored bool) (string, bool) {
	if raw == "" || shellIgnored {
		return "", false
	}
	first, _ := utf8.DecodeRuneInString(raw)
	if unicode.IsSpace(first) {
		return "", false
	}
	normalized := strings.TrimSpace(raw)
	return normalized, normalized != ""
}

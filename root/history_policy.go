package root

import (
	"github.com/faustbrian/vuja/internal/policy"
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
	return policy.HistoryCommand(raw, shellIgnored)
}

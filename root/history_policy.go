package root

import (
	"strings"

	"github.com/faustbrian/vuja/v2/internal/policy"
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
		_, ignored, match = commandStartPayload(message)
		return match, ignored
	}
}

// The accepted command is data within one control frame. Decode only after
// recognizing the complete prefix, never by redispatching its contents.
func commandStartPayload(message string) (encoded string, ignored, match bool) {
	if encoded, match = strings.CutPrefix(message, commandStartIgnoreMessage+":COMMAND:"); match {
		return encoded, true, true
	}
	encoded, match = strings.CutPrefix(message, commandStartMessage+":COMMAND:")
	return encoded, false, match
}

func historyRecordableCommand(raw string, shellIgnored bool) (string, bool) {
	return policy.HistoryCommand(raw, shellIgnored)
}

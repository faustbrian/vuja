package root

import (
	"strings"

	"github.com/faustbrian/vuja/spec"
)

func handleShellControlMessage(message string) bool {
	cwd, ok := strings.CutPrefix(message, "VUJA_CWD:")
	if !ok {
		return false
	}
	spec.SetShellCWD(cwd)
	return true
}

package root

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/config"
	"github.com/faustbrian/vuja/internal/policy"
)

// mirrorHistorySubmission appends only the command accepted by Vuja's history
// policy. It deliberately does not ask the shell to flush its broader in-memory
// history, which could contain commands Vuja rejected as private.
func mirrorHistorySubmission(command, shellName string, submittedAt time.Time) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	record, recordable := recordableNativeHistoryRecord(command, shellName, submittedAt)
	if !recordable {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path, err := resolveShellHistoryPath(home, shellName, config.Get().History.Integrations.Shell.Path)
	if err != nil {
		return err
	}
	if err := config.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := config.OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(record); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func recordableNativeHistoryRecord(command, shellName string, submittedAt time.Time) (string, bool) {
	if _, recordable := policy.HistoryCommand(command, false); !recordable {
		return "", false
	}
	return nativeHistoryRecord(command, shellName, submittedAt), true
}

func nativeHistoryRecord(command, shellName string, submittedAt time.Time) string {
	switch shellName {
	case "zsh":
		command = strings.ReplaceAll(command, "\n", "\\\n")
		return fmt.Sprintf(": %d:0;%s\n", submittedAt.Unix(), command)
	case "fish":
		command = strings.ReplaceAll(command, "\\", "\\\\")
		command = strings.ReplaceAll(command, "\n", "\\n")
		return fmt.Sprintf("- cmd: %s\n  when: %d\n", command, submittedAt.Unix())
	default:
		return command + "\n"
	}
}

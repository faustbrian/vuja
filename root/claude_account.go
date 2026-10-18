package root

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Claude's settings docs identify ~/.claude.json as sign-in metadata. Its
// 2.1.292 global-config resolver prefers <config dir>/.config.json when present,
// otherwise $CLAUDE_CONFIG_DIR/.claude.json or ~/.claude.json. Account writes
// persist oauthAccount.emailAddress. No credentials/Keychain/CLI are probed.
// https://code.claude.com/docs/en/settings
func claudeAccountFiles() (primary, legacy string) {
	if directory := os.Getenv("CLAUDE_CONFIG_DIR"); directory != "" {
		return filepath.Join(directory, ".claude.json"), filepath.Join(directory, ".config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", ".config.json")
}

func selectClaudeAccountFile(primary, legacy string) string {
	// An unreadable or malformed authoritative legacy file must hide identity,
	// not fall through to another account. Recheck each tick for migrations.
	if _, err := os.Lstat(legacy); legacy != "" && !os.IsNotExist(err) {
		return legacy
	}
	return primary
}

func claudeAccountPath() string {
	return selectClaudeAccountFile(claudeAccountFiles())
}

func readClaudeAccount(path string) string {
	if path == "" {
		return ""
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), "Claude account metadata")
	defer f.Close()
	info, err := f.Stat()
	// Reuse the conservative Codex metadata budget; oversized configuration
	// hides display metadata rather than reading unbounded per-project state.
	if err != nil || !info.Mode().IsRegular() || info.Size() > codexAuthByteLimit {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, codexAuthByteLimit+1))
	if err != nil || len(data) > codexAuthByteLimit {
		return ""
	}
	var metadata struct {
		Account struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &metadata) != nil || !safeAccountLabel(metadata.Account.Email) {
		return ""
	}
	return metadata.Account.Email
}

func (e *statusEngine) refreshClaudeAccount() {
	if e.claudeAccountFile == "" || e.ctx.Err() != nil {
		return
	}
	account := readClaudeAccount(selectClaudeAccountFile(e.claudeAccountFile, e.claudeAccountLegacyFile))
	e.mu.Lock()
	if e.ctx.Err() != nil || e.snapshot.ClaudeAccount == account {
		e.mu.Unlock()
		return
	}
	e.snapshot.ClaudeAccount = account
	e.revision++
	e.snapshot.Revision = e.revision
	snapshot := cloneStatusSnapshot(e.snapshot)
	e.mu.Unlock()
	e.publish(snapshot)
}

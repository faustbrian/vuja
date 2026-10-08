package root

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const codexAuthByteLimit = 1 << 20

func codexAccountHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// readCodexAccount reads display metadata, not authentication validity. Other
// credential stores are intentionally not probed. Never return or log tokens.
func readCodexAccount(home string) string {
	if home == "" {
		return ""
	}
	fd, err := unix.Open(filepath.Join(home, "auth.json"), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), "Codex account metadata")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > codexAuthByteLimit {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, codexAuthByteLimit+1))
	if err != nil || len(data) > codexAuthByteLimit {
		return ""
	}
	var auth struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.Mode == "api_key" {
		return ""
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil || !safeCodexAccount(claims.Email) {
		return ""
	}
	return claims.Email
}

func safeCodexAccount(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func (e *statusEngine) refreshCodexAccount() {
	if e.codexAccountHome == "" || e.ctx.Err() != nil {
		return
	}
	account := readCodexAccount(e.codexAccountHome)
	e.mu.Lock()
	if e.ctx.Err() != nil || e.snapshot.CodexAccount == account {
		e.mu.Unlock()
		return
	}
	e.snapshot.CodexAccount = account
	e.revision++
	e.snapshot.Revision = e.revision
	snapshot := cloneStatusSnapshot(e.snapshot)
	e.mu.Unlock()
	e.publish(snapshot)
}

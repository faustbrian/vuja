package shell

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Adapter defines the behavior for different shell environments
type Adapter interface {
	GetName() string
	GetShellPath() string
	GetEnv(fd int, pid int) []string
	PrepareSelectSequence(selected string) []byte
	// ScanAliases returns a map of alias name to target command
	ScanAliases() map[string]string
}

// Current shell instance
var Current Adapter

type aliasFileState struct {
	path    string
	modTime int64
	size    int64
}

type aliasCacheEntry struct {
	files   []aliasFileState
	aliases map[string]string
}

var (
	aliasCacheMu      sync.Mutex
	aliasCache        = make(map[string]aliasCacheEntry)
	readAliasFile     = os.ReadFile
	zshConfigMu       sync.Mutex
	zshConfigDir      string
	zshConfigResolved bool
)

func Init(name string) {
	switch name {
	case "zsh":
		Current = &ZshAdapter{}
	case "fish":
		Current = &FishAdapter{}
	default:
		Current = &BashAdapter{}
	}
}

// BashAdapter implementation
type BashAdapter struct{}

func (b *BashAdapter) GetName() string      { return "bash" }
func (b *BashAdapter) GetShellPath() string { return "bash" }
func (b *BashAdapter) GetEnv(fd int, pid int) []string {
	return append(os.Environ(), "VUJA_FD="+fmt.Sprint(fd), "VUJA_PID="+fmt.Sprint(pid))
}
func (b *BashAdapter) PrepareSelectSequence(selected string) []byte {
	return append([]byte{0x15}, []byte(selected)...)
}
func (b *BashAdapter) ScanAliases() map[string]string {
	return ScanPosixAliases([]string{".bashrc", ".bash_profile", ".bash_aliases"})
}

// ZshAdapter implementation
type ZshAdapter struct{}

func (z *ZshAdapter) GetName() string      { return "zsh" }
func (z *ZshAdapter) GetShellPath() string { return "zsh" }
func (z *ZshAdapter) GetEnv(fd int, pid int) []string {
	return append(os.Environ(), "VUJA_FD="+fmt.Sprint(fd), "VUJA_PID="+fmt.Sprint(pid))
}
func (z *ZshAdapter) PrepareSelectSequence(selected string) []byte {
	return append([]byte{0x15}, []byte(selected)...)
}
func (z *ZshAdapter) ScanAliases() map[string]string {
	envSet := os.Getenv("ZDOTDIR") != ""
	zdotdir := GetZshConfigDir()
	home, _ := os.UserHomeDir()

	var files []string
	if !envSet && zdotdir != home {
		files = append(files, filepath.Join(home, ".zshenv"))
	}

	files = append(files,
		filepath.Join(zdotdir, ".zshenv"),
		filepath.Join(zdotdir, ".zprofile"),
		filepath.Join(zdotdir, ".zshrc"),
	)

	return ScanPosixAliases(files)
}

func GetZshConfigDir() string {
	if zdotdir := os.Getenv("ZDOTDIR"); zdotdir != "" {
		return zdotdir
	}
	zshConfigMu.Lock()
	defer zshConfigMu.Unlock()
	if zshConfigResolved {
		return zshConfigDir
	}

	// Fallback: ask zsh directly in case ZDOTDIR is set in ~/.zshenv without export
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "zsh", "-c", "echo $ZDOTDIR")
	out, err := cmd.Output()
	if err == nil {
		zdotdir := strings.TrimSpace(string(out))
		if zdotdir != "" {
			zshConfigDir = zdotdir
			zshConfigResolved = true
			return zshConfigDir
		}
	}

	home, _ := os.UserHomeDir()
	zshConfigDir = home
	zshConfigResolved = true
	return zshConfigDir
}

// FishAdapter implementation
type FishAdapter struct{}

func (f *FishAdapter) GetName() string      { return "fish" }
func (f *FishAdapter) GetShellPath() string { return "fish" }
func (f *FishAdapter) GetEnv(fd int, pid int) []string {
	return append(os.Environ(), "VUJA_FD="+fmt.Sprint(fd), "VUJA_PID="+fmt.Sprint(pid))
}
func (f *FishAdapter) PrepareSelectSequence(selected string) []byte {
	return append([]byte{0x15}, []byte(selected)...)
}
func (f *FishAdapter) ScanAliases() map[string]string {
	// fish uses 'alias' command in config.fish or separate function files
	return ScanPosixAliases([]string{filepath.Join(GetFishConfigDir(), "config.fish")})
}

func GetFishConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fish")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fish")
}

func ScanPosixAliases(files []string) map[string]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return map[string]string{}
	}

	states := make([]aliasFileState, 0, len(files))
	for _, file := range files {
		path := file
		if !filepath.IsAbs(file) {
			path = filepath.Join(home, file)
		}
		state := aliasFileState{path: path, modTime: -1, size: -1}
		if info, statErr := os.Stat(path); statErr == nil {
			state.modTime = info.ModTime().UnixNano()
			state.size = info.Size()
		}
		states = append(states, state)
	}
	keyParts := make([]string, 0, len(states))
	for _, state := range states {
		keyParts = append(keyParts, state.path)
	}
	key := strings.Join(keyParts, "\x00")

	aliasCacheMu.Lock()
	if cached, ok := aliasCache[key]; ok && aliasStatesEqual(cached.files, states) {
		aliases := maps.Clone(cached.aliases)
		aliasCacheMu.Unlock()
		return aliases
	}
	aliasCacheMu.Unlock()

	aliases := make(map[string]string)
	for _, state := range states {
		data, err := readAliasFile(state.path)
		if err != nil {
			continue
		}

		maps.Copy(aliases, ParseAliases(string(data)))
	}
	aliasCacheMu.Lock()
	aliasCache[key] = aliasCacheEntry{files: states, aliases: maps.Clone(aliases)}
	aliasCacheMu.Unlock()
	return maps.Clone(aliases)
}

func aliasStatesEqual(left, right []aliasFileState) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func resetAliasCache() {
	aliasCacheMu.Lock()
	aliasCache = make(map[string]aliasCacheEntry)
	aliasCacheMu.Unlock()
}

func ParseAliases(data string) map[string]string {
	aliases := make(map[string]string)
	lines := strings.SplitSeq(data, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "alias ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "alias"))
		if body == "" {
			continue
		}

		pairs := SplitAliasTokens(body)
		for _, pair := range pairs {
			before, after, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			key := strings.TrimSpace(before)
			val := strings.Trim(strings.TrimSpace(after), `"'`)
			if key != "" && val != "" {
				aliases[key] = val
			}
		}
	}
	return aliases
}

func SplitAliasTokens(s string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	var quoteChar rune
	for _, c := range s {
		switch {
		case !inQuote && (c == '"' || c == '\''):
			inQuote = true
			quoteChar = c
			cur.WriteRune(c)
		case inQuote && c == quoteChar:
			inQuote = false
			cur.WriteRune(c)
		case !inQuote && c == ' ':
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

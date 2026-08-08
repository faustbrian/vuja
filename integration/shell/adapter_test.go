package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestScanPosixAliases(t *testing.T) {

	input := `
alias gca='git commit -a'
alias ta="tmux a -t" # this is a comment
# alias hidden="not found"
alias l='ls' ll='ls -l'
`
	expected := map[string]string{
		"gca": "git commit -a",
		"ta":  "tmux a -t",
		"l":   "ls",
		"ll":  "ls -l",
	}

	got := ParseAliases(input)
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("ScanPosixAliases() = %v; want %v", got, expected)
	}
}

func TestScanPosixAliasesCachesUnchangedFilesAndRefreshesChangedFiles(t *testing.T) {
	resetAliasCache()
	t.Cleanup(resetAliasCache)

	dir := t.TempDir()
	path := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(path, []byte("alias gs='git status'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalReadFile := readAliasFile
	var reads atomic.Int32
	readAliasFile = func(name string) ([]byte, error) {
		reads.Add(1)
		return os.ReadFile(name)
	}
	t.Cleanup(func() { readAliasFile = originalReadFile })

	if got := ScanPosixAliases([]string{path})["gs"]; got != "git status" {
		t.Fatalf("expected first alias value, got %q", got)
	}
	if got := ScanPosixAliases([]string{path})["gs"]; got != "git status" {
		t.Fatalf("expected cached alias value, got %q", got)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("expected one file read for an unchanged alias file, got %d", got)
	}

	if err := os.WriteFile(path, []byte("alias gs='git status --short'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ScanPosixAliases([]string{path})["gs"]; got != "git status --short" {
		t.Fatalf("expected changed alias value, got %q", got)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("expected changed file to be reread once, got %d reads", got)
	}
}

func TestSplitAliasTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{

		{"Single", "a='b'", []string{"a='b'"}},
		{"Multi", "a='b' c=\"d\"", []string{"a='b'", "c=\"d\""}},

		{"With Space", "ta='tmux a -t' l='ls -l'", []string{"ta='tmux a -t'", "l='ls -l'"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitAliasTokens(tt.input)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("SplitAliasTokens(%q) = %v; want %v", tt.input, got, tt.expected)
			}
		})
	}
}

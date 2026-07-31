package root

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/faustbrian/vuja/internal/config"
)

func TestDebugSuggestExplainsLiveDirectoryRanking(t *testing.T) {
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "scalar-docs"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	config.Init(config.DefaultConfig())

	command := newDebugSuggestCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"cd scalar-", "--cwd", workDir, "--mode", "spec", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute debug suggest: %v\n%s", err, output.String())
	}

	var result debugSuggestOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode debug output: %v\n%s", err, output.String())
	}
	if result.Query != "cd scalar-" || result.CWD != workDir || result.Mode != "spec" {
		t.Fatalf("unexpected diagnostic context: %+v", result)
	}
	if len(result.Suggestions) == 0 {
		t.Fatal("expected ranked suggestions")
	}
	first := result.Suggestions[0]
	if first.Command != "cd scalar-docs/" || first.Source != "filesystem" {
		t.Fatalf("expected live directory first, got %+v", first)
	}
	if first.Rank != 1 || first.Breakdown.MatchQuality == 0 {
		t.Fatalf("expected rank and score breakdown, got %+v", first)
	}
}

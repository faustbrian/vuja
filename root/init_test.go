package root

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureShellIntegrationMovesBootstrapBeforeExistingInitialization(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), ".zshrc")
	evalCommand := `eval "$(vuja init zsh)"`
	original := "source expensive-plugin.zsh\n\n# Vuja Autocomplete\n" + evalCommand + "\n"
	if err := os.WriteFile(configFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	integrationFile := filepath.Join(t.TempDir(), "init.zsh")
	integration := shellIntegration("zsh", integrationFile)
	changed, err := configureShellIntegration(configFile, integration)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected an integration at the end of the file to be moved")
	}

	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	sourceCommand := `source "` + integrationFile + `"`
	if !strings.HasPrefix(got, "# Vuja Autocomplete\n"+`export PATH="$HOME/.local/bin:$PATH"`+"\n"+sourceCommand+"\n\n") {
		t.Fatalf("expected Vuja bootstrap first, got:\n%s", got)
	}
	if strings.Contains(got, evalCommand) {
		t.Fatalf("expected legacy runtime initialization to be removed, got:\n%s", got)
	}
	if strings.Count(got, sourceCommand) != 1 {
		t.Fatalf("expected exactly one Vuja bootstrap, got:\n%s", got)
	}
	if !strings.Contains(got, "source expensive-plugin.zsh") {
		t.Fatalf("existing shell initialization was lost:\n%s", got)
	}

	changed, err = configureShellIntegration(configFile, integration)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected an already-first bootstrap to remain unchanged")
	}
}

func TestConfigureShellIntegrationPrependsBootstrapToNewConfig(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.fish")
	integrationFile := filepath.Join(t.TempDir(), "init.fish")
	integration := shellIntegration("fish", integrationFile)

	changed, err := configureShellIntegration(configFile, integration)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected missing integration to be added")
	}
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	expected := "# Vuja Autocomplete\n" +
		`set -gx PATH "$HOME/.local/bin" $PATH` + "\n" +
		`source "` + integrationFile + "\"\n"
	if string(content) != expected {
		t.Fatalf("unexpected new configuration:\n%s", content)
	}
}

func TestWriteShellIntegrationCreatesStaticHook(t *testing.T) {
	integrationFile := filepath.Join(t.TempDir(), "vuja", "init.zsh")
	binaryPath := filepath.Join(t.TempDir(), "bin", "vuja")

	if err := writeShellIntegration(integrationFile, "zsh", binaryPath); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(integrationFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	if !strings.Contains(got, `exec "`+binaryPath+`"`) {
		t.Fatalf("expected hook to execute the installed binary directly, got:\n%s", got)
	}
	if strings.Contains(got, "vuja init") {
		t.Fatalf("expected static hook without runtime initialization, got:\n%s", got)
	}
}

func TestCleanShellConfigRemovesStaticBootstrap(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), ".zshrc")
	original := "# Vuja Autocomplete\n" +
		`export PATH="$HOME/.local/bin:$PATH"` + "\n" +
		`source "/tmp/vuja/init.zsh"` + "\n\n" +
		"source expensive-plugin.zsh\n"
	if err := os.WriteFile(configFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if !cleanShellConfig(configFile) {
		t.Fatal("expected static bootstrap to be removed")
	}
	content, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "vuja") {
		t.Fatalf("expected all Vuja bootstrap lines to be removed, got:\n%s", content)
	}
	if string(content) != "source expensive-plugin.zsh\n" {
		t.Fatalf("existing shell initialization was changed:\n%s", content)
	}
}

func TestGeneratedShellHooksAreSyntacticallyValid(t *testing.T) {
	for _, shellName := range []string{"zsh", "bash", "fish"} {
		t.Run(shellName, func(t *testing.T) {
			shellPath, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is not installed", shellName)
			}
			command := exec.CommandContext(t.Context(), shellPath, "-n")
			command.Stdin = strings.NewReader(shellInitScript(shellName, "/tmp/vuja"))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("generated %s hook is invalid: %v: %s", shellName, err, output)
			}
		})
	}
}

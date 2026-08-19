package root

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestStatusEnginePublishesRepositoryAndVersionAccuracyContext(t *testing.T) {
	directory := t.TempDir()
	gitDirectory := filepath.Join(directory, ".git")
	if err := os.MkdirAll(filepath.Join(gitDirectory, "logs", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(gitDirectory, "HEAD"):                  "ref: refs/heads/main\n",
		filepath.Join(gitDirectory, "logs", "refs", "stash"): "one\ntwo\n",
		filepath.Join(directory, "package.json"):             `{"name":"vuja","version":"1.2.3"}`,
		filepath.Join(directory, ".node-version"):            "24.4.0\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := newStatusEngine(statusEngineOptions{
		GitLines: true, GitStash: true, Package: true,
		Run: func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
			command := strings.Join(append([]string{name}, args...), " ")
			switch {
			case strings.Contains(command, "status --porcelain=v2"):
				return []byte("# branch.head main\n1 .M N... 100644 100644 100644 a b changed.go\n"), nil
			case strings.Contains(command, "diff --numstat"):
				return []byte("12\t3\tchanged.go\n"), nil
			case name == "node":
				return []byte("v24.3.0\n"), nil
			default:
				return nil, errors.New("unavailable")
			}
		},
	})
	t.Cleanup(engine.Close)
	engine.Refresh(directory)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := engine.Snapshot()
		if snapshot.Git.LinesAdded == 12 && len(snapshot.VersionMismatches) == 1 {
			if snapshot.Git.StashCount != 2 || snapshot.Package.Name != "vuja" ||
				snapshot.VersionMismatches[0] != (versionMismatch{Tool: "node", Declared: "24.4.0", Active: "24.3.0"}) {
				t.Fatalf("unexpected status snapshot: %+v", snapshot)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status context was not published: %+v", engine.Snapshot())
}

func TestSudoStatusUsesNonInteractiveBoundedProbeAndCache(t *testing.T) {
	directory := t.TempDir()
	calls := 0
	engine := newStatusEngine(statusEngineOptions{
		Session: true,
		Run: func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
			if name != "sudo" || strings.Join(args, " ") != "-n true" {
				t.Fatalf("unexpected status command: %s %v", name, args)
			}
			calls++
			return nil, nil
		},
	})
	t.Cleanup(engine.Close)
	if !engine.cachedSudoStatus(directory) || !engine.cachedSudoStatus(directory) {
		t.Fatal("expected cached sudo authentication to be active")
	}
	if calls != 1 {
		t.Fatalf("expected one cached sudo probe, got %d", calls)
	}
}

func TestParseShellStatusMessages(t *testing.T) {
	tests := []struct {
		message string
		want    shellStatusUpdate
		ok      bool
	}{
		{message: "VUJA_JOBS:3:1", want: shellStatusUpdate{Kind: "jobs", Jobs: 3, StoppedJobs: 1}, ok: true},
		{message: "VUJA_ENV:virtualenv:/tmp/my:venv", want: shellStatusUpdate{Kind: "virtualenv", Value: "/tmp/my:venv"}, ok: true},
		{message: "VUJA_ENV:aws-profile:staging", want: shellStatusUpdate{Kind: "aws-profile", Value: "staging"}, ok: true},
		{message: "echo VUJA_JOBS:3:1", ok: false},
	}
	for _, test := range tests {
		got, ok := parseShellStatusMessage(test.message)
		if ok != test.ok || got != test.want {
			t.Fatalf("parseShellStatusMessage(%q) = %+v, %v; want %+v, %v", test.message, got, ok, test.want, test.ok)
		}
	}
}

func TestShellInitPublishesFiniteLocalStatus(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "fish"} {
		script := shellInitScript(shell, "/unused/vuja")
		for _, marker := range []string{"VUJA_JOBS:", "VUJA_ENV:virtualenv:", "VUJA_ENV:conda:", "VUJA_ENV:direnv:", "VUJA_ENV:aws-profile:", "VUJA_ENV:aws-region:", "VUJA_ENV:docker-context:", "VUJA_ENV:kubeconfig:"} {
			if !strings.Contains(script, marker) {
				t.Fatalf("%s integration does not publish %s", shell, marker)
			}
		}
	}
}

func TestLocalRepositoryStatusUsesFilesWithoutCustomHooks(t *testing.T) {
	directory := t.TempDir()
	gitDirectory := filepath.Join(directory, ".git")
	if err := os.MkdirAll(filepath.Join(gitDirectory, "logs", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "logs", "refs", "stash"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"vuja-ui","version":"2.4.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	repository := detectRepositoryStatus(directory, true)
	if repository.Root != directory || repository.StashCount != 2 {
		t.Fatalf("unexpected repository status: %+v", repository)
	}
	if pkg := detectProjectPackage(directory); pkg.Name != "vuja-ui" || pkg.Version != "2.4.1" {
		t.Fatalf("unexpected package status: %+v", pkg)
	}
}

func TestParseGitLineMetricsIgnoresBinaryFiles(t *testing.T) {
	added, deleted := parseGitLineMetrics([]byte("12\t3\tapp.go\n-\t-\tlogo.png\n4\t0\tnew.go\n"))
	if added != 16 || deleted != 3 {
		t.Fatalf("unexpected Git line metrics: +%d -%d", added, deleted)
	}
}

func TestPinnedVersionsOnlyUseExactProjectDeclarations(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"go.mod":          "module example.com/app\n\ntoolchain go1.26.2\n",
		".node-version":   "24.4.0\n",
		".php-version":    "8.4.3\n",
		".python-version": "3.13.5\n",
		".ruby-version":   "3.4.4\n",
		".tool-versions":  "elixir 1.18.4-otp-27\n",
		"composer.json":   `{"require":{"php":"^8.4"}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pins := detectPinnedVersions(directory)
	if pins["go"] != "1.26.2" || pins["node"] != "24.4.0" || pins["php"] != "8.4.3" ||
		pins["python"] != "3.13.5" || pins["ruby"] != "3.4.4" || pins["elixir"] != "1.18.4" {
		t.Fatalf("unexpected exact pins: %v", pins)
	}
	active := map[string]string{
		"go": "1.26.2", "node": "24.3.0", "php": "8.4.3",
		"python": "3.13.5", "ruby": "3.4.4", "elixir": "1.18.4",
	}
	if mismatch := comparePinnedVersions(pins, active); len(mismatch) != 1 ||
		mismatch[0].Tool != "node" || mismatch[0].Declared != "24.4.0" || mismatch[0].Active != "24.3.0" {
		t.Fatalf("unexpected version mismatch: %+v", mismatch)
	}
	for _, test := range []struct {
		tool, declared, active string
	}{
		{tool: "python", declared: "3.13.5", active: "3.12.11"},
		{tool: "ruby", declared: "3.4.4", active: "3.3.8"},
		{tool: "elixir", declared: "1.18.4", active: "1.17.3"},
	} {
		t.Run(test.tool+" mismatch", func(t *testing.T) {
			mismatches := comparePinnedVersions(
				map[string]string{test.tool: test.declared},
				map[string]string{test.tool: test.active},
			)
			if len(mismatches) != 1 || mismatches[0] != (versionMismatch{
				Tool: test.tool, Declared: test.declared, Active: test.active,
			}) {
				t.Fatalf("unexpected %s mismatch: %+v", test.tool, mismatches)
			}
		})
	}
}

func TestOperationalContextsAreParsedLocallyAndCommandGated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kube := filepath.Join(home, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(kube), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kube, []byte(`current-context: staging
contexts:
- context:
    namespace: payments
  name: staging
`), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(home, ".docker", "config.json")
	if err := os.MkdirAll(filepath.Dir(docker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docker, []byte(`{"currentContext":"colima"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	contexts := detectOperationalContexts(shellStatusSnapshot{AWSProfile: "staging", AWSRegion: "eu-north-1"})
	if contexts.Kubernetes != "staging" || contexts.KubernetesNamespace != "payments" ||
		contexts.Docker != "colima" || contexts.AWSProfile != "staging" || contexts.AWSRegion != "eu-north-1" {
		t.Fatalf("unexpected operational contexts: %+v", contexts)
	}
	if got := contextsForCommand("git status", contexts); len(got) != 0 {
		t.Fatalf("unrelated commands must not expose contexts: %v", got)
	}
	if got := strings.Join(contextsForCommand("kubectl get pods", contexts), " "); !strings.Contains(got, "Kube staging/payments") {
		t.Fatalf("kubectl must expose Kubernetes context: %q", got)
	}
	if got := strings.Join(contextsForCommand("aws sts get-caller-identity", contexts), " "); !strings.Contains(got, "AWS staging eu-north-1") {
		t.Fatalf("aws must expose profile and region: %q", got)
	}
}

func TestCommandContextChangesOnlyForSupportedToolFamilies(t *testing.T) {
	for command, want := range map[string]string{
		"k":                        "",
		"kubectl":                  "kubernetes",
		"kubectl get pods":         "kubernetes",
		"sudo kubectl get pods":    "kubernetes",
		"AWS_PROFILE=prod aws sts": "aws",
		"docker compose up":        "docker",
		"git status":               "",
	} {
		if got := statusCommandContext(command); got != want {
			t.Fatalf("statusCommandContext(%q) = %q; want %q", command, got, want)
		}
	}
}

func TestSemanticExitStatusNamesCommonShellFailures(t *testing.T) {
	for code, want := range map[int]string{
		126: "not executable",
		127: "not found",
		130: "interrupted",
		137: "killed",
		143: "terminated",
	} {
		if got := semanticExitStatus(code); got != want {
			t.Fatalf("semanticExitStatus(%d) = %q; want %q", code, got, want)
		}
	}
}

func TestChatboxRendersAllFiniteStatusCapabilities(t *testing.T) {
	compositor := newTerminalCompositor(os.Stdout, "classic", "", 240, 10)
	t.Cleanup(compositor.Close)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Title:     terminalChatboxBarConfig{Left: []string{"directory"}, Right: []string{"package", "versions"}},
		Status: terminalChatboxBarConfig{
			Left:  []string{"session", "git-branch", "git-stash", "git-lines", "environment", "version-mismatch", "contexts", "stale"},
			Right: []string{"jobs", "duration", "exit"},
		},
	})
	exitCode := 127
	compositor.SetCommandContext("kubectl get pods")
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory:         "/tmp/project/internal",
		RepositoryRoot:    "/tmp/project",
		DirectoryReadOnly: true,
		Git:               gitStatusSnapshot{Branch: "main", StashCount: 2, LinesAdded: 12, LinesDeleted: 3},
		Versions:          map[string]string{"go": "1.26.0"},
		Package:           projectPackageSnapshot{Name: "vuja", Version: "0.8.0"},
		Shell:             shellStatusSnapshot{Jobs: 3, StoppedJobs: 1},
		Session:           sessionStatusSnapshot{SSH: true, User: "brian", Host: "devbox"},
		Contexts:          operationalContextSnapshot{Kubernetes: "staging", KubernetesNamespace: "payments"},
		Environment:       []string{"venv api", "direnv loaded"},
		VersionMismatches: []versionMismatch{{Tool: "node", Declared: "24.4.0", Active: "24.3.0"}},
		StaleProviders:    []string{"git"},
		StaleSince:        time.Now().Add(-2 * time.Second),
		ExitCode:          &exitCode,
	})

	plain := ansi.Strip(compositor.inputBoxTitleLine() + "\n" + strings.Join(compositor.inputBoxStatusLines(), "\n"))
	for _, expected := range []string{
		"/tmp/project/internal", "read-only", "vuja 0.8.0", "ssh brian@devbox", "stash 2",
		"+12", "-3", "venv api", "Node 24.4.0≠24.3.0", "Kube staging/payments",
		"stale git", "jobs 3", "stopped 1", "exit 127 not found",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("expected %q in rendered status %q", expected, plain)
		}
	}
}

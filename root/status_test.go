package root

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseGitStatusSummarizesBranchAndChanges(t *testing.T) {
	status := parseGitStatus([]byte("# branch.oid abcdef1234567890\n# branch.head (detached)\n# branch.ab +2 -1\n1 M. N... 100644 100644 100644 a b staged.go\n1 .M N... 100644 100644 100644 a b edited.go\n1 A. N... 000000 100644 100644 a b added.go\n1 .D N... 100644 100644 000000 a b deleted.go\n2 R. N... 100644 100644 100644 a b R100 renamed.go\told.go\n? new.go\nu UU N... 100644 100644 100644 100644 a b c conflict.go\n"))

	if status.Branch != "detached abcdef1" || status.Ahead != 2 || status.Behind != 1 ||
		status.Staged != 3 || status.Modified != 2 || status.Renamed != 1 || status.Untracked != 1 ||
		status.Conflicts != 1 || status.Added != 1 || status.Deleted != 1 || status.Changed != 7 {
		t.Fatalf("unexpected git status: %+v", status)
	}
}

func TestDetectGitOperationUsesRepositoryStateFiles(t *testing.T) {
	for _, test := range []struct {
		path, want string
		directory  bool
	}{
		{path: "rebase-merge", want: "rebasing", directory: true},
		{path: "rebase-apply", want: "rebasing", directory: true},
		{path: "MERGE_HEAD", want: "merging"},
		{path: "CHERRY_PICK_HEAD", want: "cherry-picking"},
		{path: "REVERT_HEAD", want: "reverting"},
	} {
		t.Run(test.want+test.path, func(t *testing.T) {
			dir := t.TempDir()
			gitDir := filepath.Join(dir, ".git")
			if err := os.Mkdir(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(gitDir, test.path)
			if test.directory {
				if err := os.Mkdir(state, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(state, []byte("abc"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectGitOperation(dir); got != test.want {
				t.Fatalf("expected %s, got %q", test.want, got)
			}
		})
	}
}

func TestReadGitBranchHandlesNamedAndDetachedHeads(t *testing.T) {
	gitDir := t.TempDir()
	head := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/feature/chatbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGitBranch(gitDir); got != "feature/chatbox" {
		t.Fatalf("expected named branch, got %q", got)
	}
	if err := os.WriteFile(head, []byte("abcdef1234567890\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGitBranch(gitDir); got != "detached abcdef1" {
		t.Fatalf("expected detached head, got %q", got)
	}
}

func TestDetectProjectVersionsUsesRepositoryMetadata(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "internal", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.26.0\n",
		"package.json":   `{"engines":{"node":">=24"},"packageManager":"bun@1.2.3"}`,
		"rust-toolchain": "1.88.0\n",
		"composer.json":  `{"require":{"php":"^8.4","laravel/framework":"^12.0"}}`,
		"composer.lock":  `{"packages":[{"name":"laravel/framework","version":"v12.4.1"}]}`,
		"Dockerfile":     "FROM scratch\n",
		"compose.yaml":   "services: {}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	versions := detectProjectVersions(nested)
	want := map[string]string{
		"go": "1.26.0", "node": "24", "bun": "1.2.3", "rust": "1.88.0",
		"php": "8.4", "laravel": "12.4.1", "docker": "", "docker-compose": "",
	}
	for name, value := range want {
		if got, ok := versions[name]; !ok || got != value {
			t.Fatalf("expected %s=%q, got %q present=%v", name, value, got, ok)
		}
	}
}

func TestDetectProjectVersionsSupportsPythonRubyAndElixir(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"pyproject.toml":  "[project]\nrequires-python = \">=3.13\"\n",
		".python-version": "3.13.5\n",
		"Gemfile":         "source \"https://rubygems.org\"\n",
		".ruby-version":   "3.4.4\n",
		"mix.exs":         "defmodule Example.MixProject do\nend\n",
		".tool-versions":  "elixir 1.18.4-otp-27\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	versions := detectProjectVersionsAt(directory)
	want := map[string]string{
		"python": "3.13.5",
		"ruby":   "3.4.4",
		"elixir": "1.18.4-otp-27",
	}
	for name, value := range want {
		if got, present := versions[name]; !present || got != value {
			t.Fatalf("expected %s=%q, got %q present=%v", name, value, got, present)
		}
	}
}

func TestNormalizePythonRubyAndElixirVersions(t *testing.T) {
	tests := []struct {
		name, output, want string
	}{
		{name: "python", output: "Python 3.13.5\n", want: "3.13.5"},
		{name: "ruby", output: "ruby 3.4.4 (2025-05-14 revision a38531fd3f) +PRISM [arm64-darwin]\n", want: "3.4.4"},
		{name: "elixir", output: "Erlang/OTP 27 [erts-15.2]\n\nElixir 1.18.4 (compiled with Erlang/OTP 27)\n", want: "1.18.4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeToolVersion(test.name, test.output); got != test.want {
				t.Fatalf("expected %s, got %q", test.want, got)
			}
		})
	}
}

func TestLaravelVersionSupportsComposerMetadataVariants(t *testing.T) {
	for _, test := range []struct {
		name, path, content string
	}{
		{name: "development lock package", path: "composer.lock", content: `{"packages":[],"packages-dev":[{"name":"laravel/framework","version":"v12.5.0"}]}`},
		{name: "legacy installed package array", path: "vendor/composer/installed.json", content: `[{"name":"laravel/framework","pretty_version":"v12.6.0"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got := detectProjectVersionsAt(directory)["laravel"]
			want := "12.5.0"
			if strings.Contains(test.name, "legacy") {
				want = "12.6.0"
			}
			if got != want {
				t.Fatalf("expected Laravel %s, got %q", want, got)
			}
		})
	}
}

func TestNodeVersionPrefersPinnedProjectFileOverPackageRelevance(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"example"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".node-version"), []byte("24.3.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectProjectVersionsAt(directory)["node"]; got != "24.3.1" {
		t.Fatalf("expected pinned Node 24.3.1, got %q", got)
	}
}

func TestDetectProjectVersionsSuppressesIrrelevantTools(t *testing.T) {
	if got := detectProjectVersions(t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no versions outside a repository, got %v", got)
	}
}

func TestDetectProjectVersionsDoesNotInheritHomeToolchains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".tool-versions"), []byte("nodejs 24.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, "Developer")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := detectProjectVersions(directory); len(got) != 0 {
		t.Fatalf("expected home-level toolchains to remain irrelevant, got %v", got)
	}
	if got := detectProjectVersions(home); len(got) != 0 {
		t.Fatalf("expected the home directory itself not to act as a repository, got %v", got)
	}
}

func TestManifestChangeInvalidatesDeclaredVersion(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(manifest, []byte("module example.com/app\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := newStatusEngine(statusEngineOptions{})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	waitForStatusDirectory(t, engine, dir)
	if got := engine.Snapshot().Versions["go"]; got != "1.25" {
		t.Fatalf("expected Go 1.25, got %q", got)
	}
	if err := os.WriteFile(manifest, []byte("module example.com/app\n\ntoolchain go1.26.1\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.Refresh(dir)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if engine.Snapshot().Versions["go"] == "1.26.1" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("manifest change did not invalidate declared Go version")
}

func TestProjectMetadataChangeImmediatelyInvalidatesRelevanceCache(t *testing.T) {
	for _, test := range []struct {
		name, path, content, provider, version string
	}{
		{
			name: "Dockerfile variant",
			path: "Dockerfile.dev", content: "FROM scratch\n",
			provider: "docker",
		},
		{
			name:     "installed Composer metadata",
			path:     "vendor/composer/installed.json",
			content:  `[{"name":"laravel/framework","pretty_version":"v12.6.0"}]`,
			provider: "laravel", version: "12.6.0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			engine := newStatusEngine(statusEngineOptions{})
			t.Cleanup(engine.Close)
			if got := engine.projectVersions(directory); len(got) != 0 {
				t.Fatalf("expected an initially irrelevant repository, got %v", got)
			}
			path := filepath.Join(directory, test.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}

			versions := engine.projectVersions(directory)
			if got, present := versions[test.provider]; !present || got != test.version {
				t.Fatalf("expected immediate %s=%q relevance after metadata change, got %q present=%v", test.provider, test.version, got, present)
			}
		})
	}
}

func TestStatusEngineRefreshIsAsynchronousAndPublishesImmediateMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	engine := newStatusEngine(statusEngineOptions{
		Run: func(context.Context, string, string, ...string) ([]byte, error) {
			<-release
			return []byte("# branch.head main\n? new.go\n"), nil
		},
	})
	t.Cleanup(engine.Close)

	started := time.Now()
	engine.Refresh(dir)
	if elapsed := time.Since(started); elapsed > 5*time.Millisecond {
		t.Fatalf("refresh blocked the input path for %s", elapsed)
	}
	immediateDeadline := time.Now().Add(100 * time.Millisecond)
	for engine.Snapshot().Directory == "" && time.Now().Before(immediateDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.Snapshot(); got.Directory != dir || got.Git.Branch != "" {
		t.Fatalf("expected immediate local metadata before the Git probe, got %+v", got)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := engine.Snapshot(); got.Directory == dir && got.Git.Branch == "main" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("status refresh was not published")
}

func TestStatusEngineCoalescesDuplicateRefreshes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var calls atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Run: func(context.Context, string, string, ...string) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("# branch.head main\n"), nil
	}})
	t.Cleanup(engine.Close)

	for range 20 {
		engine.Refresh(dir)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for engine.Snapshot().Directory == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got > 2 {
		t.Fatalf("expected duplicate refreshes to coalesce, got %d probes", got)
	}
}

func TestGitCacheServesStaleResultAndCommandRefreshInvalidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Run: func(ctx context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name != "git" {
			return nil, errors.New("unexpected command")
		}
		if calls.Add(1) == 1 {
			return []byte("# branch.head main\n1 .M N... 100644 100644 100644 a b changed.go\n"), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	waitForGitBranch(t, engine, "main")
	if got := engine.Snapshot().Git.Branch; got != "main" {
		t.Fatalf("expected initial branch, got %q", got)
	}

	engine.Refresh(dir)
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected fresh cache to avoid a duplicate probe, got %d", got)
	}
	engine.RefreshAfterCommand(dir)
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(350 * time.Millisecond)
	if got := engine.Snapshot().Git.Branch; got != "main" {
		t.Fatalf("expected stale branch after timeout, got %q", got)
	}
	if got := strings.Join(engine.Snapshot().StaleProviders, ","); got != "git" {
		t.Fatalf("expected timed-out cached Git data to be marked stale, got %q", got)
	}
}

func TestCommandCompletionDebouncesGitRefreshes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Run: func(context.Context, string, string, ...string) ([]byte, error) {
		calls.Add(1)
		return []byte("# branch.head main\n"), nil
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	waitForGitBranch(t, engine, "main")

	engine.RefreshAfterCommand(dir)
	engine.RefreshAfterCommand(dir)
	engine.RefreshAfterCommand(dir)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected the command burst to remain debounced, got %d Git probes", got)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected one coalesced post-command Git probe, got %d", got)
	}
}

func TestGitMetadataChangeInvalidatesCachedStatus(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Run: func(context.Context, string, string, ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("# branch.head main\n"), nil
		}
		return []byte("# branch.head feature/chatbox\n"), nil
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	waitForGitBranch(t, engine, "main")
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feature/chatbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.Refresh(dir)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if engine.Snapshot().Git.Branch == "feature/chatbox" && calls.Load() == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected metadata invalidation, calls=%d snapshot=%+v", calls.Load(), engine.Snapshot().Git)
}

func TestStatusEngineDoesNotSampleDisabledMetrics(t *testing.T) {
	var samples atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Sample: func() (float64, float64) {
		samples.Add(1)
		return 1, 2
	}})
	engine.StartMetrics(time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	engine.Close()
	if got := samples.Load(); got != 0 {
		t.Fatalf("expected disabled metrics to have zero sampling cost, got %d", got)
	}
}

func TestStatusEngineStopsMetricSamplingOnClose(t *testing.T) {
	var samples atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Metrics: true, Sample: func() (float64, float64) {
		value := samples.Add(1)
		return float64(value), 50
	}})
	engine.StartMetrics(2 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for samples.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	engine.Close()
	stoppedAt := samples.Load()
	time.Sleep(10 * time.Millisecond)
	if got := samples.Load(); got != stoppedAt {
		t.Fatalf("expected sampling to stop at %d, got %d", stoppedAt, got)
	}
}

func TestVersionProvidersAreRelevantOnlyAndNeverInvokeArtisan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "artisan"), []byte("#!/usr/bin/env php"), 0o755); err != nil {
		t.Fatal(err)
	}
	var commandsMu sync.Mutex
	var commands []string
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		commandsMu.Lock()
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		commandsMu.Unlock()
		return nil, errors.New("missing")
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	waitForStatusDirectory(t, engine, dir)

	if _, relevant := engine.Snapshot().Versions["laravel"]; !relevant {
		t.Fatal("expected artisan to make Laravel relevant")
	}
	commandsMu.Lock()
	defer commandsMu.Unlock()
	for _, command := range commands {
		if strings.Contains(command, "artisan") {
			t.Fatalf("Laravel provider must not invoke artisan, got %q", command)
		}
	}
}

func TestPythonRubyAndElixirProvidersResolveRelevantActiveVersions(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{
		"pyproject.toml": "[project]\nname = \"example\"\n",
		"Gemfile":        "source \"https://rubygems.org\"\n",
		"mix.exs":        "defmodule Example.MixProject do\nend\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		switch name {
		case "python3":
			return []byte("Python 3.13.5"), nil
		case "ruby":
			return []byte("ruby 3.4.4 (2025-05-14 revision a38531fd3f) [arm64-darwin]"), nil
		case "elixir":
			return []byte("Erlang/OTP 27\n\nElixir 1.18.4 (compiled with Erlang/OTP 27)"), nil
		default:
			return nil, errors.New("unexpected provider")
		}
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(directory)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		versions := engine.Snapshot().Versions
		if versions["python"] == "3.13.5" && versions["ruby"] == "3.4.4" && versions["elixir"] == "1.18.4" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected active Python, Ruby, and Elixir versions, got %v", engine.Snapshot().Versions)
}

func TestVersionManagerProbesAreScopedToProject(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(repository, "services", "first")
	second := filepath.Join(repository, "services", "second")
	for _, directory := range []string{first, second} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "pyproject.toml"), []byte("[project]\nname = \"example\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, directory, name string, _ ...string) ([]byte, error) {
		if name != "python3" {
			return nil, errors.New("unexpected provider")
		}
		if directory == first {
			return []byte("Python 3.12.11"), nil
		}
		return []byte("Python 3.13.5"), nil
	}})
	t.Cleanup(engine.Close)

	for directory, want := range map[string]string{first: "3.12.11", second: "3.13.5"} {
		engine.Refresh(directory)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			snapshot := engine.Snapshot()
			if snapshot.Directory == directory && snapshot.Versions["python"] == want {
				break
			}
			time.Sleep(time.Millisecond)
		}
		snapshot := engine.Snapshot()
		if snapshot.Directory != directory || snapshot.Versions["python"] != want {
			t.Fatalf("expected project-scoped Python %s in %s, got %+v", want, directory, snapshot)
		}
	}
}

func TestExecutableVersionProbeIsCachedAcrossRepositories(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, dir := range dirs {
		if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var composerCalls atomic.Int32
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name == "composer" {
			composerCalls.Add(1)
			return []byte("Composer version 2.8.9 2026-01-01"), nil
		}
		return nil, errors.New("missing")
	}})
	t.Cleanup(engine.Close)
	for _, dir := range dirs {
		engine.Refresh(dir)
		deadline := time.Now().Add(time.Second)
		for engine.Snapshot().Versions["composer"] != "2.8.9" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := engine.Snapshot().Versions["composer"]; got != "2.8.9" {
			t.Fatalf("expected resolved Composer version, got %q", got)
		}
	}
	if got := composerCalls.Load(); got != 1 {
		t.Fatalf("expected one process-wide Composer probe, got %d", got)
	}
}

func TestVersionRefreshRetainsLastValidValueWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "composer.json")
	if err := os.WriteFile(manifest, []byte(`{"require":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	failed := make(chan struct{})
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, _ string, name string, _ ...string) ([]byte, error) {
		if name != "composer" {
			return nil, errors.New("missing")
		}
		if calls.Add(1) == 1 {
			return []byte("Composer version 2.8.9 2026-01-01"), nil
		}
		<-release
		close(failed)
		return nil, errors.New("probe failed")
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(dir)
	deadline := time.Now().Add(time.Second)
	for engine.Snapshot().Versions["composer"] != "2.8.9" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.Snapshot().Versions["composer"]; got != "2.8.9" {
		t.Fatalf("expected initial Composer version, got %q", got)
	}
	engine.mu.Lock()
	engine.versions["composer"] = cachedStatusVersion{expires: time.Now().Add(-time.Second)}
	engine.mu.Unlock()
	if err := os.WriteFile(manifest, []byte(`{"require":{"php":"^8.4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	engine.Refresh(dir)
	refreshDeadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(refreshDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.Snapshot().Versions["composer"]; got != "2.8.9" {
		t.Fatalf("expected the last valid version during refresh, got %q", got)
	}
	close(release)
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for failed replacement probe")
	}
	time.Sleep(10 * time.Millisecond)
	if got := engine.Snapshot().Versions["composer"]; got != "2.8.9" {
		t.Fatalf("expected failed refresh to retain Composer 2.8.9, got %q", got)
	}
	if got := strings.Join(engine.Snapshot().StaleProviders, ","); got != "versions" {
		t.Fatalf("expected retained tool versions to be marked stale, got %q", got)
	}
}

func TestDirectoryChangeDoesNotCarryVersionsAcrossRepositories(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, directory := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(directory, "composer.json"), []byte(`{"require":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	engine := newStatusEngine(statusEngineOptions{Run: func(_ context.Context, directory, name string, _ ...string) ([]byte, error) {
		if name != "composer" {
			return nil, errors.New("missing")
		}
		if directory == first {
			return []byte("Composer version 2.8.9 2026-01-01"), nil
		}
		<-release
		return nil, errors.New("missing")
	}})
	t.Cleanup(engine.Close)
	engine.Refresh(first)
	deadline := time.Now().Add(time.Second)
	for engine.Snapshot().Versions["composer"] != "2.8.9" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.Snapshot().Versions["composer"]; got != "2.8.9" {
		t.Fatalf("expected first repository Composer version, got %q", got)
	}
	engine.mu.Lock()
	engine.versions["composer"] = cachedStatusVersion{expires: time.Now().Add(-time.Second)}
	engine.mu.Unlock()
	engine.Refresh(second)
	secondDeadline := time.Now().Add(time.Second)
	for engine.Snapshot().Directory != second && time.Now().Before(secondDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := engine.Snapshot().Versions["composer"]; got != "" {
		close(release)
		t.Fatalf("expected unresolved second repository not to inherit Composer %q", got)
	}
	close(release)
}

func TestVersionProbeTimeoutDoesNotBlockStatusPublication(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := newStatusEngine(statusEngineOptions{Run: func(ctx context.Context, _ string, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	t.Cleanup(engine.Close)
	started := time.Now()
	engine.Refresh(dir)
	deadline := time.Now().Add(time.Second)
	for {
		if _, cached := engine.cachedVersion("composer"); cached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for failed-probe backoff")
		}
		time.Sleep(time.Millisecond)
	}
	if elapsed := time.Since(started); elapsed > 600*time.Millisecond {
		t.Fatalf("bounded version probe took %s", elapsed)
	}
}

func waitForStatusDirectory(t *testing.T, engine *statusEngine, directory string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if engine.Snapshot().Directory == directory {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status for %s was not published", directory)
}

func waitForGitBranch(t *testing.T, engine *statusEngine, branch string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if engine.Snapshot().Git.Branch == branch {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Git branch %s was not published", branch)
}

func BenchmarkChatboxStatusRender(b *testing.B) {
	compositor := newTerminalCompositor(os.Stdout, "classic", "", 160, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left:  []string{"directory", "git-branch", "git-status", "git-added", "git-deleted", "versions"},
			Right: []string{"duration", "exit", "cpu", "memory"},
		},
		Colors: map[string]string{"directory": "#61ffcf", "git-branch": "#fd7df4", "php": "#777bb4"},
	})
	exitCode := 0
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/Users/developer/project", Git: gitStatusSnapshot{Branch: "main", Changed: 3, Modified: 3, Added: 2, Deleted: 1},
		Versions: map[string]string{"php": "8.4.1", "go": "1.26.0", "node": "24.1.0"},
		Duration: 250 * time.Millisecond, ExitCode: &exitCode, CPU: 12, Memory: 64,
	})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = compositor.inputBoxStatusLine()
	}
}

func TestCachedStatusRenderStaysWithinRegressionBudget(t *testing.T) {
	compositor := benchmarkStatusCompositor(160)
	t.Cleanup(compositor.Close)
	if allocations := testing.AllocsPerRun(1000, func() { _ = compositor.inputBoxStatusLines() }); allocations > 1 {
		t.Fatalf("cached status render allocated %.2f objects, budget is 1", allocations)
	}
	const iterations = 100_000
	started := time.Now()
	for range iterations {
		_ = compositor.inputBoxStatusLines()
	}
	if average := time.Since(started) / iterations; average > 100*time.Microsecond {
		t.Fatalf("cached status render averaged %s, budget is 100µs", average)
	}
}

func TestManagedPromptFirstDrawStaysWithinRegressionBudget(t *testing.T) {
	const iterations = 100
	started := time.Now()
	for range iterations {
		compositor := newTerminalCompositor(io.Discard, "bottom", "budget", 160, 24)
		compositor.SetInputBoxTheme(testInputBoxTheme())
		compositor.SetChatboxConfig(terminalChatboxConfig{
			Prompt: "› ", Separator: " · ", Status: terminalChatboxBarConfig{
				Left: []string{"directory", "git-branch"}, Right: []string{"exit"},
			},
		})
		compositor.WritePTY(terminalMarkerBytes("budget", "prompt-start"))
		compositor.WritePTY([]byte("› "))
		compositor.WritePTY(terminalMarkerBytes("budget", "prompt-end"))
		compositor.Close()
	}
	if average := time.Since(started) / iterations; average > 5*time.Millisecond {
		t.Fatalf("managed prompt first draw averaged %s, budget is 5ms", average)
	}
}

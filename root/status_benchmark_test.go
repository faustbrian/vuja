package root

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func BenchmarkStatusSegmentLayout(b *testing.B) {
	for _, width := range []int{40, 80, 160} {
		b.Run(strconv.Itoa(width), func(b *testing.B) {
			compositor := benchmarkStatusCompositor(width)
			defer compositor.Close()
			b.ReportAllocs()
			for range b.N {
				compositor.statusLinesCache = nil
				_ = compositor.inputBoxStatusLines()
			}
		})
	}
}

func BenchmarkStatusGitCacheLookup(b *testing.B) {
	engine := newStatusEngine(statusEngineOptions{})
	defer engine.Close()
	engine.storeGitStatus("/tmp/project/.git", gitStatusSnapshot{Branch: "main", Modified: 3})
	b.ReportAllocs()
	for range b.N {
		_, _ = engine.cachedGitStatus("/tmp/project/.git")
	}
}

func BenchmarkRepositoryRelevanceDetection(b *testing.B) {
	directory := b.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.com/app\n\ngo 1.26\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		_ = detectProjectVersions(directory)
	}
}

func BenchmarkFiniteLocalStatusProviders(b *testing.B) {
	directory := b.TempDir()
	if err := os.Mkdir(filepath.Join(directory, ".git"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(`{"name":"vuja","version":"1.2.3"}`), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".node-version"), []byte("24.4.0\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	shell := shellStatusSnapshot{VirtualEnv: filepath.Join(directory, ".venv")}
	b.ReportAllocs()
	for range b.N {
		_ = detectRepositoryStatus(directory, true)
		_ = detectProjectPackage(directory)
		_ = detectPinnedVersions(directory)
		_ = detectEnvironmentStatus(shell, directory)
	}
}

func BenchmarkCommandAwareContextGate(b *testing.B) {
	contexts := operationalContextSnapshot{
		Kubernetes: "staging", KubernetesNamespace: "payments",
		AWSProfile: "staging", AWSRegion: "eu-north-1", Docker: "colima",
	}
	b.ReportAllocs()
	for range b.N {
		_ = contextsForCommand("kubectl get pods", contexts)
		_ = contextsForCommand("git status", contexts)
	}
}

func BenchmarkStatusVersionCacheLookup(b *testing.B) {
	engine := newStatusEngine(statusEngineOptions{})
	defer engine.Close()
	engine.storeVersion("go", "1.26", time.Hour)
	b.ReportAllocs()
	for range b.N {
		_, _ = engine.cachedVersion("go")
	}
}

func BenchmarkSystemMetricSnapshotRender(b *testing.B) {
	compositor := benchmarkStatusCompositor(120)
	defer compositor.Close()
	b.ReportAllocs()
	for range b.N {
		_ = compositor.inputBoxStatusLines()
	}
}

func BenchmarkSystemMetricSample(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		_, _ = sampleSystemUsage()
	}
}

func BenchmarkStatusMetricRefresh(b *testing.B) {
	engine := newStatusEngine(statusEngineOptions{
		Metrics: true,
		Sample:  func() (float64, float64) { return 12, 42 },
	})
	defer engine.Close()
	b.ReportAllocs()
	for range b.N {
		engine.refreshMetrics()
	}
}

func BenchmarkPromptRedraw(b *testing.B) {
	for _, withStatus := range []bool{false, true} {
		name := "without-status"
		if withStatus {
			name = "with-status"
		}
		b.Run(name, func(b *testing.B) {
			for range b.N {
				compositor := newTerminalCompositor(io.Discard, "bottom", "benchmark", 120, 20)
				compositor.SetInputBoxTheme(testInputBoxTheme())
				if withStatus {
					compositor.SetChatboxConfig(terminalChatboxConfig{Separator: " · ", Status: terminalChatboxBarConfig{
						Left: []string{"directory", "git-branch"}, Right: []string{"exit"},
					}})
				}
				compositor.WritePTY(terminalMarkerBytes("benchmark", "prompt-start"))
				compositor.WritePTY([]byte("› git status"))
				compositor.WritePTY(terminalMarkerBytes("benchmark", "prompt-end"))
				compositor.Close()
			}
		})
	}
}

func BenchmarkPromptCompletionTransition(b *testing.B) {
	for range b.N {
		compositor := newTerminalCompositor(io.Discard, "bottom", "benchmark", 120, 20)
		compositor.SetInputBoxTheme(testInputBoxTheme())
		compositor.SetChatboxConfig(terminalChatboxConfig{
			Separator: " · ",
			Title: terminalChatboxBarConfig{
				Left:  []string{"directory"},
				Right: []string{"versions"},
			},
			Status: terminalChatboxBarConfig{
				Left:  []string{"git-branch", "git-status"},
				Right: []string{"duration", "exit", "cpu", "memory"},
			},
		})
		exitCode := 0
		compositor.SetStatusSnapshot(statusSnapshot{
			Directory: "/tmp/project",
			Git:       gitStatusSnapshot{Branch: "main", Modified: 3},
			Versions:  map[string]string{"go": "1.26.0"},
			CPU:       8,
			Memory:    42,
			HasCPU:    true,
			HasMemory: true,
			ExitCode:  &exitCode,
		})
		compositor.WritePTY(terminalMarkerBytes("benchmark", "prompt-start"))
		compositor.WritePTY([]byte("› ls"))
		compositor.WritePTY(terminalMarkerBytes("benchmark", "prompt-end"))
		compositor.WritePTY(terminalMarkerBytes("benchmark", "command-start"))
		compositor.WritePTY([]byte("\r\nresult\r\n"))
		compositor.WritePTY(terminalMarkerBytes("benchmark", "command-end:0"))
		compositor.Close()
	}
}

func benchmarkStatusCompositor(width int) *terminalCompositor {
	compositor := newTerminalCompositor(io.Discard, "classic", "", width, 10)
	compositor.SetInputBoxTheme(testInputBoxTheme())
	compositor.SetChatboxConfig(terminalChatboxConfig{
		Separator: " · ",
		Status: terminalChatboxBarConfig{
			Left:  []string{"directory", "git-branch", "git-status", "git-added", "git-deleted", "versions"},
			Right: []string{"duration", "exit", "cpu", "memory"},
		},
		Colors: map[string]string{"directory": "#61ffcf", "git-branch": "#fd7df4", "php": "#777bb4"},
	})
	exitCode := 1
	compositor.SetStatusSnapshot(statusSnapshot{
		Directory: "/Users/developer/project", Git: gitStatusSnapshot{Branch: "main", Modified: 3, Added: 2, Deleted: 1},
		Versions: map[string]string{"php": "8.4", "go": "1.26", "node": "24"},
		Duration: 38 * time.Millisecond, ExitCode: &exitCode, CPU: 8, Memory: 42,
	})
	return compositor
}

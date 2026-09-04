package root

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/config"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.0.0", "v1.0.0", false},
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.0", "v1.1.9", false},
		{"dev", "v1.0.0", false}, // dev never updates
		{"v1.0.0", "dev", false},
		{"", "v1.0.0", false},
		{"v1.0.0", "v1.1.0-nightly.8cb1f47", false}, // nightly never triggers update
		{"v1.1.0-nightly.abc", "v1.2.0", true},      // but if you are on nightly, you can update to stable
	}

	for _, tt := range tests {
		if got := IsNewer(tt.current, tt.latest); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v; want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestReleaseChannelsRespectSemanticPrereleaseBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		current string
		latest  string
		want    bool
	}{
		{name: "stable ignores release candidate", channel: "stable", current: "v1.0.0", latest: "v1.1.0-rc.1", want: false},
		{name: "stable promotes release candidate", channel: "stable", current: "v1.0.0-rc.2", latest: "v1.0.0", want: true},
		{name: "release candidate advances", channel: "rc", current: "v1.0.0-rc.1", latest: "v1.0.0-rc.2", want: true},
		{name: "release candidate promotes stable", channel: "rc", current: "v1.0.0-rc.2", latest: "v1.0.0", want: true},
		{name: "release candidate rejects nightly", channel: "rc", current: "v1.0.0-rc.2", latest: "v1.1.0-nightly.abc", want: false},
		{name: "nightly advances same base", channel: "nightly", current: "v1.1.0-nightly.abc", latest: "v1.1.0-nightly.def", want: true},
		{name: "invalid release rejected", channel: "nightly", current: "v1.0.0", latest: "latest", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isNewerForChannel(test.current, test.latest, test.channel); got != test.want {
				t.Fatalf("isNewerForChannel(%q, %q, %q) = %v; want %v", test.current, test.latest, test.channel, got, test.want)
			}
		})
	}
}

func TestSelectReleaseForChannel(t *testing.T) {
	releases := []releaseInfo{
		{TagName: "v2.0.0", Draft: true},
		{TagName: "v1.2.0-nightly.def", Prerelease: true},
		{TagName: "v1.2.0-rc.2", Prerelease: true},
		{TagName: "v1.1.0"},
		{TagName: "not-semver"},
	}

	for _, test := range []struct {
		channel string
		want    string
	}{
		{channel: "stable", want: "v1.1.0"},
		{channel: "rc", want: "v1.2.0-rc.2"},
		{channel: "nightly", want: "v1.2.0-nightly.def"},
	} {
		t.Run(test.channel, func(t *testing.T) {
			release, err := selectReleaseForChannel(releases, test.channel)
			if err != nil {
				t.Fatal(err)
			}
			if release.TagName != test.want {
				t.Fatalf("expected %s release %q, got %q", test.channel, test.want, release.TagName)
			}
		})
	}
}

func TestBackgroundUpdateFailureClosesItsResultChannel(t *testing.T) {
	results := make(chan updateResult, 1)
	done := make(chan struct{})
	go func() {
		runBackgroundUpdateCheck(results, func(chan<- updateResult) { panic("update failure") })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background update failure escaped its containment boundary")
	}
	if _, open := <-results; open {
		t.Fatal("expected failed background update check to close its result channel")
	}
}

func TestRunUpdateReturnsFetchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("VUJA_UPDATE_URL", server.URL)
	cfg := config.DefaultConfig()
	config.Init(cfg)

	var output bytes.Buffer
	executableCalled := false
	err := runUpdate(t.Context(), &output, server.Client(), func() (string, error) {
		executableCalled = true
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "update server") {
		t.Fatalf("expected update server failure, got %v", err)
	}
	if executableCalled {
		t.Fatal("expected update target resolution not to run after a fetch failure")
	}
}

func TestUpdateState(t *testing.T) {
	// Use a temporary directory for the state file
	tmpDir, err := os.MkdirTemp("", "vuja-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, ".local", "share"))

	state := config.LoadState()
	state.Updater.SeenVersion = "v1.0.0"
	state.Updater.LastCheckTime = time.Unix(123456789, 0)

	err = config.SaveState(state)
	if err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	loaded := config.LoadState()
	if loaded.Updater.SeenVersion != state.Updater.SeenVersion {
		t.Errorf("Expected SeenVersion %q, got %q", state.Updater.SeenVersion, loaded.Updater.SeenVersion)
	}
	if loaded.Updater.LastCheckTime.Unix() != state.Updater.LastCheckTime.Unix() {
		t.Errorf("Expected LastCheck %v, got %v", state.Updater.LastCheckTime, loaded.Updater.LastCheckTime)
	}
}

func TestInstallReleaseVerifiesChecksumAndReplacesBinary(t *testing.T) {
	archive := buildTestReleaseArchive(t, []byte("new binary"))
	checksum := fmt.Sprintf("%x  vuja_%s_%s.tar.gz\n", sha256.Sum256(archive), runtime.GOOS, runtime.GOARCH)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vuja.tar.gz":
			_, _ = w.Write(archive)
		case "/SHA256SUMS":
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "vuja")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	release := releaseInfo{
		TagName: "v1.2.3",
		Assets: []releaseAsset{
			{Name: fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), URL: server.URL + "/vuja.tar.gz"},
			{Name: "SHA256SUMS", URL: server.URL + "/SHA256SUMS"},
		},
	}

	if err := installRelease(context.Background(), server.Client(), release, target); err != nil {
		t.Fatalf("install release: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("expected verified replacement, got %q", got)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("expected executable mode 0755, info=%v err=%v", info, err)
	}
}

func TestInstallReleaseRejectsChecksumMismatchWithoutReplacingBinary(t *testing.T) {
	archive := buildTestReleaseArchive(t, []byte("untrusted binary"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vuja.tar.gz":
			_, _ = w.Write(archive)
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%064d  vuja_%s_%s.tar.gz\n", 0, runtime.GOOS, runtime.GOARCH)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "vuja")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	release := releaseInfo{
		Assets: []releaseAsset{
			{Name: fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), URL: server.URL + "/vuja.tar.gz"},
			{Name: "SHA256SUMS", URL: server.URL + "/SHA256SUMS"},
		},
	}

	err := installRelease(context.Background(), server.Client(), release, target)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old binary" {
		t.Fatalf("target changed after failed verification: %q", got)
	}
}

func buildTestReleaseArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "vuja",
		Mode: 0755,
		Size: int64(len(binary)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

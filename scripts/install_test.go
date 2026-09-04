package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type installerRelease struct {
	TagName    string                  `json:"tag_name"`
	Draft      bool                    `json:"draft"`
	Prerelease bool                    `json:"prerelease"`
	Assets     []installerReleaseAsset `json:"assets"`
}

type installerReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func TestInstallerSelectsExplicitReleaseChannel(t *testing.T) {
	t.Parallel()

	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := newInstallerReleaseServer(t, archiveName, []installerRelease{
		{TagName: "release", Prerelease: false},
		{TagName: "preview-nightly.abcdef", Prerelease: true},
		{TagName: "v1.2.0-nightly.abcdef", Prerelease: true},
		{TagName: "v1.2.0-rc.2", Prerelease: true},
		{TagName: "v1.1.0"},
	})

	for _, test := range []struct {
		name    string
		channel string
		want    string
	}{
		{name: "stable remains the default", want: "v1.1.0"},
		{name: "release candidate is explicit", channel: "rc", want: "v1.2.0-rc.2"},
		{name: "nightly is explicit", channel: "nightly", want: "v1.2.0-nightly.abcdef"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			installDir := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "install.sh")
			cmd.Dir = "."
			cmd.Env = append(installerTestEnvironment(),
				"BIN_DIR="+installDir,
				"HOME="+t.TempDir(),
				"VUJA_API_URL="+server.URL,
				"VUJA_CHANNEL="+test.channel,
			)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install %s channel: %v\n%s", test.channel, err, output)
			}

			installed, err := os.ReadFile(filepath.Join(installDir, "vuja"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(installed), test.want) {
				t.Fatalf("expected %s installer to select %s, got binary %q", test.channel, test.want, installed)
			}
		})
	}
}

func TestInstallerRejectsUnknownReleaseChannel(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(installerTestEnvironment(),
		"BIN_DIR="+t.TempDir(),
		"HOME="+t.TempDir(),
		"VUJA_API_URL=http://127.0.0.1:1",
		"VUJA_CHANNEL=preview",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected unknown release channel to be rejected, got output %s", output)
	}
	if !strings.Contains(string(output), "stable, rc, or nightly") {
		t.Fatalf("expected finite-channel guidance, got %s", output)
	}
}

func TestInstallerRejectsMalformedStableReleaseTag(t *testing.T) {
	t.Parallel()

	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := newInstallerReleaseServer(t, archiveName, []installerRelease{{TagName: "release"}})
	cmd := exec.CommandContext(t.Context(), "sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(installerTestEnvironment(),
		"BIN_DIR="+t.TempDir(),
		"HOME="+t.TempDir(),
		"VUJA_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected malformed stable tag to be rejected, got output %s", output)
	}
	if !strings.Contains(string(output), "invalid tag") {
		t.Fatalf("expected malformed tag guidance, got %s", output)
	}
}

func TestInstallerAuthenticatesGitHubAPIRequest(t *testing.T) {
	t.Parallel()

	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := newInstallerReleaseServer(t, archiveName, []installerRelease{{TagName: "v1.1.0"}}, "test-token")
	cmd := exec.CommandContext(t.Context(), "sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(installerTestEnvironment(),
		"BIN_DIR="+t.TempDir(),
		"GITHUB_TOKEN=test-token",
		"HOME="+t.TempDir(),
		"VUJA_API_URL="+server.URL,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install with authenticated API request: %v\n%s", err, output)
	}
}

func TestInstallerReleaseCandidateSelectsHighestSemanticVersion(t *testing.T) {
	t.Parallel()

	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := newInstallerReleaseServer(t, archiveName, []installerRelease{
		{TagName: "v1.1.1"},
		{TagName: "v1.2.0-rc.3", Prerelease: true},
		{TagName: "v1.1.0"},
	})
	installDir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(installerTestEnvironment(),
		"BIN_DIR="+installDir,
		"HOME="+t.TempDir(),
		"VUJA_API_URL="+server.URL,
		"VUJA_CHANNEL=rc",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install rc channel: %v\n%s", err, output)
	}
	installed, err := os.ReadFile(filepath.Join(installDir, "vuja"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "v1.2.0-rc.3") {
		t.Fatalf("expected highest eligible semantic version, got binary %q", installed)
	}
}

func TestInstallerPreservesExistingBinaryWhenReplacementCopyFails(t *testing.T) {
	t.Parallel()

	archiveName := fmt.Sprintf("vuja_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := newInstallerReleaseServer(t, archiveName, []installerRelease{{TagName: "v1.1.0"}})
	installDir := t.TempDir()
	target := filepath.Join(installDir, "vuja")
	if err := os.WriteFile(target, []byte("existing binary"), 0700); err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "cp"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", "install.sh")
	cmd.Dir = "."
	cmd.Env = append(installerTestEnvironment(),
		"BIN_DIR="+installDir,
		"HOME="+t.TempDir(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VUJA_API_URL="+server.URL,
	)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected failed replacement copy, got success\n%s", output)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected existing binary to remain after failed copy: %v", err)
	}
	if string(content) != "existing binary" {
		t.Fatalf("existing binary changed after failed copy: %q", content)
	}
}

func installerTestEnvironment() []string {
	blocked := map[string]struct{}{
		"VUJA_PID": {}, "VUJA_FD": {}, "VUJA_HISTORY_ACK_FD": {},
		"VUJA_IS_CHILD": {}, "VUJA_MANAGED_PROMPT": {},
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, skip := blocked[name]; !skip {
			environment = append(environment, entry)
		}
	}
	return environment
}

func newInstallerReleaseServer(t *testing.T, archiveName string, releases []installerRelease, expectedToken ...string) *httptest.Server {
	t.Helper()

	archives := make(map[string][]byte, len(releases))
	for index := range releases {
		release := &releases[index]
		archive := installerArchive(t, release.TagName)
		archives[release.TagName] = archive
		release.Assets = []installerReleaseAsset{
			{Name: archiveName, URL: "/assets/" + release.TagName + "/" + archiveName},
			{Name: "SHA256SUMS", URL: "/assets/" + release.TagName + "/SHA256SUMS"},
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if len(expectedToken) > 0 && strings.HasPrefix(request.URL.Path, "/repos/") &&
			request.Header.Get("Authorization") != "Bearer "+expectedToken[0] {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		switch request.URL.Path {
		case "/repos/faustbrian/vuja/releases/latest":
			_ = encoder.Encode(releases[len(releases)-1])
		case "/repos/faustbrian/vuja/releases":
			_ = encoder.Encode(releases)
		default:
			parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/assets/"), "/")
			if len(parts) != 2 {
				http.NotFound(writer, request)
				return
			}
			archive, ok := archives[parts[0]]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", "application/octet-stream")
			if parts[1] == "SHA256SUMS" {
				_, _ = fmt.Fprintf(writer, "%x  %s\n", sha256.Sum256(archive), archiveName)
				return
			}
			_, _ = writer.Write(archive)
		}
	}))
	t.Cleanup(server.Close)

	for index := range releases {
		for assetIndex := range releases[index].Assets {
			releases[index].Assets[assetIndex].URL = server.URL + releases[index].Assets[assetIndex].URL
		}
	}
	return server
}

func installerArchive(t *testing.T, version string) []byte {
	t.Helper()

	binary := []byte("#!/bin/sh\n# " + version + "\nexit 0\n")
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "vuja", Mode: 0755, Size: int64(len(binary))}); err != nil {
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

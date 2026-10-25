package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const qualificationSource = "1234567890abcdef1234567890abcdef12345678"

func TestReleaseQualificationInstallsPinnedArchiveAndModule(t *testing.T) {
	fixture := newQualificationFixture(t, "linux/amd64", "v2.0.0-rc.1")
	output, err := fixture.run(t, "--go-install")
	if err != nil {
		t.Fatalf("qualify fixture: %v\n%s", err, output)
	}
	for _, want := range []string{"archive version: vuja v2.0.0-rc.1", "module version: vuja v2.0.0-rc.1", "qualification passed", "cleanup passed"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in %s", want, output)
		}
	}
	fixture.assertClean(t)
	if output, err := fixture.run(t); err != nil {
		t.Fatalf("fresh retry: %v\n%s", err, output)
	}
	fixture.assertClean(t)
}

func TestReleaseQualificationNativeTargetsAndStableTag(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			fixture := newQualificationFixture(t, platform, "v2.0.0")
			output, err := fixture.run(t)
			if err != nil || !strings.Contains(output, "archive version: vuja v2.0.0") {
				t.Fatalf("fixture platform routing failed: %v\n%s", err, output)
			}
			fixture.assertClean(t)
		})
	}
}

func TestReleaseQualificationRejectsInvalidDistribution(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		executed    bool
		change      func(*testing.T, *qualificationFixture)
	}{
		{"nightly", "arguments", false, func(_ *testing.T, f *qualificationFixture) { f.tag = "v2.0.0-nightly.abcdef" }},
		{"malformed tag", "arguments", false, func(_ *testing.T, f *qualificationFixture) { f.tag = "v02.0.0" }},
		{"malformed source", "arguments", false, func(_ *testing.T, f *qualificationFixture) { f.source = "main" }},
		{"unknown platform", "arguments", false, func(_ *testing.T, f *qualificationFixture) { f.platform = "windows/amd64" }},
		{"wrong native platform", "native-platform", false, func(_ *testing.T, f *qualificationFixture) { f.native = "darwin/arm64" }},
		{"download", "download", false, func(t *testing.T, f *qualificationFixture) { f.marker(t, "fail-download") }},
		{"missing checksum entry", "checksum", false, func(t *testing.T, f *qualificationFixture) {
			f.write(t, filepath.Join(f.assets, "SHA256SUMS"), strings.Repeat("0", 64)+"  other.tar.gz\n", 0600)
		}},
		{"duplicate checksum entry", "checksum", false, func(t *testing.T, f *qualificationFixture) {
			path := filepath.Join(f.assets, "SHA256SUMS")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f.write(t, path, string(body)+string(body), 0600)
		}},
		{"wrong checksum", "checksum", false, func(t *testing.T, f *qualificationFixture) {
			f.write(t, filepath.Join(f.assets, "SHA256SUMS"), strings.Repeat("0", 64)+"  vuja_linux_amd64.tar.gz\n", 0600)
		}},
		{"attestation", "provenance", false, func(t *testing.T, f *qualificationFixture) { f.marker(t, "fail-attestation") }},
		{"wrong authenticated source", "provenance", false, func(_ *testing.T, f *qualificationFixture) { f.source = strings.Repeat("0", 40) }},
		{"missing notice", "archive-install", false, func(t *testing.T, f *qualificationFixture) { f.archive(t, "LICENSE", false) }},
		{"archive link", "archive-install", false, func(t *testing.T, f *qualificationFixture) { f.archive(t, "", true) }},
		{"wrong archive version", "archive-version", true, func(t *testing.T, f *qualificationFixture) {
			f.tag = "v2.0.0-rc.2"
			f.archive(t, "", false)
			f.tag = "v2.0.0-rc.1"
		}},
		{"module install", "module-install", true, func(t *testing.T, f *qualificationFixture) { f.marker(t, "fail-module") }},
		{"module metadata", "module-metadata", true, func(t *testing.T, f *qualificationFixture) { f.marker(t, "bad-module-metadata") }},
		{"module version", "module-version", true, func(t *testing.T, f *qualificationFixture) {
			f.write(t, filepath.Join(f.assets, "module-vuja"), qualificationBinary("dev"), 0700)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newQualificationFixture(t, "linux/amd64", "v2.0.0-rc.1")
			tc.change(t, &fixture)
			output, err := fixture.run(t, "--go-install")
			if err == nil || !strings.Contains(output, "qualification failed at "+tc.stage) || strings.Contains(output, "qualification passed") {
				t.Fatalf("expected rejection at %s, got %v\n%s", tc.stage, err, output)
			}
			calls, readErr := os.ReadFile(fixture.calls)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if !tc.executed && strings.Contains(string(calls), "executed ") {
				t.Fatalf("unverified bytes executed: %s", calls)
			}
			fixture.assertClean(t)
		})
	}
}

func (f qualificationFixture) marker(t *testing.T, name string) {
	t.Helper()
	f.write(t, filepath.Join(f.assets, name), "reject\n", 0600)
}

type qualificationFixture struct {
	root, bin, runner, assets, calls, tag, platform, native, source string
}

func newQualificationFixture(t *testing.T, platform, tag string) qualificationFixture {
	t.Helper()
	root := t.TempDir()
	f := qualificationFixture{root: root, bin: filepath.Join(root, "bin"), runner: filepath.Join(root, "runner"), assets: filepath.Join(root, "assets"), calls: filepath.Join(root, "calls"), tag: tag, platform: platform, native: platform, source: qualificationSource}
	for _, path := range []string{f.bin, f.runner, f.assets} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, filepath.Join(root, "sentinel"), "outside private state\n", 0600)
	f.archive(t, "", false)
	f.write(t, filepath.Join(f.bin, "uname"), `#!/bin/sh
case "$1" in
  -s) case "$FIXTURE_PLATFORM" in linux/*) echo Linux;; darwin/*) echo Darwin;; esac;;
  -m) case "$FIXTURE_PLATFORM" in */amd64) echo x86_64;; */arm64) echo arm64;; esac;;
  *) exit 91;;
esac
`, 0700)
	// These leaf tools substitute network/native execution only. Real tar,
	// SHA-256, private installation and cleanup run in the shared verifier.
	f.write(t, filepath.Join(f.bin, "gh"), `#!/bin/bash
set -eu
printf 'gh %s\n' "$*" >> "$FIXTURE_CALLS"
case "$1 $2" in
  'release download')
    test "$3" = "$FIXTURE_TAG"
    shift 3
    while test "$#" -gt 0; do
      case "$1" in
        --repo) test "$2" = faustbrian/vuja; shift 2;;
        --pattern) shift 2;;
        --dir) destination=$2; shift 2;;
        *) exit 92;;
      esac
    done
    test ! -f "$FIXTURE_ASSETS/fail-download"
    cp "$FIXTURE_ASSETS/"* "$destination/"
    ;;
  'attestation verify')
    test ! -f "$FIXTURE_ASSETS/fail-attestation"
    test -s "$3"
    shift 3
    while test "$#" -gt 0; do
      case "$1" in
        --repo) test "$2" = faustbrian/vuja; repo=yes; shift 2;;
        --signer-workflow) test "$2" = faustbrian/vuja/.github/workflows/release.yml; workflow=yes; shift 2;;
        --source-digest) test "$2" = 1234567890abcdef1234567890abcdef12345678; source=yes; shift 2;;
        --signer-digest) test "$2" = 1234567890abcdef1234567890abcdef12345678; signer=yes; shift 2;;
        --source-ref) test "$2" = "refs/tags/$FIXTURE_TAG"; ref=yes; shift 2;;
        --deny-self-hosted-runners) hosted=yes; shift;;
        --format) test "$2" = json; shift 2;;
        *) exit 93;;
      esac
    done
    test "${repo-}${workflow-}${source-}${signer-}${ref-}${hosted-}" = yesyesyesyesyesyes
    echo '{"fixture":"not cryptographic proof"}'
    ;;
  *) exit 94;;
esac
`, 0700)
	f.write(t, filepath.Join(f.bin, "go"), `#!/bin/bash
set -eu
printf 'go %s\n' "$*" >> "$FIXTURE_CALLS"
case "$1" in
  install)
    test "$#" = 2
    test "$2" = "github.com/faustbrian/vuja/v2/cmd/vuja@$FIXTURE_TAG"
    test "$GOWORK" = off && test "$GOENV" = off
    test "$GOSUMDB" = sum.golang.org
    test "$PWD" != "$FIXTURE_CHECKOUT"
    mkdir -p "$GOMODCACHE/readonly"
    chmod 0500 "$GOMODCACHE/readonly"
    test ! -f "$FIXTURE_ASSETS/fail-module"
    cp "$FIXTURE_ASSETS/module-vuja" "$GOBIN/vuja"
    chmod 0700 "$GOBIN/vuja"
    ;;
  version)
    test "$2" = -m && test -f "$3"
    metadata_tag=$FIXTURE_TAG
    if test -f "$FIXTURE_ASSETS/bad-module-metadata"; then metadata_tag=v1.0.0; fi
    printf '\tpath\tgithub.com/faustbrian/vuja/v2/cmd/vuja\n\tmod\tgithub.com/faustbrian/vuja/v2\t%s\th1:fixture\n' "$metadata_tag"
    ;;
  *) exit 95;;
esac
`, 0700)
	f.write(t, filepath.Join(f.assets, "module-vuja"), qualificationBinary(tag), 0700)
	return f
}

func qualificationBinary(tag string) string {
	return `#!/bin/sh
set -eu
test "$#" = 1 && test "$1" = version
test "${VUJA_PID-unset}" = unset && test "${CODEX_HOME-unset}" = unset
test "${TMUX-unset}" = unset && test "${ITERM_SESSION_ID-unset}" = unset
test "$HOME" != "$FIXTURE_ROOT" && test -d "$HOME"
test "$VUJA_IS_CHILD" = true
test ! -f "$HOME/previous-run"
touch "$HOME/previous-run"
printf 'executed %s\n' "$0" >> "$FIXTURE_CALLS"
printf 'vuja ` + tag + `\n'
`
}

func (f qualificationFixture) archive(t *testing.T, omit string, symlink bool) {
	t.Helper()
	var buffer bytes.Buffer
	z := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(z)
	for _, name := range []string{"vuja", "LICENSE", "THIRD_PARTY_NOTICES.md"} {
		if name == omit {
			continue
		}
		body := "fixture notice\n"
		if name == "vuja" {
			body = qualificationBinary(f.tag)
		}
		header := &tar.Header{Name: name, Mode: 0755, Size: int64(len(body))}
		if symlink && name == "vuja" {
			header.Typeflag, header.Linkname, header.Size, body = tar.TypeSymlink, "/usr/bin/true", 0, ""
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	name := "vuja_" + strings.ReplaceAll(f.platform, "/", "_") + ".tar.gz"
	f.write(t, filepath.Join(f.assets, name), buffer.String(), 0600)
	f.write(t, filepath.Join(f.assets, "SHA256SUMS"), fmt.Sprintf("%x  %s\n", sha256.Sum256(buffer.Bytes()), name), 0600)
}

func (f qualificationFixture) write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func (f qualificationFixture) run(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	checkout, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"qualify-release.sh", f.tag, f.source, f.platform}, extra...)
	cmd := exec.CommandContext(t.Context(), "/bin/bash", args...)
	cmd.Env = []string{
		"PATH=" + f.bin + ":/usr/bin:/bin", "HOME=" + f.root,
		"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted", "RUNNER_TEMP=" + f.runner,
		"FIXTURE_ROOT=" + f.root, "FIXTURE_ASSETS=" + f.assets, "FIXTURE_CALLS=" + f.calls,
		"FIXTURE_TAG=" + f.tag, "FIXTURE_PLATFORM=" + f.native, "FIXTURE_CHECKOUT=" + checkout,
		"VUJA_PID=not-a-real-pid", "CODEX_HOME=not-a-real-home", "TMUX=not-a-real-pane", "ITERM_SESSION_ID=not-a-real-pane",
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (f qualificationFixture) assertClean(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.runner)
	if err != nil || len(entries) != 0 {
		t.Errorf("private runner storage not cleaned: %v %v", entries, err)
	}
	text, err := os.ReadFile(filepath.Join(f.root, "sentinel"))
	if err != nil || string(text) != "outside private state\n" {
		t.Errorf("outside state changed: %q %v", text, err)
	}
}

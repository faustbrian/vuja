package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
	"github.com/faustbrian/vuja/tests/providerfixture"
	"golang.org/x/sys/unix"
)

func TestSSHDiscoveryIncludesQuotedFilesAndOmitsComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(dir, "parts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"config":        "Include parts/*.conf \"extra hosts\"\nHost final # not-a-host\n",
		"parts/02.conf": "Host beta alpha *.wild !negative\n",
		"parts/01.conf": "Host = \"alpha\"\n",
		"extra hosts":   "Host gamma\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	results := sshHostGenerator(t.Context(), []string{"ssh", ""}, "ssh", "")
	var found []string
	for _, result := range results {
		// A machine's system configuration is independent of this user fixture.
		if result.Cmd == "alpha" || result.Cmd == "beta" || result.Cmd == "gamma" || result.Cmd == "final" || result.Cmd == "#" || result.Cmd == "not-a-host" {
			found = append(found, result.Cmd)
		}
	}
	if want := []string{"alpha", "beta", "gamma", "final"}; !reflect.DeepEqual(found, want) {
		t.Fatalf("expected lexically included production host inventory %v, got %v", want, found)
	}
}

func sshFixture(t testing.TB, files map[string]string) (string, []sshConfigRoot) {
	t.Helper()
	home := t.TempDir()
	for name, data := range files {
		path := filepath.Join(home, ".ssh", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, []sshConfigRoot{{filepath.Join(home, ".ssh", "config"), filepath.Join(home, ".ssh"), true}}
}

func sshNames(results []spec.Suggestion) []string {
	var names []string
	for _, result := range results {
		names = append(names, result.Cmd)
	}
	return names
}

func TestSSHDiscoveryCachesAndInvalidatesGraphChanges(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{"config": "Include parts/*.conf\n", "parts/a.conf": "Host alpha\n"})
	var cache sshHostInventory
	read := func(want []string) {
		t.Helper()
		if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, want) {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
	read([]string{"alpha"})
	// A caller cannot mutate the shared cached inventory.
	cache.load(t.Context(), home, roots)[0].Cmd = "not-a-real-host"
	read([]string{"alpha"})
	path := filepath.Join(home, ".ssh", "parts", "a.conf")
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Host bravo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	read([]string{"bravo"})
	if err := os.WriteFile(filepath.Join(home, ".ssh", "parts", "b.conf"), []byte("Host charlie\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read([]string{"bravo", "charlie"})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	read([]string{"charlie"})
	if err := os.WriteFile(roots[0].path, []byte("Host root-changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read([]string{"root-changed"})
}

func TestSSHDiscoveryUsesOriginBaseAndNeverExecutesConditions(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":       "Include nested/first\nMatch exec \"touch NEVER-CREATED\"\nInclude conditional\nHost final\n",
		"nested/first": "Include second\n",
		"second":       "Host from-user-base\nInclude config\n",
		"conditional":  "Host condition-inventory\n",
	})
	configData, err := os.ReadFile(roots[0].path)
	if err != nil {
		t.Fatal(err)
	}
	configData = []byte(strings.ReplaceAll(string(configData), "touch NEVER-CREATED", "touch "+filepath.Join(home, "NEVER-CREATED")))
	if err := os.WriteFile(roots[0].path, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	system := t.TempDir()
	for name, data := range map[string]string{"config": "Include child\n", "child": "Host from-system-base\n"} {
		if err := os.WriteFile(filepath.Join(system, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	roots = append(roots, sshConfigRoot{filepath.Join(system, "config"), system, false})
	var cache sshHostInventory
	want := []string{"from-user-base", "condition-inventory", "final", "from-system-base"}
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if _, err := os.Stat(filepath.Join(home, "NEVER-CREATED")); !os.IsNotExist(err) {
		t.Fatal("a Match condition must never execute")
	}
}

func TestSSHDiscoveryExpandsLocalPathsAndRefreshesEnvironment(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":   "Include ~/.ssh/expanded ${VUJA_SSH_TEST_INCLUDE} %d/.ssh/expanded\n",
		"expanded": "Host tilde-host\n",
		"one":      "Host env-one\n", "two": "Host env-two\n",
	})
	t.Setenv("VUJA_SSH_TEST_INCLUDE", filepath.Join(home, ".ssh", "one"))
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"tilde-host", "env-one"}) {
		t.Fatal(got)
	}
	t.Setenv("VUJA_SSH_TEST_INCLUDE", filepath.Join(home, ".ssh", "two"))
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"tilde-host", "env-two"}) {
		t.Fatal(got)
	}
}

func TestSSHDiscoveryRejectsNonregularOversizedAndUnsafeInputs(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":        "Include missing fifo oversized parts/*\nHost -option *.wild !negative good # ignored\nHost \"bad;command\" \"bad\\nline\"\nHost \"unclosed\n",
		"oversized":     strings.Repeat(" ", sshFileBytes+1),
		"parts/visible": "Host visible\n",
		"parts/.hidden": "Host should-stay-hidden\n",
	})
	if err := unix.Mkfifo(filepath.Join(home, ".ssh", "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"visible", "good"}) {
		t.Fatalf("unexpected inventory %v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := cache.load(ctx, home, roots); len(got) != 0 {
		t.Fatal("canceled discovery returned suggestions")
	}
}

func TestSSHDiscoveryBoundsCyclesDepthFilesBytesAndHosts(t *testing.T) {
	for _, mode := range []string{"files", "depth", "hosts", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string]string{"config": ""}
			want := 0
			switch mode {
			case "files":
				files["config"] = "Include parts/*\n"
				for i := 0; i < sshConfigFiles+2; i++ {
					files[fmt.Sprintf("parts/%03d", i)] = fmt.Sprintf("Host host-%03d\n", i)
				}
				want = sshConfigFiles - 1
			case "depth":
				for i := 0; i < sshConfigDepth+2; i++ {
					files[fmt.Sprintf("depth-%d", i)] = fmt.Sprintf("Host level-%d\nInclude depth-%d\n", i, i+1)
				}
				files["config"] = "Include depth-0\n"
				want = sshConfigDepth - 1
			case "hosts":
				for i := 0; i < sshHostLimit+2; i++ {
					files["config"] += fmt.Sprintf("Host host-%d\n", i)
				}
				want = sshHostLimit
			case "bytes":
				files["config"] = "Include parts/*\n"
				for i := 0; i < 20; i++ {
					files[fmt.Sprintf("parts/%02d", i)] = strings.Repeat("#padding\n", 25000) + fmt.Sprintf("Host byte-%d\n", i)
				}
				// Root bytes consume part of the global budget. Nine full child files fit.
				want = 9
			}
			home, roots := sshFixture(t, files)
			var cache sshHostInventory
			ctx, observations := providerfixture.Observe(t, "inventory")
			got := cache.load(ctx, home, roots)
			if len(got) != want {
				t.Fatalf("%s limit want %d hosts, got %d", mode, want, len(got))
			}
			providerfixture.Check(t, *observations, "ssh-hosts", health.Failed)
			for i, result := range got {
				name := fmt.Sprintf("host-%d", i)
				switch mode {
				case "files":
					name = fmt.Sprintf("host-%03d", i)
				case "depth":
					name = fmt.Sprintf("level-%d", i)
				case "bytes":
					name = fmt.Sprintf("byte-%d", i)
				}
				if result.Cmd != name {
					t.Fatalf("%s retained prefix row %d want %s got %s", mode, i, name, result.Cmd)
				}
			}
		})
	}
}

func BenchmarkSSHInventory(b *testing.B) {
	files := map[string]string{"config": "Include parts/*\n"}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("parts/%02d", i)] = fmt.Sprintf("Host host-%d\n", i)
	}
	home, roots := sshFixture(b, files)
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var cache sshHostInventory
			if len(cache.load(b.Context(), home, roots)) != 20 {
				b.Fatal("incomplete inventory")
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		var cache sshHostInventory
		cache.load(b.Context(), home, roots)
		b.ReportAllocs()
		for b.Loop() {
			if len(cache.load(b.Context(), home, roots)) != 20 {
				b.Fatal("incomplete inventory")
			}
		}
	})
}

func TestSSHDiscoveryUsesPOSIXNegatedGlobClasses(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":      "Include parts/[!a]*\n",
		"parts/alpha": "Host excluded-alpha\n",
		"parts/bravo": "Host included-bravo\n",
	})
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"included-bravo"}) {
		t.Fatalf("POSIX negated class returned %v", got)
	}
}

func TestSSHDiscoveryDoesNotBroadenEscapedLiteralGlob(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":                   "Include parts/literal\\*.conf\n",
		"parts/literal*.conf":      "Host literal-file\n",
		"parts/literal-other.conf": "Host unrelated-file\n",
	})
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"literal-file"}) {
		t.Fatalf("escaped literal glob returned %v", got)
	}
}

func TestSSHDiscoveryInvalidatesEverySymlinkAlias(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config": "Include a b\n", "shared": "Host shared-host\n", "changed": "Host changed-host\n",
	})
	dir := filepath.Join(home, ".ssh")
	for _, alias := range []string{"a", "b"} {
		if err := os.Symlink("shared", filepath.Join(dir, alias)); err != nil {
			t.Fatal(err)
		}
	}
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"shared-host"}) {
		t.Fatal(got)
	}
	if err := os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("changed", filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"shared-host", "changed-host"}) {
		t.Fatalf("retargeted duplicate alias returned %v", got)
	}
}

func TestSSHDiscoverySupportsQuotedPathsAndPortableBracketClasses(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":       "Include 'quoted path' parts/[[:digit:]]* literal/[]]* literal/[a-]*\n",
		"quoted path":  "Host quoted-alias\n",
		"parts/1-file": "Host digit-alias\n", "parts/z-file": "Host excluded-letter\n",
		"literal/]file": "Host bracket-alias\n", "literal/a-file": "Host range-alias\n", "literal/-file": "Host hyphen-alias\n",
	})
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"quoted-alias", "digit-alias", "bracket-alias", "hyphen-alias", "range-alias"}) {
		t.Fatalf("quoted/class inventory %v", got)
	}
}

func TestSSHDiscoveryServesTransferConsumers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("Host vuja-transfer-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ssh", "scp", "rsync", "sftp"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			provider := spec.Registry[name].Generator
			for {
				if provider != nil {
					for _, result := range provider(ctx, []string{name, ""}, name, "") {
						if result.Cmd == "vuja-transfer-fixture" {
							// Prepared lookup avoids shell-alias discovery; it exercises
							// production argument mapping without starting a shell.
							for _, mapped := range spec.LookupContextPrepared(ctx, name+" vuja-transfer-") {
								if mapped.Cmd == name+" vuja-transfer-fixture" {
									return
								}
							}
							t.Fatal("configured alias was not mapped into its command context")
							return
						}
					}
				}
				if provider == nil || ctx.Err() != nil {
					t.Fatal("production consumer does not discover the configured host")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestSSHDiscoveryResolvesParentsAfterSymlinks(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{
		"config":                      "Include parts/link/../hosts parts/*/../hosts\n",
		"parts/hosts":                 "Host wrong-lexical-parent\n",
		"elsewhere/hosts":             "Host filesystem-parent\n",
		"elsewhere/child/placeholder": "",
	})
	if err := os.Symlink("../elsewhere/child", filepath.Join(home, ".ssh", "parts", "link")); err != nil {
		t.Fatal(err)
	}
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"filesystem-parent"}) {
		t.Fatalf("Include symlink/parent resolution returned %v", got)
	}
}

type sshEditedDuringRead struct {
	*os.File
	edit func() error
}

func (f *sshEditedDuringRead) Read(data []byte) (int, error) {
	n, err := f.File.Read(data)
	if err == io.EOF && f.edit != nil {
		edit := f.edit
		f.edit = nil
		if editErr := edit(); editErr != nil {
			return n, editErr
		}
	}
	return n, err
}

func TestSSHDiscoveryRejectsSnapshotEditedDuringAcquisition(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{"config": "Host alpha\n"})
	f, err := os.Open(roots[0].path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &sshEditedDuringRead{File: f, edit: func() error {
		if err := os.WriteFile(roots[0].path, []byte("Host bravo\n"), 0o600); err != nil {
			return err
		}
		return os.Chtimes(roots[0].path, before.ModTime(), before.ModTime())
	}}
	data, after, err := readSSHConfigSnapshot(reader, before, sshFileBytes)
	if !errors.Is(err, errSSHConfigUnstable) || after != nil {
		t.Fatalf("changed file was accepted as a cacheable snapshot: bytes=%q error=%v metadata=%v", data, err, after != nil)
	}
	var cache sshHostInventory
	if got := sshNames(cache.load(t.Context(), home, roots)); !reflect.DeepEqual(got, []string{"bravo"}) {
		t.Fatal(got)
	}
}

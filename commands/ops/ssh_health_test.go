package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/tests/providerfixture"
)

func TestSSHHealthReportsDiscoveryWithoutLosingPartialAliases(t *testing.T) {
	for _, mode := range []string{"inventory", "empty", "absent", "unreadable", "oversized", "partial", "cancelled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string]string{"config": "Host fixture-host\n"}
			want := []string{"fixture-host"}
			outcome := health.Healthy
			switch mode {
			case "empty":
				files["config"], want = "# no configured aliases\n", nil
			case "absent":
				files, want, outcome = nil, nil, health.Skipped
			case "unreadable":
				files["config"], want, outcome = "fixture", nil, health.Failed
			case "oversized":
				files["config"], want, outcome = strings.Repeat("#", sshFileBytes+1), nil, health.Failed
			case "partial":
				files["config"], outcome = "Host fixture-host\nInclude nonregular\n", health.Failed
			case "cancelled":
				want, outcome = nil, health.Unknown
			case "expired":
				want, outcome = nil, health.TimedOut
			}
			home, roots := sshFixture(t, files)
			if mode == "unreadable" {
				// ENOTDIR is deterministic even for privileged test runners.
				roots[0].path = filepath.Join(roots[0].path, "child")
			} else if mode == "partial" {
				if err := os.Mkdir(filepath.Join(home, ".ssh", "nonregular"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, values := providerfixture.Observe(t, mode)
			var cache sshHostInventory
			if got := sshNames(cache.load(ctx, home, roots)); !reflect.DeepEqual(got, want) {
				t.Fatalf("discovery result policy changed: want %v, got %v", want, got)
			}
			providerfixture.Check(t, *values, "ssh-hosts", outcome)
		})
	}
}

func TestSSHHealthReportsLimitsOnlyWhenAliasesAreOmitted(t *testing.T) {
	for _, mode := range []string{"hosts-exact", "hosts-over", "files-exact", "files-over", "files-exact-absent-root"} {
		t.Run(mode, func(t *testing.T) {
			files := map[string]string{"config": ""}
			count := sshHostLimit
			if strings.HasPrefix(mode, "hosts") {
				limit := sshHostLimit
				if mode == "hosts-over" {
					limit++
				}
				for index := 0; index < limit; index++ {
					files["config"] += fmt.Sprintf("Host fixture-%d\n", index)
				}
			} else {
				files["config"] = "Include parts/*\n"
				count = sshConfigFiles - 1 // the root consumes one file
				limit := count
				if mode == "files-over" {
					limit++
				}
				for index := 0; index < limit; index++ {
					files[fmt.Sprintf("parts/%03d", index)] = fmt.Sprintf("Host fixture-%03d\n", index)
				}
			}
			home, roots := sshFixture(t, files)
			if mode == "files-exact-absent-root" {
				roots = append(roots, sshConfigRoot{filepath.Join(home, "absent-system-config"), home, false})
			}
			var cache sshHostInventory
			ctx, values := providerfixture.Observe(t, "inventory")
			if got := cache.load(ctx, home, roots); len(got) != count {
				t.Fatalf("retained alias prefix changed: want %d, got %d", count, len(got))
			}
			want := health.Healthy
			if strings.HasSuffix(mode, "over") {
				want = health.Failed
			}
			providerfixture.Check(t, *values, "ssh-hosts", want)
		})
	}
}

func TestSSHHealthCacheReuseIsQuietAndRefreshCanRecover(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{"config": "Host first\n"})
	var cache sshHostInventory
	ctx, values := providerfixture.Observe(t, "inventory")
	if got := sshNames(cache.load(ctx, home, roots)); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatal(got)
	}
	providerfixture.Check(t, *values, "ssh-hosts", health.Healthy)
	*values = nil
	cache.load(ctx, home, roots)
	providerfixture.Check(t, *values, "ssh-hosts", health.Unknown)
	if err := os.WriteFile(roots[0].path, []byte(strings.Repeat("#", sshFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.load(ctx, home, roots)
	providerfixture.Check(t, *values, "ssh-hosts", health.Failed)
	*values = nil
	if err := os.WriteFile(roots[0].path, []byte("Host recovered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sshNames(cache.load(ctx, home, roots)); !reflect.DeepEqual(got, []string{"recovered"}) {
		t.Fatal(got)
	}
	providerfixture.Check(t, *values, "ssh-hosts", health.Healthy)
}

func TestSSHHealthFinalSnapshotReadFailureRemainsFailedUntilFreshRepair(t *testing.T) {
	files := map[string]string{"config": "Include parts/*\n"}
	var prefix []string
	for index := 0; index < 10; index++ {
		name := fmt.Sprintf("byte-%d", index)
		files[fmt.Sprintf("parts/%02d", index)] = strings.Repeat("#padding\n", 25000) + "Host " + name + "\n"
		if index < 9 {
			prefix = append(prefix, name)
		}
	}
	home, roots := sshFixture(t, files)
	var cache sshHostInventory
	ctx, values := providerfixture.Observe(t, "inventory")
	// The tenth/final file exceeds the aggregate budget while each individual
	// file is valid. There is no later acquisition to mask its read error.
	for attempt := 0; attempt < 2; attempt++ {
		*values = nil
		if got := sshNames(cache.load(ctx, home, roots)); !reflect.DeepEqual(got, prefix) {
			t.Fatalf("partial acquisition lost the independently expected prefix: %v", got)
		}
		providerfixture.Check(t, *values, "ssh-hosts", health.Failed)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "parts", "09"), []byte("Host byte-9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	*values = nil
	if got := sshNames(cache.load(ctx, home, roots)); !reflect.DeepEqual(got, append(prefix, "byte-9")) {
		t.Fatalf("fresh repaired acquisition did not restore all aliases: %v", got)
	}
	providerfixture.Check(t, *values, "ssh-hosts", health.Healthy)
}

func TestSSHHealthPublishesAfterReleasingInventoryLock(t *testing.T) {
	home, roots := sshFixture(t, map[string]string{"config": "Host fixture-host\n"})
	var cache sshHostInventory
	observed := false
	ctx := health.WithObserver(t.Context(), func(value health.Observation) {
		if value.Source.Name() != "ssh-hosts" {
			return
		}
		observed = true
		// Observers update shared session state. They must not inherit an
		// inventory lock and prevent concurrent consumers reading that state.
		if !cache.TryLock() {
			t.Fatal("provider callback retained the inventory lock")
		}
		cache.Unlock()
		if got := sshNames(cache.load(context.Background(), home, roots)); !reflect.DeepEqual(got, []string{"fixture-host"}) {
			t.Fatal(got)
		}
	})
	cache.load(ctx, home, roots)
	if !observed {
		t.Fatal("completed discovery never reached its observer")
	}
}

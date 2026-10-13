package pkginstaller

import (
	"testing"

	"github.com/faustbrian/vuja/tests/providerfixture"
)

func TestPackageHealthKeepsDifferentToolsIndependent(t *testing.T) {
	for _, provider := range []struct{ manager, binary, source string }{
		{"pacman", "pacman", "packages-pacman"},
		{"yay", "yay", "packages-yay"},
		{"paru", "paru", "packages-paru"},
		{"apt", "dpkg-query", "packages-debian"},
		{"apt-get", "dpkg-query", "packages-debian"},
		{"dnf", "rpm", "packages-rpm"},
		{"yum", "rpm", "packages-rpm"},
		{"brew", "brew", "packages-brew"},
	} {
		t.Run(provider.manager, func(t *testing.T) {
			for _, mode := range []string{"inventory", "empty", "failed", "missing", "cancelled", "expired"} {
				t.Run(mode, func(t *testing.T) {
					dir := providerfixture.Environment(t)
					body := "printf '%s\\n' 'fixture-package'"
					if mode == "empty" {
						body = "exit 0"
					} else if mode == "failed" {
						body = "exit 7"
					}
					if mode != "missing" {
						providerfixture.Write(t, dir, provider.binary, body)
					}
					ctx, values := providerfixture.Observe(t, mode)
					results := installedPackageGenerator(provider.manager)(ctx, nil, "", "")
					count := 0
					if mode == "inventory" {
						count = 1
					}
					if len(results) != count || count == 1 && results[0].Cmd != "fixture-package" {
						t.Fatalf("installed package results changed: %+v", results)
					}
					providerfixture.Check(t, *values, provider.source, providerfixture.Outcome(mode))
				})
			}
		})
	}
}

func TestPackageHealthUnsupportedSelectorDoesNotInventAnAttempt(t *testing.T) {
	providerfixture.Environment(t)
	ctx, values := providerfixture.Observe(t, "inventory")
	if got := installedPackageGenerator("unsupported")(ctx, nil, "", ""); len(got) != 0 || len(*values) != 0 {
		t.Fatalf("intentional unsupported path invented a provider attempt: %+v %+v", got, *values)
	}
}

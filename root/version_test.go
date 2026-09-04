package root

import "testing"

func TestResolveVersionUsesGoInstallModuleVersionWithoutOverridingReleaseBuilds(t *testing.T) {
	for _, test := range []struct {
		name          string
		linked        string
		moduleVersion string
		want          string
	}{
		{name: "release linker value wins", linked: "v1.2.3", moduleVersion: "v1.2.2", want: "v1.2.3"},
		{name: "go install reports module version", linked: "dev", moduleVersion: "v1.2.3", want: "v1.2.3"},
		{name: "local build remains development", linked: "dev", moduleVersion: "(devel)", want: "dev"},
		{name: "empty module version remains development", linked: "dev", want: "dev"},
		{name: "invalid module version remains development", linked: "dev", moduleVersion: "latest", want: "dev"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveVersion(test.linked, test.moduleVersion); got != test.want {
				t.Fatalf("resolveVersion(%q, %q) = %q; want %q", test.linked, test.moduleVersion, got, test.want)
			}
		})
	}
}

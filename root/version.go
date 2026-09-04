package root

import (
	"runtime/debug"

	"golang.org/x/mod/semver"
)

// Version is the current build version, injected at release time via ldflags
// e.g. go build -ldflags="-X github.com/faustbrian/vuja/root.Version=v1.2.0"
var Version = "dev"

func init() {
	if build, ok := debug.ReadBuildInfo(); ok {
		Version = resolveVersion(Version, build.Main.Version)
	}
}

func resolveVersion(linked, moduleVersion string) string {
	if linked != "" && linked != "dev" {
		return linked
	}
	if semver.IsValid(moduleVersion) {
		return moduleVersion
	}
	return "dev"
}

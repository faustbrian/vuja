package main

import (
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

func TestNightlyVersionStaysInModuleMajor(t *testing.T) {
	for _, test := range []struct {
		name, base, sha, want string
		invalid               bool
	}{
		{name: "before first v2 tag", want: "v2.0.0-nightly.abc123"},
		{name: "stable advances patch", base: "v2.3.4", want: "v2.3.5-nightly.abc123"},
		{name: "candidate retains target", base: "v2.3.4-rc.2", want: "v2.3.4-nightly.abc123"},
		{name: "decimal carry", base: "v2.3.99", want: "v2.3.100-nightly.abc123"},
		{name: "large patch", base: "v2.3.99999999999999999999", want: "v2.3.100000000000000000000-nightly.abc123"},
		{name: "numeric hash", sha: "1234567", want: "v2.0.0-nightly.1234567"},
		{name: "leading zero numeric hash", sha: "0123456", want: "v2.0.0-nightly.g0123456"},
		{name: "zero hash", sha: "0", want: "v2.0.0-nightly.0"},
		{name: "all zero hash", sha: "0000000", want: "v2.0.0-nightly.g0000000"},
		{name: "leading zero hex hash", sha: "0abc12", want: "v2.0.0-nightly.0abc12"},
		{name: "old major refused", base: "v1.0.0", invalid: true},
		{name: "nightly is not a base", base: "v2.3.4-nightly.old", invalid: true},
		{name: "malformed tag refused", base: "v2.bad.0", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sha := test.sha
			if sha == "" {
				sha = "abc123"
			}
			cmd := exec.CommandContext(t.Context(), "bash", "nightly-version.sh", test.base, sha)
			output, err := cmd.CombinedOutput()
			if test.invalid {
				if err == nil {
					t.Fatalf("invalid base selected a nightly version: %q", output)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(output)) != test.want {
				t.Fatalf("nightly version = %q, error %v; want %q", output, err, test.want)
			}
			if !semver.IsValid(strings.TrimSpace(string(output))) {
				t.Fatalf("generated nightly is not valid Go SemVer: %q", output)
			}
		})
	}
}

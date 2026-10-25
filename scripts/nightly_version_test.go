package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNightlyVersionStaysInModuleMajor(t *testing.T) {
	for _, test := range []struct {
		name, base, want string
		invalid          bool
	}{
		{name: "before first v2 tag", want: "v2.0.0-nightly.abc123"},
		{name: "stable advances patch", base: "v2.3.4", want: "v2.3.5-nightly.abc123"},
		{name: "candidate retains target", base: "v2.3.4-rc.2", want: "v2.3.4-nightly.abc123"},
		{name: "decimal carry", base: "v2.3.99", want: "v2.3.100-nightly.abc123"},
		{name: "large patch", base: "v2.3.99999999999999999999", want: "v2.3.100000000000000000000-nightly.abc123"},
		{name: "old major refused", base: "v1.0.0", invalid: true},
		{name: "nightly is not a base", base: "v2.3.4-nightly.old", invalid: true},
		{name: "malformed tag refused", base: "v2.bad.0", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "bash", "nightly-version.sh", test.base, "abc123")
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
		})
	}
}

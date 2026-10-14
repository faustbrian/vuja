package config

import "testing"

func TestHistoryAgeRetentionConfigurationAcceptsOptionalDurationAndRejectsUnsafeValues(t *testing.T) {
	for _, duration := range []string{"0s", "72h", "2160h"} {
		if _, err := Decode([]byte("[history]\nmax-age = '" + duration + "'\n")); err != nil {
			t.Fatalf("valid optional retention duration %q was rejected: %v", duration, err)
		}
	}
	for _, duration := range []string{"-1h", "yesterday", "999999999999999999h"} {
		if _, err := Decode([]byte("[history]\nmax-age = '" + duration + "'\n")); err == nil {
			t.Fatalf("invalid retention duration %q was accepted", duration)
		}
	}
}

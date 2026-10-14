package config

import "testing"

func TestOutputCheckpointConfigurationRejectsUnsafePolicy(t *testing.T) {
	for _, setting := range []string{
		"output-checkpoints = 'automatic'",
		"output-checkpoint-interval = '0s'",
		"output-checkpoint-interval = '2h'",
		"output-checkpoint-max-age = '-1s'",
		"output-checkpoint-panes = 0",
		"output-checkpoint-panes = 65",
	} {
		t.Run(setting, func(t *testing.T) {
			if _, err := Decode([]byte("[ui.chatbox]\n" + setting + "\n")); err == nil {
				t.Fatal("unsafe checkpoint policy was accepted for runtime use")
			}
		})
	}
}

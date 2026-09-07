package root

import (
	"errors"
	"syscall"
	"testing"
)

func TestSignalManagedParentRequiresALiveVujaAncestor(t *testing.T) {
	processes := map[int]processIdentity{
		300: {ParentPID: 200, Name: "vuja"},
		200: {ParentPID: 100, Name: "zsh"},
		100: {ParentPID: 1, Name: "/Users/test/.local/bin/vuja"},
		90:  {ParentPID: 1, Name: "unrelated"},
	}
	lookup := func(pid int) (processIdentity, error) {
		identity, ok := processes[pid]
		if !ok {
			return processIdentity{}, errors.New("missing process")
		}
		return identity, nil
	}

	var signaled []int
	signal := func(pid int, signal syscall.Signal) error {
		if signal != syscall.SIGUSR1 {
			t.Fatalf("expected SIGUSR1, got %v", signal)
		}
		signaled = append(signaled, pid)
		return nil
	}
	if !signalManagedParent("100", 300, lookup, signal) {
		t.Fatal("expected the owning Vuja ancestor to receive reload")
	}
	if len(signaled) != 1 || signaled[0] != 100 {
		t.Fatalf("expected only pid 100 to be signaled, got %v", signaled)
	}

	signaled = nil
	if signalManagedParent("90", 300, lookup, signal) {
		t.Fatal("expected an unrelated live pid to be rejected")
	}
	if len(signaled) != 0 {
		t.Fatalf("expected no signal for unrelated pid, got %v", signaled)
	}

	processes[100] = processIdentity{ParentPID: 1, Name: "zsh"}
	if signalManagedParent("100", 300, lookup, signal) {
		t.Fatal("expected a rescue shell retaining the old pid to be rejected")
	}
}

func TestRescueEnvironmentDropsManagedSessionState(t *testing.T) {
	environment := withoutVujaSessionEnvironment([]string{
		"HOME=/home/test",
		"VUJA_PID=42",
		"VUJA_FD=13",
		"VUJA_HISTORY_ACK_FD=14",
		"VUJA_IS_CHILD=1",
		"VUJA_MANAGED_PROMPT=marker",
		"VUJA_MARKER=marker",
		"VUJA_PROMPT_TEXT=prompt",
		"VUJA_ACTIVE_SHELL=zsh",
		"VUJA_LOG_LEVEL=debug",
	})
	want := []string{"HOME=/home/test", "VUJA_LOG_LEVEL=debug"}
	if len(environment) != len(want) {
		t.Fatalf("expected only non-session state, got %v", environment)
	}
	for index := range want {
		if environment[index] != want[index] {
			t.Fatalf("expected %v, got %v", want, environment)
		}
	}
}

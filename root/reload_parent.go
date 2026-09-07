package root

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type processIdentity struct {
	ParentPID int
	Name      string
}

func lookupProcessIdentity(pid int) (processIdentity, error) {
	output, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return processIdentity{}, err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return processIdentity{}, errors.New("process identity is incomplete")
	}
	parentPID, err := strconv.Atoi(fields[0])
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{ParentPID: parentPID, Name: fields[1]}, nil
}

func signalManagedParent(
	pidText string,
	currentPID int,
	lookup func(int) (processIdentity, error),
	signal func(int, syscall.Signal) error,
) bool {
	targetPID, err := strconv.Atoi(pidText)
	if err != nil || targetPID <= 1 || targetPID == currentPID {
		return false
	}
	seen := make(map[int]struct{})
	for pid, depth := currentPID, 0; pid > 1 && depth < 64; depth++ {
		if _, exists := seen[pid]; exists {
			return false
		}
		seen[pid] = struct{}{}
		identity, err := lookup(pid)
		if err != nil {
			return false
		}
		if pid == targetPID {
			name := strings.TrimSuffix(filepath.Base(identity.Name), ".exe")
			return name == "vuja" && signal(pid, syscall.SIGUSR1) == nil
		}
		pid = identity.ParentPID
	}
	return false
}

var vujaSessionEnvironmentNames = map[string]struct{}{
	"VUJA_ACTIVE_SHELL":   {},
	"VUJA_FD":             {},
	"VUJA_HISTORY_ACK_FD": {},
	"VUJA_IS_CHILD":       {},
	"VUJA_MANAGED_PROMPT": {},
	"VUJA_MARKER":         {},
	"VUJA_PID":            {},
	"VUJA_PROMPT_TEXT":    {},
}

func withoutVujaSessionEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if _, managed := vujaSessionEnvironmentNames[name]; found && managed {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

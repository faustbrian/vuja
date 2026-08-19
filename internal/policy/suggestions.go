package policy

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/faustbrian/vuja/internal/config"
)

var sensitiveAssignment = regexp.MustCompile(`(?i)(api[_-]?key|token|password|passwd|secret|private[_-]?key)\s*=\s*\S+`)

func IsSensitive(command string) bool {
	lower := strings.ToLower(command)
	if sensitiveAssignment.MatchString(command) {
		return true
	}
	for _, flag := range []string{"--password ", "--password=", "--token ", "--token=", "--api-key ", "--api-key=", "authorization: bearer "} {
		if strings.Contains(lower, flag) {
			return true
		}
	}
	for _, pattern := range config.Get().Suggestions.IgnorePatterns {
		if matches(pattern, command) {
			return true
		}
	}
	return false
}

func IsDestructive(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	patterns := []string{
		"rm -rf", "rm -fr", "git reset --hard", "git clean -f",
		"kubectl delete", "docker system prune", "drop database",
		"truncate table", "terraform destroy",
	}
	for _, pattern := range patterns {
		if strings.HasPrefix(command, pattern) || strings.Contains(command, " "+pattern) {
			return true
		}
	}
	return false
}

func Blocked(command string) bool {
	for _, pattern := range config.Get().Suggestions.Blocks {
		if matches(pattern, command) {
			return true
		}
	}
	return false
}

func matches(pattern, value string) bool {
	pattern, value = strings.TrimSpace(pattern), strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if pattern == value {
		return true
	}
	ok, _ := filepath.Match(pattern, value)
	return ok
}

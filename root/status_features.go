package root

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

type shellStatusSnapshot struct {
	Jobs          int
	StoppedJobs   int
	Direnv        string
	VirtualEnv    string
	Conda         string
	Mise          string
	Nix           string
	AWSProfile    string
	AWSRegion     string
	DockerContext string
	KubeConfig    string
}

type shellStatusUpdate struct {
	Kind        string
	Value       string
	Jobs        int
	StoppedJobs int
}

type sessionStatusSnapshot struct {
	SSH       bool
	User      string
	Host      string
	Container string
	Root      bool
	Sudo      bool
}

type operationalContextSnapshot struct {
	Kubernetes          string
	KubernetesNamespace string
	AWSProfile          string
	AWSRegion           string
	Docker              string
}

type versionMismatch struct {
	Tool     string
	Declared string
	Active   string
}

type projectPackageSnapshot struct {
	Name    string
	Version string
}

type repositoryStatusSnapshot struct {
	Root       string
	GitDir     string
	CommonDir  string
	StashCount int
}

func parseShellStatusMessage(message string) (shellStatusUpdate, bool) {
	if value, ok := strings.CutPrefix(message, "VUJA_JOBS:"); ok {
		jobs, stopped, found := strings.Cut(value, ":")
		if !found {
			return shellStatusUpdate{}, false
		}
		jobCount, jobErr := strconv.Atoi(jobs)
		stoppedCount, stoppedErr := strconv.Atoi(stopped)
		if jobErr != nil || stoppedErr != nil || jobCount < 0 || stoppedCount < 0 {
			return shellStatusUpdate{}, false
		}
		return shellStatusUpdate{Kind: "jobs", Jobs: jobCount, StoppedJobs: stoppedCount}, true
	}
	value, ok := strings.CutPrefix(message, "VUJA_ENV:")
	if !ok {
		return shellStatusUpdate{}, false
	}
	kind, contents, found := strings.Cut(value, ":")
	if !found {
		return shellStatusUpdate{}, false
	}
	switch kind {
	case "direnv", "virtualenv", "conda", "mise", "nix", "aws-profile", "aws-region", "docker-context", "kubeconfig":
		return shellStatusUpdate{Kind: kind, Value: contents}, true
	default:
		return shellStatusUpdate{}, false
	}
}

func (s *shellStatusSnapshot) apply(update shellStatusUpdate) {
	switch update.Kind {
	case "jobs":
		s.Jobs, s.StoppedJobs = update.Jobs, update.StoppedJobs
	case "direnv":
		s.Direnv = update.Value
	case "virtualenv":
		s.VirtualEnv = update.Value
	case "conda":
		s.Conda = update.Value
	case "mise":
		s.Mise = update.Value
	case "nix":
		s.Nix = update.Value
	case "aws-profile":
		s.AWSProfile = update.Value
	case "aws-region":
		s.AWSRegion = update.Value
	case "docker-context":
		s.DockerContext = update.Value
	case "kubeconfig":
		s.KubeConfig = update.Value
	}
}

func findRepositoryRoot(directory string) string {
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}

func gitCommonDirectory(gitDirectory string) string {
	content, err := os.ReadFile(filepath.Join(gitDirectory, "commondir"))
	if err != nil {
		return gitDirectory
	}
	common := strings.TrimSpace(string(content))
	if common == "" {
		return gitDirectory
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDirectory, common)
	}
	return filepath.Clean(common)
}

func detectRepositoryStatus(directory string, includeStash bool) repositoryStatusSnapshot {
	root := findRepositoryRoot(directory)
	if root == "" {
		return repositoryStatusSnapshot{}
	}
	gitDirectory := findGitDirectory(directory)
	commonDirectory := gitCommonDirectory(gitDirectory)
	status := repositoryStatusSnapshot{
		Root:      root,
		GitDir:    gitDirectory,
		CommonDir: commonDirectory,
	}
	if includeStash {
		status.StashCount = countNonEmptyLines(filepath.Join(commonDirectory, "logs", "refs", "stash"))
	}
	return status
}

func countNonEmptyLines(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

func parseGitLineMetrics(output []byte) (int, int) {
	added, deleted := 0, 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if value, err := strconv.Atoi(fields[0]); err == nil {
			added += value
		}
		if value, err := strconv.Atoi(fields[1]); err == nil {
			deleted += value
		}
	}
	return added, deleted
}

func detectPinnedVersions(directory string) map[string]string {
	pins := make(map[string]string)
	root := findRepositoryRoot(directory)
	if root == "" {
		root = filepath.Clean(directory)
	}
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		for _, pair := range []struct {
			file string
			tool string
		}{
			{".node-version", "node"},
			{".nvmrc", "node"},
			{".php-version", "php"},
			{".python-version", "python"},
			{".ruby-version", "ruby"},
			{"rust-toolchain", "rust"},
		} {
			if _, exists := pins[pair.tool]; exists {
				continue
			}
			if content, err := os.ReadFile(filepath.Join(current, pair.file)); err == nil {
				pins[pair.tool] = normalizePinnedVersion(string(content))
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, ".tool-versions")); err == nil {
			for tool, names := range map[string][]string{
				"node":   {"nodejs", "node"},
				"php":    {"php"},
				"python": {"python"},
				"ruby":   {"ruby"},
				"elixir": {"elixir"},
				"go":     {"golang", "go"},
				"rust":   {"rust"},
			} {
				if _, exists := pins[tool]; !exists {
					value := toolVersion(string(content), names...)
					if tool == "elixir" {
						value, _, _ = strings.Cut(value, "-otp-")
					}
					if exactVersion(value) {
						pins[tool] = normalizePinnedVersion(value)
					}
				}
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, "rust-toolchain.toml")); err == nil {
			if _, exists := pins["rust"]; !exists {
				pins["rust"] = normalizePinnedVersion(quotedAssignment(string(content), "channel"))
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, "go.mod")); err == nil {
			if _, exists := pins["go"]; !exists {
				if value := directiveValue(string(content), "toolchain"); value != "" {
					pins["go"] = normalizePinnedVersion(value)
				}
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, "package.json")); err == nil {
			var pkg struct {
				PackageManager string `json:"packageManager"`
			}
			if json.Unmarshal(content, &pkg) == nil {
				if value, ok := strings.CutPrefix(pkg.PackageManager, "bun@"); ok && exactVersion(value) {
					pins["bun"] = normalizePinnedVersion(value)
				}
			}
		}
		if current == root || filepath.Dir(current) == current {
			break
		}
	}
	for tool, value := range pins {
		if !exactVersion(value) {
			delete(pins, tool)
		}
	}
	return pins
}

func normalizePinnedVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "go")
	value = strings.TrimPrefix(value, "v")
	return value
}

func exactVersion(value string) bool {
	value = normalizePinnedVersion(value)
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func comparePinnedVersions(pins, active map[string]string) []versionMismatch {
	order := []string{"php", "python", "ruby", "elixir", "node", "bun", "go", "rust"}
	mismatches := make([]versionMismatch, 0, len(pins))
	for _, tool := range order {
		declared, relevant := pins[tool]
		running := normalizePinnedVersion(active[tool])
		if relevant && running != "" && normalizePinnedVersion(declared) != running {
			mismatches = append(mismatches, versionMismatch{Tool: tool, Declared: declared, Active: running})
		}
	}
	return mismatches
}

func detectProjectPackage(directory string) projectPackageSnapshot {
	root := findRepositoryRoot(directory)
	if root == "" {
		root = filepath.Clean(directory)
	}
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		if content, err := os.ReadFile(filepath.Join(current, "package.json")); err == nil {
			var pkg projectPackageSnapshot
			if json.Unmarshal(content, &pkg) == nil && (pkg.Name != "" || pkg.Version != "") {
				return pkg
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, "composer.json")); err == nil {
			var pkg projectPackageSnapshot
			if json.Unmarshal(content, &pkg) == nil && (pkg.Name != "" || pkg.Version != "") {
				return pkg
			}
		}
		if content, err := os.ReadFile(filepath.Join(current, "Cargo.toml")); err == nil {
			pkg := projectPackageSnapshot{
				Name:    tomlSectionAssignment(string(content), "package", "name"),
				Version: tomlSectionAssignment(string(content), "package", "version"),
			}
			if pkg.Name != "" || pkg.Version != "" {
				return pkg
			}
		}
		if current == root || filepath.Dir(current) == current {
			return projectPackageSnapshot{}
		}
	}
}

func tomlSectionAssignment(content, section, key string) string {
	active := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			active = trimmed == "["+section+"]"
			continue
		}
		if active {
			if value := quotedAssignment(trimmed, key); value != "" {
				return value
			}
		}
	}
	return ""
}

func detectSessionStatus() sessionStatusSnapshot {
	result := sessionStatusSnapshot{
		SSH:  os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "",
		Root: os.Geteuid() == 0,
		Sudo: os.Getenv("SUDO_USER") != "" || os.Getenv("SUDO_COMMAND") != "",
	}
	if current, err := user.Current(); err == nil {
		result.User = current.Username
	}
	result.Host, _ = os.Hostname()
	result.Container = strings.TrimSpace(os.Getenv("container"))
	if result.Container == "" {
		for _, path := range []string{"/run/.containerenv", "/.dockerenv"} {
			if _, err := os.Stat(path); err == nil {
				result.Container = "container"
				break
			}
		}
	}
	return result
}

func detectOperationalContexts(shell shellStatusSnapshot) operationalContextSnapshot {
	result := operationalContextSnapshot{
		AWSProfile: firstNonEmpty(shell.AWSProfile, os.Getenv("AWS_PROFILE"), os.Getenv("AWS_DEFAULT_PROFILE")),
		AWSRegion:  firstNonEmpty(shell.AWSRegion, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION")),
		Docker:     firstNonEmpty(shell.DockerContext, os.Getenv("DOCKER_CONTEXT")),
	}
	if result.Docker == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if content, readErr := os.ReadFile(filepath.Join(home, ".docker", "config.json")); readErr == nil {
				var config struct {
					CurrentContext string `json:"currentContext"`
				}
				if json.Unmarshal(content, &config) == nil && config.CurrentContext != "default" {
					result.Docker = config.CurrentContext
				}
			}
		}
	}
	kubeConfig := firstNonEmpty(shell.KubeConfig, os.Getenv("KUBECONFIG"))
	if kubeConfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			kubeConfig = filepath.Join(home, ".kube", "config")
		}
	}
	for _, path := range filepath.SplitList(kubeConfig) {
		if result.Kubernetes, result.KubernetesNamespace = parseKubeConfig(path); result.Kubernetes != "" {
			break
		}
	}
	return result
}

func parseKubeConfig(path string) (string, string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	current := ""
	type entry struct {
		name      string
		namespace string
	}
	var contexts []entry
	var active *entry
	inContexts := false
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(trimmed, "current-context:"); ok {
			current = strings.Trim(strings.TrimSpace(value), "\"'")
			continue
		}
		if trimmed == "contexts:" {
			inContexts = true
			continue
		}
		if inContexts && len(line) > 0 && line[0] != ' ' && line[0] != '-' {
			inContexts = false
		}
		if !inContexts {
			continue
		}
		if strings.HasPrefix(trimmed, "- context:") {
			contexts = append(contexts, entry{})
			active = &contexts[len(contexts)-1]
			continue
		}
		if active == nil {
			continue
		}
		if value, ok := strings.CutPrefix(trimmed, "name:"); ok {
			active.name = strings.Trim(strings.TrimSpace(value), "\"'")
		}
		if value, ok := strings.CutPrefix(trimmed, "namespace:"); ok {
			active.namespace = strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	for _, context := range contexts {
		if context.name == current {
			return current, context.namespace
		}
	}
	return current, ""
}

func contextsForCommand(command string, contexts operationalContextSnapshot) []string {
	executable := statusCommandContext(command)
	if executable == "" {
		return nil
	}
	switch executable {
	case "kubernetes":
		if contexts.Kubernetes == "" {
			return nil
		}
		value := "Kube " + contexts.Kubernetes
		if contexts.KubernetesNamespace != "" {
			value += "/" + contexts.KubernetesNamespace
		}
		return []string{value}
	case "aws":
		value := strings.TrimSpace(strings.Join([]string{contexts.AWSProfile, contexts.AWSRegion}, " "))
		if value != "" {
			return []string{"AWS " + value}
		}
	case "docker":
		if contexts.Docker != "" {
			return []string{"Docker " + contexts.Docker}
		}
	}
	return nil
}

func detectEnvironmentStatus(shell shellStatusSnapshot, directory string) []string {
	result := make([]string, 0, 5)
	if shell.VirtualEnv != "" {
		result = append(result, "venv "+filepath.Base(shell.VirtualEnv))
	}
	if shell.Conda != "" {
		result = append(result, "conda "+shell.Conda)
	}
	if shell.Mise != "" {
		result = append(result, "mise "+shell.Mise)
	}
	if shell.Nix != "" {
		result = append(result, "nix "+shell.Nix)
	}
	if shell.Direnv != "" {
		result = append(result, "direnv loaded")
	} else if findUp(directory, ".envrc") != "" {
		result = append(result, "direnv unloaded")
	}
	return result
}

func findUp(directory, name string) string {
	root := findRepositoryRoot(directory)
	if root == "" {
		root = filepath.Clean(directory)
	}
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		path := filepath.Join(current, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
		if current == root || filepath.Dir(current) == current {
			return ""
		}
	}
}

func directoryReadOnly(directory string) bool {
	info, err := os.Stat(directory)
	return err == nil && info.Mode().Perm()&0o222 == 0
}

func semanticExitStatus(code int) string {
	switch code {
	case 126:
		return "not executable"
	case 127:
		return "not found"
	case 130:
		return "interrupted"
	case 137:
		return "killed"
	case 143:
		return "terminated"
	default:
		return ""
	}
}

func statusToolDisplayName(tool string) string {
	switch tool {
	case "php":
		return "PHP"
	case "node":
		return "Node"
	case "bun":
		return "Bun"
	case "go":
		return "Go"
	case "rust":
		return "Rust"
	default:
		return strings.ToUpper(tool)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func staleStatusText(providers []string) string {
	if len(providers) == 0 {
		return ""
	}
	return fmt.Sprintf("stale %s", strings.Join(providers, ","))
}

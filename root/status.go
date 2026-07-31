package root

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type gitStatusSnapshot struct {
	Branch       string
	Operation    string
	Resolved     bool
	Changed      int
	Staged       int
	Modified     int
	Renamed      int
	Untracked    int
	Conflicts    int
	Added        int
	Deleted      int
	Ahead        int
	Behind       int
	StashCount   int
	LinesAdded   int
	LinesDeleted int
}

type statusSnapshot struct {
	Revision          uint64
	Directory         string
	CommandContext    string
	RepositoryRoot    string
	DirectoryReadOnly bool
	Git               gitStatusSnapshot
	Versions          map[string]string
	ActiveVersions    map[string]string
	Package           projectPackageSnapshot
	Shell             shellStatusSnapshot
	Session           sessionStatusSnapshot
	Contexts          operationalContextSnapshot
	Environment       []string
	VersionMismatches []versionMismatch
	StaleProviders    []string
	StaleSince        time.Time
	CPU               float64
	Memory            float64
	HasCPU            bool
	HasMemory         bool
	ExitCode          *int
	Duration          time.Duration
}

type statusCommand func(context.Context, string, string, ...string) ([]byte, error)

type statusEngineOptions struct {
	Run          statusCommand
	OnUpdate     func(statusSnapshot)
	Sample       func() (float64, float64)
	Metrics      bool
	GitLines     bool
	Session      bool
	GitStash     bool
	Package      bool
	Contexts     bool
	Environment  bool
	SkipVersions bool
}

type statusEngine struct {
	mu             sync.RWMutex
	snapshot       statusSnapshot
	versions       map[string]cachedStatusVersion
	git            map[string]cachedGitStatus
	projects       map[string]cachedProjectVersions
	run            statusCommand
	onUpdate       func(statusSnapshot)
	sample         func() (float64, float64)
	metrics        bool
	gitLines       bool
	session        bool
	gitStash       bool
	projectPackage bool
	contexts       bool
	environment    bool
	skipVersions   bool
	shell          shellStatusSnapshot
	ctx            context.Context
	cancel         context.CancelFunc
	requestMu      sync.Mutex
	metricWG       sync.WaitGroup
	workerWG       sync.WaitGroup
	working        bool
	pending        statusRefreshRequest
	desired        string
	commandTimer   *time.Timer
	revision       uint64
}

type statusRefreshRequest struct {
	directory string
	force     bool
}

type cachedStatusVersion struct {
	value   string
	expires time.Time
}

type cachedGitStatus struct {
	status    gitStatusSnapshot
	refreshed time.Time
	signature string
}

type cachedProjectVersions struct {
	versions  map[string]string
	signature string
	refreshed time.Time
}

func newStatusEngine(options statusEngineOptions) *statusEngine {
	ctx, cancel := context.WithCancel(context.Background())
	run := options.Run
	if run == nil {
		run = runStatusCommand
	}
	sample := options.Sample
	if sample == nil {
		sample = sampleSystemUsage
	}
	return &statusEngine{
		run: run, onUpdate: options.OnUpdate, sample: sample, metrics: options.Metrics,
		gitLines: options.GitLines, session: options.Session, gitStash: options.GitStash,
		projectPackage: options.Package, contexts: options.Contexts, environment: options.Environment,
		skipVersions: options.SkipVersions,
		ctx:          ctx, cancel: cancel,
		versions: make(map[string]cachedStatusVersion), git: make(map[string]cachedGitStatus),
		projects: make(map[string]cachedProjectVersions),
	}
}

func (e *statusEngine) Close() {
	e.cancel()
	e.requestMu.Lock()
	if e.commandTimer != nil {
		e.commandTimer.Stop()
		e.commandTimer = nil
	}
	e.requestMu.Unlock()
	e.metricWG.Wait()
	e.workerWG.Wait()
}

func (e *statusEngine) StartMetrics(interval time.Duration) {
	if interval <= 0 || !e.metrics {
		return
	}
	e.metricWG.Add(1)
	go func() {
		defer e.metricWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				e.refreshMetrics()
			}
		}
	}()
}

func (e *statusEngine) refreshMetrics() {
	cpu, memory := e.sample()
	e.mu.Lock()
	if e.snapshot.HasCPU && e.snapshot.HasMemory &&
		math.Round(e.snapshot.CPU) == math.Round(cpu) && math.Round(e.snapshot.Memory) == math.Round(memory) {
		e.mu.Unlock()
		return
	}
	e.snapshot.CPU = cpu
	e.snapshot.Memory = memory
	e.snapshot.HasCPU = true
	e.snapshot.HasMemory = true
	e.revision++
	e.snapshot.Revision = e.revision
	snapshot := cloneStatusSnapshot(e.snapshot)
	e.mu.Unlock()
	e.publish(snapshot)
}

func (e *statusEngine) Snapshot() statusSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneStatusSnapshot(e.snapshot)
}

func (e *statusEngine) SetCommandResult(exitCode int, duration time.Duration) {
	e.mu.Lock()
	e.snapshot.ExitCode = &exitCode
	e.snapshot.Duration = duration
	e.revision++
	e.snapshot.Revision = e.revision
	snapshot := cloneStatusSnapshot(e.snapshot)
	e.mu.Unlock()
	e.publish(snapshot)
}

func (e *statusEngine) SetShellStatus(update shellStatusUpdate) {
	e.mu.Lock()
	e.shell.apply(update)
	e.mu.Unlock()
}

func (e *statusEngine) Refresh(directory string) {
	e.requestMu.Lock()
	if e.commandTimer != nil {
		e.commandTimer.Stop()
		e.commandTimer = nil
	}
	e.requestMu.Unlock()
	e.request(statusRefreshRequest{directory: directory})
}

func (e *statusEngine) RefreshAfterCommand(directory string) {
	e.requestMu.Lock()
	e.desired = directory
	if e.commandTimer != nil {
		e.commandTimer.Stop()
	}
	e.commandTimer = time.AfterFunc(75*time.Millisecond, func() {
		e.requestMu.Lock()
		if e.ctx.Err() != nil || e.desired != directory {
			e.requestMu.Unlock()
			return
		}
		e.commandTimer = nil
		e.requestMu.Unlock()
		e.request(statusRefreshRequest{directory: directory, force: true})
	})
	e.requestMu.Unlock()
}

func (e *statusEngine) request(request statusRefreshRequest) {
	e.requestMu.Lock()
	if e.ctx.Err() != nil {
		e.requestMu.Unlock()
		return
	}
	e.desired = request.directory
	if e.working {
		if e.pending.directory == request.directory {
			e.pending.force = e.pending.force || request.force
		} else {
			e.pending = request
		}
		e.requestMu.Unlock()
		return
	}
	e.working = true
	e.workerWG.Add(1)
	e.requestMu.Unlock()
	go func() {
		defer e.workerWG.Done()
		e.refreshLoop(request)
	}()
}

func (e *statusEngine) refreshLoop(request statusRefreshRequest) {
	for {
		e.refresh(request.directory, request.force)
		e.requestMu.Lock()
		if e.pending.directory == "" || e.ctx.Err() != nil {
			e.working = false
			e.pending = statusRefreshRequest{}
			e.requestMu.Unlock()
			return
		}
		request = e.pending
		e.pending = statusRefreshRequest{}
		e.requestMu.Unlock()
	}
}

func (e *statusEngine) refresh(directory string, force bool) {
	versionsToResolve := make(map[string]string)
	pinnedVersions := make(map[string]string)
	if !e.skipVersions {
		versionsToResolve = e.projectVersions(directory)
		pinnedVersions = detectPinnedVersions(directory)
	}
	for tool, version := range pinnedVersions {
		if _, exists := versionsToResolve[tool]; !exists {
			versionsToResolve[tool] = version
		}
	}
	previous := e.Snapshot()
	previousVersions := map[string]string(nil)
	previousActiveVersions := map[string]string(nil)
	if previous.Directory == directory {
		previousVersions = previous.Versions
		previousActiveVersions = previous.ActiveVersions
	}
	e.mu.RLock()
	shellStatus := e.shell
	e.mu.RUnlock()
	repository := detectRepositoryStatus(directory, e.gitStash)
	sessionStatus := sessionStatusSnapshot{}
	if e.session {
		sessionStatus = detectSessionStatus()
	}
	projectPackage := projectPackageSnapshot{}
	if e.projectPackage {
		projectPackage = detectProjectPackage(directory)
	}
	contexts := operationalContextSnapshot{}
	if e.contexts {
		contexts = detectOperationalContexts(shellStatus)
	}
	environment := []string(nil)
	if e.environment {
		environment = detectEnvironmentStatus(shellStatus, directory)
	}
	next := statusSnapshot{
		Directory:         directory,
		RepositoryRoot:    repository.Root,
		DirectoryReadOnly: directoryReadOnly(directory),
		Versions:          retainResolvedVersions(versionsToResolve, previousVersions),
		ActiveVersions:    cloneVersions(previousActiveVersions),
		Package:           projectPackage,
		Shell:             shellStatus,
		Session:           sessionStatus,
		Contexts:          contexts,
		Environment:       environment,
	}
	if e.session && !next.Session.Root && !next.Session.Sudo {
		next.Session.Sudo = e.cachedSudoStatus(directory)
	}
	if e.metrics {
		next.CPU, next.Memory = e.sample()
		next.HasCPU, next.HasMemory = true, true
	}
	gitDir := repository.GitDir
	gitSignature := ""
	probeGit := false
	if gitDir != "" {
		gitSignature = gitMetadataSignature(gitDir)
		cached, cachedOK := e.cachedGitStatus(gitDir)
		if cachedOK {
			next.Git = cached.status
		} else {
			next.Git.Branch = readGitBranch(gitDir)
		}
		next.Git.StashCount = repository.StashCount
		next.Git.Operation = detectGitOperation(directory)
		probeGit = force || !cachedOK || cached.signature != gitSignature || time.Since(cached.refreshed) >= time.Second
	}
	if !e.commitSnapshot(directory, next) {
		return
	}
	var versions sync.WaitGroup
	var activeVersions map[string]string
	var versionFailures []string
	versions.Add(1)
	go func() {
		defer versions.Done()
		activeVersions, versionFailures = e.resolveVersions(directory, versionsToResolve, pinnedVersions)
	}()
	if probeGit {
		ctx, cancel := context.WithTimeout(e.ctx, 300*time.Millisecond)
		output, err := e.run(ctx, directory, "git", "-C", directory, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
		cancel()
		if err == nil {
			next.Git = parseGitStatus(output)
			next.Git.Operation = detectGitOperation(directory)
			next.Git.StashCount = repository.StashCount
			if e.gitLines {
				lineContext, lineCancel := context.WithTimeout(e.ctx, 250*time.Millisecond)
				lineOutput, lineErr := e.run(lineContext, directory, "git", "-C", directory, "diff", "--numstat", "HEAD", "--")
				lineCancel()
				if lineErr == nil {
					next.Git.LinesAdded, next.Git.LinesDeleted = parseGitLineMetrics(lineOutput)
				} else if cached, ok := e.cachedGitStatus(gitDir); ok {
					next.Git.LinesAdded, next.Git.LinesDeleted = cached.status.LinesAdded, cached.status.LinesDeleted
				}
			}
			e.storeGitStatus(gitDir, next.Git, gitSignature)
		} else if _, ok := e.cachedGitStatus(gitDir); ok {
			next.StaleProviders = append(next.StaleProviders, "git")
		}
	}
	versions.Wait()
	next.Versions = retainResolvedVersions(versionsToResolve, previousVersions)
	next.ActiveVersions = retainResolvedVersions(activeVersions, previousActiveVersions)
	next.VersionMismatches = comparePinnedVersions(pinnedVersions, next.ActiveVersions)
	for provider, previousValue := range previousActiveVersions {
		_, relevantVersion := versionsToResolve[provider]
		_, relevantPin := pinnedVersions[provider]
		if previousValue != "" && activeVersions[provider] == "" && (relevantVersion || relevantPin) {
			next.StaleProviders = appendUnique(next.StaleProviders, "versions")
			break
		}
	}
	for _, provider := range versionFailures {
		if previousVersions[provider] != "" || previousActiveVersions[provider] != "" {
			next.StaleProviders = appendUnique(next.StaleProviders, "versions")
			break
		}
	}
	if len(next.StaleProviders) > 0 {
		if len(previous.StaleProviders) > 0 && !previous.StaleSince.IsZero() {
			next.StaleSince = previous.StaleSince
		} else {
			next.StaleSince = time.Now()
		}
	}
	e.commitSnapshot(directory, next)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (e *statusEngine) cachedSudoStatus(directory string) bool {
	const cacheKey = "__sudo"
	if value, ok := e.cachedVersion(cacheKey); ok {
		return value == "active"
	}
	ctx, cancel := context.WithTimeout(e.ctx, 100*time.Millisecond)
	_, err := e.run(ctx, directory, "sudo", "-n", "true")
	cancel()
	value := ""
	if err == nil {
		value = "active"
	}
	e.storeVersion(cacheKey, value, 30*time.Second)
	return value == "active"
}

func retainResolvedVersions(current, previous map[string]string) map[string]string {
	retained := cloneVersions(current)
	for name, value := range retained {
		if value == "" && previous[name] != "" {
			retained[name] = previous[name]
		}
	}
	return retained
}

func (e *statusEngine) commitSnapshot(directory string, next statusSnapshot) bool {
	if !e.isDesired(directory) || e.ctx.Err() != nil {
		return false
	}
	next = cloneStatusSnapshot(next)
	e.mu.Lock()
	next.ExitCode = e.snapshot.ExitCode
	next.Duration = e.snapshot.Duration
	e.revision++
	next.Revision = e.revision
	e.snapshot = next
	e.mu.Unlock()
	e.publish(next)
	return true
}

func (e *statusEngine) projectVersions(directory string) map[string]string {
	signature := projectMetadataSignature(directory)
	e.mu.RLock()
	cached, ok := e.projects[directory]
	e.mu.RUnlock()
	if ok && cached.signature == signature && time.Since(cached.refreshed) < time.Second {
		return cloneVersions(cached.versions)
	}
	versions := detectProjectVersions(directory)
	e.mu.Lock()
	e.projects[directory] = cachedProjectVersions{
		versions: cloneVersions(versions), signature: signature, refreshed: time.Now(),
	}
	e.mu.Unlock()
	return versions
}

func projectMetadataSignature(directory string) string {
	names := []string{
		"go.mod", "go.work", "go.work.sum", "package.json", ".node-version", ".nvmrc", ".tool-versions",
		"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "bunfig.toml",
		"pyproject.toml", ".python-version", "uv.lock", "poetry.lock", "Pipfile", "Pipfile.lock", "requirements.txt",
		".ruby-version", "Gemfile", "Gemfile.lock", "mix.exs", "mix.lock",
		"Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml", "composer.json", "composer.lock", ".php-version",
		"vendor/composer/installed.json", "artisan", "Dockerfile", ".dockerignore",
		"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
	}
	var signature strings.Builder
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		for _, name := range names {
			info, err := os.Stat(filepath.Join(current, name))
			if err != nil {
				continue
			}
			writeProjectMetadataSignature(&signature, current, name, info)
		}
		if entries, err := os.ReadDir(current); err == nil {
			for _, entry := range entries {
				if entry.IsDir() ||
					(!strings.HasPrefix(entry.Name(), "Dockerfile.") &&
						!strings.HasPrefix(entry.Name(), "requirements") &&
						!strings.HasSuffix(entry.Name(), ".gemspec")) {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr == nil {
					writeProjectMetadataSignature(&signature, current, entry.Name(), info)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return signature.String()
}

func writeProjectMetadataSignature(signature *strings.Builder, directory, name string, info os.FileInfo) {
	signature.WriteString(directory)
	signature.WriteByte('/')
	signature.WriteString(name)
	signature.WriteByte(':')
	signature.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
	signature.WriteByte(':')
	signature.WriteString(strconv.FormatInt(info.Size(), 10))
	signature.WriteByte(';')
}

func readGitBranch(gitDirectory string) string {
	content, err := os.ReadFile(filepath.Join(gitDirectory, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(content))
	if reference, ok := strings.CutPrefix(head, "ref:"); ok {
		return strings.TrimPrefix(strings.TrimSpace(reference), "refs/heads/")
	}
	if len(head) >= 7 {
		return "detached " + head[:7]
	}
	return "detached"
}

func (e *statusEngine) cachedGitStatus(repository string) (cachedGitStatus, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	value, ok := e.git[repository]
	return value, ok
}

func (e *statusEngine) storeGitStatus(repository string, status gitStatusSnapshot, signature ...string) {
	metadata := ""
	if len(signature) > 0 {
		metadata = signature[0]
	}
	e.mu.Lock()
	e.git[repository] = cachedGitStatus{status: status, refreshed: time.Now(), signature: metadata}
	e.mu.Unlock()
}

func gitMetadataSignature(gitDirectory string) string {
	var signature strings.Builder
	for _, name := range []string{
		"HEAD", "index", "packed-refs", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply",
	} {
		info, err := os.Stat(filepath.Join(gitDirectory, name))
		if err != nil {
			continue
		}
		signature.WriteString(name)
		signature.WriteByte(':')
		signature.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		signature.WriteByte(':')
		signature.WriteString(strconv.FormatInt(info.Size(), 10))
		signature.WriteByte(';')
	}
	return signature.String()
}

func (e *statusEngine) isDesired(directory string) bool {
	e.requestMu.Lock()
	defer e.requestMu.Unlock()
	return e.desired == directory
}

func (e *statusEngine) publish(snapshot statusSnapshot) {
	if e.onUpdate != nil {
		e.onUpdate(snapshot)
	}
}

func (e *statusEngine) resolveVersions(
	directory string,
	versions map[string]string,
	pinned map[string]string,
) (map[string]string, []string) {
	commands := []struct {
		name    string
		command []string
	}{
		{name: "php", command: []string{"php", "-r", "echo PHP_VERSION;"}},
		{name: "composer", command: []string{"composer", "--no-plugins", "--version", "--no-ansi"}},
		{name: "python", command: []string{"python3", "--version"}},
		{name: "ruby", command: []string{"ruby", "--version"}},
		{name: "elixir", command: []string{"elixir", "--version"}},
		{name: "go", command: []string{"go", "version"}},
		{name: "node", command: []string{"node", "--version"}},
		{name: "bun", command: []string{"bun", "--version"}},
		{name: "rust", command: []string{"rustc", "--version"}},
		{name: "docker", command: []string{"docker", "--version"}},
		{name: "docker-compose", command: []string{"docker", "compose", "version", "--short"}},
	}
	var mu sync.Mutex
	var wait sync.WaitGroup
	limit := make(chan struct{}, 4)
	active := make(map[string]string)
	failures := make([]string, 0, len(commands))
	pending := make([]struct {
		name     string
		command  []string
		cacheKey string
	}, 0, len(commands))
	for _, provider := range commands {
		value, relevant := versions[provider.name]
		_, pinnedRelevant := pinned[provider.name]
		if !relevant && !pinnedRelevant {
			continue
		}
		if value != "" && !pinnedRelevant {
			active[provider.name] = value
			continue
		}
		cacheKey := statusVersionCacheKey(provider.name, directory)
		if value, ok := e.cachedVersion(cacheKey); ok {
			if versions[provider.name] == "" {
				versions[provider.name] = value
			}
			active[provider.name] = value
			continue
		}
		pending = append(pending, struct {
			name     string
			command  []string
			cacheKey string
		}{name: provider.name, command: provider.command, cacheKey: cacheKey})
	}
	for _, provider := range pending {
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case limit <- struct{}{}:
			case <-e.ctx.Done():
				return
			}
			defer func() { <-limit }()
			ctx, cancel := context.WithTimeout(e.ctx, 250*time.Millisecond)
			output, err := e.run(ctx, directory, provider.command[0], provider.command[1:]...)
			cancel()
			value := ""
			if err == nil {
				value = normalizeToolVersion(provider.name, string(output))
			}
			ttl := 5 * time.Minute
			if err != nil {
				ttl = 30 * time.Second
			}
			e.storeVersion(provider.cacheKey, value, ttl)
			mu.Lock()
			if versions[provider.name] == "" {
				versions[provider.name] = value
			}
			active[provider.name] = value
			if err != nil {
				failures = append(failures, provider.name)
			}
			mu.Unlock()
		}()
	}
	wait.Wait()
	return active, failures
}

func statusVersionCacheKey(name, directory string) string {
	switch name {
	case "composer", "docker", "docker-compose":
		return name
	default:
		return name + "\x00" + filepath.Clean(directory)
	}
}

func (e *statusEngine) cachedVersion(key string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	value, ok := e.versions[key]
	return value.value, ok && time.Now().Before(value.expires)
}

func (e *statusEngine) storeVersion(key, value string, ttl time.Duration) {
	e.mu.Lock()
	e.versions[key] = cachedStatusVersion{value: value, expires: time.Now().Add(ttl)}
	e.mu.Unlock()
}

func runStatusCommand(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "NO_COLOR=1")
	return command.Output()
}

func parseGitStatus(output []byte) gitStatusSnapshot {
	status := gitStatusSnapshot{Resolved: true}
	var oid string
	detached := false
	for _, line := range strings.Split(string(output), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			oid = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.head "):
			status.Branch = strings.TrimPrefix(line, "# branch.head ")
			detached = status.Branch == "(detached)"
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			for _, field := range fields {
				if value, ok := strings.CutPrefix(field, "+"); ok {
					status.Ahead, _ = strconv.Atoi(value)
				}
				if value, ok := strings.CutPrefix(field, "-"); ok {
					status.Behind, _ = strconv.Atoi(value)
				}
			}
		case strings.HasPrefix(line, "? "):
			status.Changed++
			status.Untracked++
		case strings.HasPrefix(line, "u "):
			status.Changed++
			status.Conflicts++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			fields := strings.Fields(line)
			if len(fields) < 2 || len(fields[1]) < 2 {
				continue
			}
			index, worktree := fields[1][0], fields[1][1]
			status.Changed++
			if index != '.' {
				status.Staged++
			}
			if worktree != '.' {
				status.Modified++
			}
			if strings.HasPrefix(line, "2 ") && len(fields) > 8 && strings.HasPrefix(fields[8], "R") {
				status.Renamed++
			}
			if index == 'A' || worktree == 'A' {
				status.Added++
			}
			if index == 'D' || worktree == 'D' {
				status.Deleted++
			}
		}
	}
	if detached {
		status.Branch = "detached"
		if oid != "" && oid != "(initial)" {
			status.Branch += " " + oid[:min(7, len(oid))]
		}
	}
	return status
}

func detectGitOperation(directory string) string {
	gitDir := findGitDirectory(directory)
	if gitDir == "" {
		return ""
	}
	for _, state := range []struct {
		name  string
		paths []string
	}{
		{name: "rebasing", paths: []string{"rebase-merge", "rebase-apply"}},
		{name: "merging", paths: []string{"MERGE_HEAD"}},
		{name: "cherry-picking", paths: []string{"CHERRY_PICK_HEAD"}},
		{name: "reverting", paths: []string{"REVERT_HEAD"}},
	} {
		for _, name := range state.paths {
			if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
				return state.name
			}
		}
	}
	return ""
}

func findGitDirectory(directory string) string {
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, ".git")
		if info, err := os.Stat(candidate); err == nil {
			if info.IsDir() {
				return candidate
			}
			if content, readErr := os.ReadFile(candidate); readErr == nil {
				if value, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:"); ok {
					path := strings.TrimSpace(value)
					if !filepath.IsAbs(path) {
						path = filepath.Join(current, path)
					}
					return filepath.Clean(path)
				}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}

func isGitWorktree(directory string) bool {
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}

func detectProjectVersions(directory string) map[string]string {
	versions := make(map[string]string)
	start := filepath.Clean(directory)
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)
	hasRepository := findGitDirectory(directory) != ""
	for current := start; ; current = filepath.Dir(current) {
		if home != "." && current == home && !hasRepository {
			break
		}
		detected := detectProjectVersionsAt(current)
		for name, value := range detected {
			if _, exists := versions[name]; !exists {
				versions[name] = value
			}
		}
		_, gitErr := os.Stat(filepath.Join(current, ".git"))
		if _, ok := versions["php"]; !ok && (current == start || gitErr == nil) && hasExtensionAtDepth(current, ".php", 2) {
			versions["php"] = ""
			detected["php"] = ""
		}
		if _, ok := versions["docker"]; !ok && (current == start || gitErr == nil) && hasNamedPrefix(current, "Dockerfile.") {
			versions["docker"] = ""
			detected["docker"] = ""
		}
		if gitErr == nil {
			break
		}
		if len(detected) > 0 && !hasRepository {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return versions
}

func detectProjectVersionsAt(directory string) map[string]string {
	versions := make(map[string]string)
	nodeRelevant := false
	if content, err := os.ReadFile(filepath.Join(directory, "go.mod")); err == nil {
		versions["go"] = directiveValue(string(content), "toolchain")
		if versions["go"] == "" {
			versions["go"] = directiveValue(string(content), "go")
		}
		versions["go"] = strings.TrimPrefix(versions["go"], "go")
	} else if anyFile(directory, "go.work", "go.work.sum") {
		versions["go"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, "package.json")); err == nil {
		nodeRelevant = true
		var pkg struct {
			Engines        map[string]string `json:"engines"`
			PackageManager string            `json:"packageManager"`
		}
		if json.Unmarshal(content, &pkg) == nil {
			if version := declaredVersion(pkg.Engines["node"]); version != "" {
				versions["node"] = version
			}
			if value, ok := strings.CutPrefix(pkg.PackageManager, "bun@"); ok {
				versions["bun"] = value
			}
		}
	}
	if _, ok := versions["node"]; !ok {
		for _, name := range []string{".node-version", ".nvmrc"} {
			if content, err := os.ReadFile(filepath.Join(directory, name)); err == nil {
				versions["node"] = strings.TrimSpace(string(content))
				break
			}
		}
	}
	if content, err := os.ReadFile(filepath.Join(directory, ".tool-versions")); err == nil {
		if _, ok := versions["node"]; !ok {
			versions["node"] = toolVersion(string(content), "nodejs", "node")
		}
	}
	if _, ok := versions["node"]; !ok && (nodeRelevant || anyFile(directory, "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml")) {
		versions["node"] = ""
	}
	if _, ok := versions["bun"]; !ok && anyFile(directory, "bun.lock", "bun.lockb", "bunfig.toml") {
		versions["bun"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, "pyproject.toml")); err == nil {
		versions["python"] = declaredVersion(quotedAssignment(string(content), "requires-python"))
	} else if anyFile(directory, "uv.lock", "poetry.lock", "Pipfile", "Pipfile.lock", "requirements.txt") ||
		hasNamedPrefix(directory, "requirements") {
		versions["python"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, ".python-version")); err == nil {
		versions["python"] = strings.TrimSpace(string(content))
	}
	if content, err := os.ReadFile(filepath.Join(directory, ".ruby-version")); err == nil {
		versions["ruby"] = strings.TrimSpace(string(content))
	} else if anyFile(directory, "Gemfile", "Gemfile.lock") || hasNamedSuffix(directory, ".gemspec") {
		versions["ruby"] = ""
	}
	if anyFile(directory, "mix.exs", "mix.lock") {
		versions["elixir"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, ".tool-versions")); err == nil {
		for tool, names := range map[string][]string{
			"python": {"python"},
			"ruby":   {"ruby"},
			"elixir": {"elixir"},
		} {
			if value := toolVersion(string(content), names...); value != "" {
				versions[tool] = value
			}
		}
	}
	if content, err := os.ReadFile(filepath.Join(directory, "rust-toolchain")); err == nil {
		versions["rust"] = strings.TrimSpace(string(content))
	} else if content, err := os.ReadFile(filepath.Join(directory, "rust-toolchain.toml")); err == nil {
		versions["rust"] = quotedAssignment(string(content), "channel")
	} else if anyFile(directory, "Cargo.toml", "Cargo.lock") {
		versions["rust"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, "composer.json")); err == nil {
		var composer struct {
			Require map[string]string `json:"require"`
		}
		if json.Unmarshal(content, &composer) == nil {
			versions["php"] = declaredVersion(composer.Require["php"])
			if laravel, ok := composer.Require["laravel/framework"]; ok {
				versions["laravel"] = declaredVersion(laravel)
			}
		}
		versions["composer"] = ""
	}
	if content, err := os.ReadFile(filepath.Join(directory, ".php-version")); err == nil {
		versions["php"] = strings.TrimSpace(string(content))
	}
	if content, err := os.ReadFile(filepath.Join(directory, "composer.lock")); err == nil {
		if _, ok := versions["php"]; !ok {
			versions["php"] = ""
		}
		versions["composer"] = ""
		var lock struct {
			Packages    []composerPackage `json:"packages"`
			PackagesDev []composerPackage `json:"packages-dev"`
		}
		if json.Unmarshal(content, &lock) == nil {
			for _, pkg := range append(lock.Packages, lock.PackagesDev...) {
				if pkg.Name == "laravel/framework" {
					versions["laravel"] = strings.TrimPrefix(pkg.Version, "v")
					break
				}
			}
		}
	}
	if _, ok := versions["laravel"]; !ok {
		if content, err := os.ReadFile(filepath.Join(directory, "vendor", "composer", "installed.json")); err == nil {
			for _, pkg := range installedComposerPackages(content) {
				if pkg.Name == "laravel/framework" {
					value := pkg.PrettyVersion
					if value == "" {
						value = pkg.Version
					}
					versions["laravel"] = strings.TrimPrefix(value, "v")
					break
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "artisan")); err == nil {
		if _, ok := versions["laravel"]; !ok {
			versions["laravel"] = ""
		}
	}
	dockerRelevant := anyFile(directory, "Dockerfile", ".dockerignore", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml")
	if dockerRelevant {
		versions["docker"] = ""
	}
	if anyFile(directory, "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml") {
		versions["docker-compose"] = ""
	}
	return versions
}

type composerPackage struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	PrettyVersion string `json:"pretty_version"`
}

func installedComposerPackages(content []byte) []composerPackage {
	var document struct {
		Packages []composerPackage `json:"packages"`
	}
	if json.Unmarshal(content, &document) == nil && document.Packages != nil {
		return document.Packages
	}
	var packages []composerPackage
	if json.Unmarshal(content, &packages) == nil {
		return packages
	}
	return nil
}

func hasNamedPrefix(directory, prefix string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			return true
		}
	}
	return false
}

func hasNamedSuffix(directory, suffix string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return true
		}
	}
	return false
}

var declaredVersionPattern = regexp.MustCompile(`\d+(?:\.\d+){0,2}`)

func declaredVersion(value string) string {
	return declaredVersionPattern.FindString(value)
}

func toolVersion(content string, names ...string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, name := range names {
			if fields[0] == name {
				return fields[1]
			}
		}
	}
	return ""
}

func hasExtensionAtDepth(directory, extension string, depth int) bool {
	type entry struct {
		path  string
		depth int
	}
	queue := []entry{{path: directory}}
	visited := 0
	for len(queue) > 0 && visited < 256 {
		current := queue[0]
		queue = queue[1:]
		visited++
		entries, err := os.ReadDir(current.path)
		if err != nil {
			continue
		}
		for _, candidate := range entries {
			if candidate.IsDir() {
				if current.depth < depth && candidate.Name() != ".git" && candidate.Name() != "vendor" && candidate.Name() != "node_modules" {
					queue = append(queue, entry{path: filepath.Join(current.path, candidate.Name()), depth: current.depth + 1})
				}
				continue
			}
			if filepath.Ext(candidate.Name()) == extension {
				return true
			}
		}
	}
	return false
}

func directiveValue(content, directive string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == directive {
			return fields[1]
		}
	}
	return ""
}

func quotedAssignment(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == key {
			return strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	return ""
}

func anyFile(directory string, names ...string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
			return true
		}
	}
	return false
}

func normalizeToolVersion(name, output string) string {
	value := strings.TrimSpace(output)
	switch name {
	case "python":
		return declaredVersion(strings.TrimPrefix(value, "Python "))
	case "ruby":
		return declaredVersion(strings.TrimPrefix(value, "ruby "))
	case "elixir":
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); strings.HasPrefix(line, "Elixir ") {
				return declaredVersion(strings.TrimPrefix(line, "Elixir "))
			}
		}
		return ""
	}
	for _, prefix := range []string{"Laravel Framework ", "PHP ", "Composer version ", "go version go", "go", "v", "rustc ", "Docker version "} {
		value = strings.TrimPrefix(value, prefix)
	}
	if name == "docker" {
		value = strings.TrimSuffix(strings.Split(value, ",")[0], ",")
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func cloneVersions(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

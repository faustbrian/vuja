package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type WorkspaceInfo struct {
	Root             string
	HasGit           bool
	GitBranch        string
	GitMerging       bool
	GitRebasing      bool
	GitCherryPicking bool
	GitReverting     bool
	GitDirty         bool
	GitStaged        bool
	GitConflicted    bool
	GitAhead         int
	GitBehind        int
	HasNodeProject   bool
	HasGoProject     bool
	HasRustProject   bool
	HasPythonProject bool
	HasDockerfile    bool
	HasMakefile      bool
	HasJustfile      bool
	HasK8s           bool
	SignatureFiles   []string
}

var signatureChecks = []struct {
	path  string
	field func(*WorkspaceInfo)
}{
	{".git", func(w *WorkspaceInfo) { w.HasGit = true }},
	{"package.json", func(w *WorkspaceInfo) { w.HasNodeProject = true }},
	{"go.mod", func(w *WorkspaceInfo) { w.HasGoProject = true }},
	{"Cargo.toml", func(w *WorkspaceInfo) { w.HasRustProject = true }},
	{"Dockerfile", func(w *WorkspaceInfo) { w.HasDockerfile = true }},
	{"Makefile", func(w *WorkspaceInfo) { w.HasMakefile = true }},
	{"justfile", func(w *WorkspaceInfo) { w.HasJustfile = true }},
	{"pyproject.toml", func(w *WorkspaceInfo) { w.HasPythonProject = true }},
	{"requirements.txt", func(w *WorkspaceInfo) { w.HasPythonProject = true }},
	{"Chart.yaml", func(w *WorkspaceInfo) { w.HasK8s = true }},
	{"k8s", func(w *WorkspaceInfo) { w.HasK8s = true }},
	{"kubernetes", func(w *WorkspaceInfo) { w.HasK8s = true }},
	{"docker-compose.yml", func(w *WorkspaceInfo) { w.HasDockerfile = true }},
	{"docker-compose.yaml", func(w *WorkspaceInfo) { w.HasDockerfile = true }},
	{"Taskfile.yml", nil},
	{"pom.xml", nil},
	{"build.gradle", nil},
	{"CMakeLists.txt", nil},
}

// Detect scans the current project hierarchy for signature files and returns
// workspace metadata rooted at the containing Git repository or nearest
// project marker.
func Detect(cwd string) WorkspaceInfo {
	cwd = filepath.Clean(cwd)
	hasGit, gitRoot, _, _ := resolveGitMetadata(cwd)
	info := WorkspaceInfo{Root: gitRoot, HasGit: hasGit}

	stop := gitRoot
	if stop == "" {
		stop = findNearestProjectRoot(cwd)
		info.Root = stop
	}
	if stop == "" {
		stop = cwd
	}

	seen := make(map[string]bool)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, check := range signatureChecks {
			fullPath := filepath.Join(dir, check.path)
			if _, err := os.Stat(fullPath); err != nil {
				continue
			}
			label := check.path
			if dir != cwd {
				relative, err := filepath.Rel(stop, fullPath)
				if err == nil {
					label = relative
				}
			}
			if !seen[label] {
				info.SignatureFiles = append(info.SignatureFiles, label)
				seen[label] = true
			}
			if check.field != nil {
				check.field(&info)
			}
		}
		if dir == stop || filepath.Dir(dir) == dir {
			break
		}
	}

	_, info.GitBranch = detectGitInfo(cwd)
	if hasGit {
		_, _, gitDir, _ := resolveGitMetadata(cwd)
		if gitDir != "" {
			info.GitMerging = pathExists(filepath.Join(gitDir, "MERGE_HEAD"))
			info.GitRebasing = pathExists(filepath.Join(gitDir, "rebase-merge")) || pathExists(filepath.Join(gitDir, "rebase-apply"))
			info.GitCherryPicking = pathExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
			info.GitReverting = pathExists(filepath.Join(gitDir, "REVERT_HEAD"))
		}
	}

	return info
}

// WithGitStatusCached adds asynchronously refreshed working-tree state.
func WithGitStatusCached(info WorkspaceInfo) WorkspaceInfo {
	state := cachedGitStatus(info.Root)
	info.GitDirty = state.dirty
	info.GitStaged = state.staged
	info.GitConflicted = state.conflicted
	info.GitAhead = state.ahead
	info.GitBehind = state.behind
	return info
}

type gitStatus struct {
	dirty      bool
	staged     bool
	conflicted bool
	ahead      int
	behind     int
	updatedAt  time.Time
	refreshing bool
}

var gitStatusCache struct {
	sync.Mutex
	entries map[string]gitStatus
}

func cachedGitStatus(root string) gitStatus {
	if root == "" {
		return gitStatus{}
	}
	gitStatusCache.Lock()
	if gitStatusCache.entries == nil {
		gitStatusCache.entries = make(map[string]gitStatus)
	}
	state := gitStatusCache.entries[root]
	if !state.refreshing && time.Since(state.updatedAt) >= 2*time.Second {
		state.refreshing = true
		gitStatusCache.entries[root] = state
		go refreshGitStatus(root)
	}
	gitStatusCache.Unlock()
	return state
}

func refreshGitStatus(root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v2", "--branch", "--untracked-files=no").Output()
	state := gitStatus{updatedAt: time.Now()}
	if err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			switch {
			case strings.HasPrefix(line, "# branch.ab "):
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					state.ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[2], "+"))
					state.behind, _ = strconv.Atoi(strings.TrimPrefix(fields[3], "-"))
				}
			case strings.HasPrefix(line, "u "):
				state.conflicted, state.dirty, state.staged = true, true, true
			case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
				fields := strings.Fields(line)
				if len(fields) < 2 || len(fields[1]) < 2 {
					continue
				}
				state.staged = state.staged || fields[1][0] != '.'
				state.dirty = state.dirty || fields[1][1] != '.'
			}
		}
	}
	gitStatusCache.Lock()
	gitStatusCache.entries[root] = state
	gitStatusCache.Unlock()
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func findNearestProjectRoot(cwd string) string {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, check := range signatureChecks {
			if check.path == ".git" {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, check.path)); err == nil {
				return dir
			}
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

func resolveGitMetadata(cwd string) (hasGit bool, root string, gitDir string, headPath string) {
	dir := cwd
	for dir != "" {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return true, dir, gitPath, filepath.Join(gitPath, "HEAD")
			}
			content, errRead := os.ReadFile(gitPath)
			if errRead == nil {
				s := strings.TrimSpace(string(content))
				if after, ok := strings.CutPrefix(s, "gitdir: "); ok {
					gitDir = strings.TrimSpace(after)
					if !filepath.IsAbs(gitDir) {
						gitDir = filepath.Join(dir, gitDir)
					}
					return true, dir, gitDir, filepath.Join(gitDir, "HEAD")
				}
			}
			return true, dir, "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false, "", "", ""
}

func resolveGitHeadPath(cwd string) (hasGit bool, headPath string) {
	hasGit, _, _, headPath = resolveGitMetadata(cwd)
	return hasGit, headPath
}

func detectGitInfo(cwd string) (hasGit bool, branch string) {
	hasGit, headPath := resolveGitHeadPath(cwd)
	if headPath != "" {
		if data, errHead := os.ReadFile(headPath); errHead == nil {
			s := strings.TrimSpace(string(data))
			if after, ok := strings.CutPrefix(s, "ref: refs/heads/"); ok {
				return hasGit, after
			}
		}
	}
	return hasGit, ""
}

type cacheEntry struct {
	key  string // cwd + "|" + dirModTime
	info WorkspaceInfo
}

var (
	wsCache   *cacheEntry
	wsCacheMu sync.Mutex
)

// DetectCached returns cached workspace info, invalidating when directory modtime changes
// or when the Git HEAD file changes (to catch branch switches that don't affect cwd modtime).
func DetectCached(cwd string) WorkspaceInfo {
	dirInfo, err := os.Stat(cwd)
	if err != nil {
		return Detect(cwd)
	}
	key := cwd + "|" + dirInfo.ModTime().String()

	_, root, gitDir, headPath := resolveGitMetadata(cwd)
	if root == "" {
		root = findNearestProjectRoot(cwd)
	}
	if root != "" && root != cwd {
		if rootInfo, rootErr := os.Stat(root); rootErr == nil {
			key += "|ROOT:" + rootInfo.ModTime().String()
		}
	}
	if headPath != "" {
		if headInfo, err := os.Stat(headPath); err == nil {
			key += "|HEAD:" + headInfo.ModTime().String()
		}
	}
	if gitDir != "" {
		for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
			if info, markerErr := os.Stat(filepath.Join(gitDir, marker)); markerErr == nil {
				key += "|" + marker + ":" + info.ModTime().String()
			}
		}
	}

	wsCacheMu.Lock()
	defer wsCacheMu.Unlock()

	if wsCache != nil && wsCache.key == key {
		return wsCache.info
	}

	info := Detect(cwd)
	wsCache = &cacheEntry{key: key, info: info}
	return info
}

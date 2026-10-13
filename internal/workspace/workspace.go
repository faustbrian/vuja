package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/faustbrian/vuja/internal/health"
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
	return DetectContext(context.Background(), cwd)
}

// DetectContext observes a fresh acquisition, not an inferred cached success.
func DetectContext(ctx context.Context, cwd string) WorkspaceInfo {
	p := newMetadataProbe(ctx)
	started := time.Now()
	info := p.detect(cwd)
	health.Report(p.ctx, health.Workspace, time.Since(started), p.err)
	return info
}

type metadataProbe struct {
	ctx context.Context
	err error
}

func newMetadataProbe(ctx context.Context) *metadataProbe {
	if ctx == nil {
		ctx = context.Background()
	}
	return &metadataProbe{ctx: ctx}
}

func (p *metadataProbe) failure(err error) {
	if p.err == nil && err != nil && !errors.Is(err, os.ErrNotExist) {
		p.err = err
	}
}

func (p *metadataProbe) stat(path string) (os.FileInfo, error) {
	if err := p.ctx.Err(); err != nil {
		p.failure(err)
		return nil, err
	}
	info, err := os.Stat(path)
	p.failure(err)
	return info, err
}

// Reads are required metadata of an already discovered repository, unlike
// optional signature/operation-marker probes. Absence cannot restore health.
func (p *metadataProbe) read(path string) ([]byte, error) {
	if err := p.ctx.Err(); err != nil {
		p.failure(err)
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		p.failure(errors.New("required Git metadata unavailable"))
	} else {
		p.failure(err)
	}
	return data, err
}

func (p *metadataProbe) detect(cwd string) WorkspaceInfo {
	cwd = filepath.Clean(cwd)
	if dir, err := p.stat(cwd); err != nil || !dir.IsDir() {
		if p.ctx.Err() == nil && p.err == nil {
			p.err = errors.New("workspace directory unavailable")
		}
	}
	hasGit, gitRoot, gitDir, headPath := p.resolveGitMetadata(cwd)
	info := WorkspaceInfo{Root: gitRoot, HasGit: hasGit}

	stop := gitRoot
	if stop == "" {
		stop = p.findNearestProjectRoot(cwd)
		info.Root = stop
	}
	if stop == "" {
		stop = cwd
	}

	seen := make(map[string]bool)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if p.ctx.Err() != nil {
			break
		}
		for _, check := range signatureChecks {
			fullPath := filepath.Join(dir, check.path)
			if _, err := p.stat(fullPath); err != nil {
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

	if headPath != "" {
		if data, err := p.read(headPath); err == nil {
			if branch, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/"); ok {
				info.GitBranch = branch
			}
		}
	}
	if hasGit && gitDir != "" {
		info.GitMerging = p.pathExists(filepath.Join(gitDir, "MERGE_HEAD"))
		info.GitRebasing = p.pathExists(filepath.Join(gitDir, "rebase-merge")) || p.pathExists(filepath.Join(gitDir, "rebase-apply"))
		info.GitCherryPicking = p.pathExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
		info.GitReverting = p.pathExists(filepath.Join(gitDir, "REVERT_HEAD"))
	}

	return info
}

// WithGitStatusCached adds asynchronously refreshed working-tree state.
func WithGitStatusCached(info WorkspaceInfo) WorkspaceInfo {
	return WithGitStatusCachedContext(context.Background(), info)
}

func WithGitStatusCachedContext(ctx context.Context, info WorkspaceInfo) WorkspaceInfo {
	state := cachedGitStatusContext(ctx, info.Root)
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

const gitStatusCacheLimit = 128

func cachedGitStatus(root string) gitStatus {
	return cachedGitStatusContext(context.Background(), root)
}

func cachedGitStatusContext(ctx context.Context, root string) gitStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	if root == "" {
		return gitStatus{}
	}
	gitStatusCache.Lock()
	if gitStatusCache.entries == nil {
		gitStatusCache.entries = make(map[string]gitStatus)
	}
	state := gitStatusCache.entries[root]
	if ctx.Err() == nil && !state.refreshing && time.Since(state.updatedAt) >= 2*time.Second {
		state.refreshing = true
		gitStatusCache.entries[root] = state
		go refreshGitStatusContext(ctx, root)
	}
	pruneGitStatusCacheLocked(root)
	gitStatusCache.Unlock()
	return state
}

func refreshGitStatus(root string) {
	refreshGitStatusContext(context.Background(), root)
}

func refreshGitStatusContext(parent context.Context, root string) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 150*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v2", "--branch", "--untracked-files=no").Output()
	health.Report(ctx, health.WorkspaceGitStatus, time.Since(started), err)
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
	if err != nil {
		// Unknown is not clean. Keep usable previous facts and the normal
		// refresh cadence; only an actual successful query can replace them.
		previous := gitStatusCache.entries[root]
		if !errors.Is(ctx.Err(), context.Canceled) {
			previous.updatedAt = state.updatedAt
		}
		previous.refreshing = false
		state = previous
	}
	gitStatusCache.entries[root] = state
	pruneGitStatusCacheLocked(root)
	gitStatusCache.Unlock()
}

func pruneGitStatusCacheLocked(currentRoot string) {
	for len(gitStatusCache.entries) > gitStatusCacheLimit {
		oldestRoot := ""
		var oldest time.Time
		for root, state := range gitStatusCache.entries {
			if root == currentRoot || state.refreshing {
				continue
			}
			if oldestRoot == "" || state.updatedAt.Before(oldest) {
				oldestRoot, oldest = root, state.updatedAt
			}
		}
		if oldestRoot == "" {
			return
		}
		delete(gitStatusCache.entries, oldestRoot)
	}
}

func pathExists(path string) bool {
	return newMetadataProbe(context.Background()).pathExists(path)
}

func (p *metadataProbe) pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := p.stat(path)
	return err == nil
}

func findNearestProjectRoot(cwd string) string {
	return newMetadataProbe(context.Background()).findNearestProjectRoot(cwd)
}

func (p *metadataProbe) findNearestProjectRoot(cwd string) string {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if p.ctx.Err() != nil {
			return ""
		}
		for _, check := range signatureChecks {
			if check.path == ".git" {
				continue
			}
			if _, err := p.stat(filepath.Join(dir, check.path)); err == nil {
				return dir
			}
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
}

func resolveGitMetadata(cwd string) (hasGit bool, root string, gitDir string, headPath string) {
	return newMetadataProbe(context.Background()).resolveGitMetadata(cwd)
}

func (p *metadataProbe) resolveGitMetadata(cwd string) (hasGit bool, root string, gitDir string, headPath string) {
	dir := cwd
	for dir != "" {
		if p.ctx.Err() != nil {
			return false, "", "", ""
		}
		gitPath := filepath.Join(dir, ".git")
		info, err := p.stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return true, dir, gitPath, filepath.Join(gitPath, "HEAD")
			}
			content, errRead := p.read(gitPath)
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
	cwd       string
	key       string // cwd + "|" + dirModTime
	info      WorkspaceInfo
	updatedAt time.Time
}

var (
	wsCache   *cacheEntry
	wsCacheMu sync.Mutex
)

// DetectCached returns cached workspace info, invalidating when directory modtime changes
// or when the Git HEAD file changes (to catch branch switches that don't affect cwd modtime).
func DetectCached(cwd string) WorkspaceInfo {
	return DetectCachedContext(context.Background(), cwd)
}

func metadataCacheKey(info os.FileInfo) string {
	return info.ModTime().String() + ":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatUint(uint64(info.Mode()), 10)
}

func DetectCachedContext(ctx context.Context, cwd string) WorkspaceInfo {
	p := newMetadataProbe(ctx)
	started := time.Now()
	fresh := false
	defer func() {
		if fresh || p.err != nil || p.ctx.Err() != nil {
			health.Report(p.ctx, health.Workspace, time.Since(started), p.err)
		}
	}()
	cwd = filepath.Clean(cwd)
	dirInfo, err := p.stat(cwd)
	if err != nil || !dirInfo.IsDir() {
		fresh = true
		return p.detect(cwd)
	}
	key := cwd + "|" + metadataCacheKey(dirInfo)

	_, root, gitDir, headPath := p.resolveGitMetadata(cwd)
	if root == "" {
		root = p.findNearestProjectRoot(cwd)
	}
	if root != "" && root != cwd {
		if rootInfo, rootErr := p.stat(root); rootErr == nil {
			key += "|ROOT:" + metadataCacheKey(rootInfo)
		}
	}
	if headPath != "" {
		if headInfo, err := p.stat(headPath); err == nil {
			key += "|HEAD:" + metadataCacheKey(headInfo)
		}
	}
	if gitDir != "" {
		for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
			if info, markerErr := p.stat(filepath.Join(gitDir, marker)); markerErr == nil {
				key += "|" + marker + ":" + metadataCacheKey(info)
			}
		}
	}

	wsCacheMu.Lock()
	if wsCache != nil && wsCache.key == key && p.err == nil && p.ctx.Err() == nil {
		wsCache.updatedAt = time.Now()
		info := wsCache.info
		wsCacheMu.Unlock()
		return info
	}
	wsCacheMu.Unlock()

	// Traversal can touch dozens of markers. Keep it outside the cache lock so
	// a stale refresh cannot block readers of the last published snapshot.
	fresh = true
	info := p.detect(cwd)
	if p.err != nil || p.ctx.Err() != nil {
		return info
	}
	wsCacheMu.Lock()
	wsCache = &cacheEntry{cwd: cwd, key: key, info: info, updatedAt: time.Now()}
	wsCacheMu.Unlock()
	return info
}

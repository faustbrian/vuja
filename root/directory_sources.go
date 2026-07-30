package root

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faustbrian/vuja/integration"
	"github.com/faustbrian/vuja/internal/scoring"
	"github.com/faustbrian/vuja/internal/workspace"
)

func parseZoxideDirectories(output string) []scoring.DirectoryImport {
	now := time.Now()
	entries := make([]scoring.DirectoryImport, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		separator := strings.IndexAny(line, " \t")
		if separator < 1 {
			continue
		}
		score, err := strconv.ParseFloat(strings.TrimSpace(line[:separator]), 64)
		path := filepath.Clean(strings.TrimSpace(line[separator:]))
		if err != nil || path == "." || !filepath.IsAbs(path) {
			continue
		}
		entries = append(entries, scoring.DirectoryImport{
			Path:     path,
			Count:    max(1, int(score)),
			LastUsed: now,
		})
	}
	return entries
}

func loadZoxideDirectories(ctx context.Context) []scoring.DirectoryImport {
	path, err := exec.LookPath("zoxide")
	if err != nil {
		return nil
	}
	output, err := exec.CommandContext(ctx, path, "query", "-ls").Output()
	if err != nil {
		return nil
	}
	return parseZoxideDirectories(string(output))
}

func historyDirectoryImports(stats []integration.HistoryStat) []scoring.DirectoryImport {
	byPath := make(map[string]scoring.DirectoryImport)
	for _, stat := range stats {
		path := filepath.Clean(strings.TrimSpace(stat.Cwd))
		if path == "." || !filepath.IsAbs(path) {
			continue
		}
		entry := byPath[path]
		entry.Path = path
		entry.Count += max(stat.Count, 1)
		if stat.LastUsed.After(entry.LastUsed) {
			entry.LastUsed = stat.LastUsed
		}
		byPath[path] = entry
	}
	entries := make([]scoring.DirectoryImport, 0, len(byPath))
	for _, entry := range byPath {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Path < entries[j].Path
	})
	return entries
}

func gitWorktreeImports(currentDirectory string, history []scoring.DirectoryImport) []scoring.DirectoryImport {
	roots := make(map[string]bool)
	addRoot := func(path string) {
		if path == "" {
			return
		}
		info := workspace.DetectCached(path)
		if info.HasGit && info.Root != "" {
			roots[info.Root] = true
		}
	}
	addRoot(currentDirectory)
	for index, entry := range history {
		if index == 200 {
			break
		}
		addRoot(entry.Path)
	}

	now := time.Now()
	paths := make(map[string]bool)
	for root := range roots {
		paths[root] = true
		for _, path := range discoverGitWorktrees(root) {
			paths[path] = true
		}
	}
	entries := make([]scoring.DirectoryImport, 0, len(paths))
	for path := range paths {
		entries = append(entries, scoring.DirectoryImport{Path: path, Count: 1, LastUsed: now})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func discoverGitWorktrees(root string) []string {
	commonGitDirectory := resolveCommonGitDirectory(root)
	if commonGitDirectory == "" {
		return nil
	}
	metadata, err := os.ReadDir(filepath.Join(commonGitDirectory, "worktrees"))
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, item := range metadata {
		if !item.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(commonGitDirectory, "worktrees", item.Name(), "gitdir"))
		if err != nil {
			continue
		}
		gitFile := filepath.Clean(strings.TrimSpace(string(content)))
		if gitFile == "." || !filepath.IsAbs(gitFile) {
			continue
		}
		seen[filepath.Dir(gitFile)] = true
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func resolveCommonGitDirectory(root string) string {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return gitPath
	}
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	gitDirectory, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
	if !ok {
		return ""
	}
	gitDirectory = strings.TrimSpace(gitDirectory)
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(root, gitDirectory)
	}
	commonContent, err := os.ReadFile(filepath.Join(gitDirectory, "commondir"))
	if err == nil {
		commonDirectory := strings.TrimSpace(string(commonContent))
		if !filepath.IsAbs(commonDirectory) {
			commonDirectory = filepath.Join(gitDirectory, commonDirectory)
		}
		return filepath.Clean(commonDirectory)
	}
	return filepath.Clean(filepath.Join(gitDirectory, "..", ".."))
}

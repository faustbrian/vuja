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
	"github.com/faustbrian/vuja/spec"
)

const directoryRecentWindow = 14 * 24 * time.Hour

func parseZoxideDirectories(output string) []scoring.DirectoryImport {
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
			Path:  path,
			Count: max(1, int(score)),
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

func historyNavigationDirectoryImports(events []integration.HistoryEntry, now time.Time) []scoring.DirectoryImport {
	byPath := make(map[string]scoring.DirectoryImport)
	for _, event := range events {
		path, ok := importedNavigationDestination(event.Command, event.Cwd)
		if !ok {
			continue
		}
		entry := byPath[path]
		entry.Path = path
		entry.Count++
		if !event.StartedAt.IsZero() && !event.StartedAt.Before(now.Add(-directoryRecentWindow)) {
			entry.RecentCount++
		}
		if event.StartedAt.After(entry.LastUsed) {
			entry.LastUsed = event.StartedAt
		}
		byPath[path] = entry
	}
	entries := make([]scoring.DirectoryImport, 0, len(byPath))
	for _, entry := range byPath {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RecentCount != entries[j].RecentCount {
			return entries[i].RecentCount > entries[j].RecentCount
		}
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Path < entries[j].Path
	})
	return entries
}

func importedNavigationDestination(command, cwd string) (string, bool) {
	tokens := spec.Tokenize(strings.TrimSpace(command))
	if len(tokens) == 1 && tokens[0] == "cd" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Clean(home), true
	}
	if len(tokens) == 3 && tokens[0] == "cd" && tokens[1] == "--" {
		tokens = []string{"cd", tokens[2]}
	}
	if len(tokens) != 2 || tokens[0] != "cd" {
		return "", false
	}
	base := filepath.Clean(strings.TrimSpace(cwd))
	if !filepath.IsAbs(base) {
		return "", false
	}
	target := strings.TrimSpace(tokens[1])
	if target == "" || target == "-" || strings.ContainsAny(target, "$`*?{}[]") {
		return "", false
	}
	if target == "~" || strings.HasPrefix(target, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		target = filepath.Join(home, strings.TrimPrefix(target, "~/"))
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return filepath.Clean(target), true
}

func isDirectoryNavigationCommand(command string) bool {
	tokens := spec.Tokenize(strings.TrimSpace(command))
	return len(tokens) > 0 && (tokens[0] == "cd" || tokens[0] == "z")
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

	paths := make(map[string]bool)
	for root := range roots {
		paths[root] = true
		for _, path := range discoverGitWorktrees(root) {
			paths[path] = true
		}
	}
	entries := make([]scoring.DirectoryImport, 0, len(paths))
	for path := range paths {
		entries = append(entries, scoring.DirectoryImport{Path: path, Count: 1})
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

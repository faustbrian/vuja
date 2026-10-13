package runner

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/faustbrian/vuja/internal/health"
	"github.com/faustbrian/vuja/spec"
)

func init() {
	spec.Register(&spec.Spec{
		Name:        "just",
		Description: "command runner",
		MaxArgs:     1,
		Generator: spec.ImmediateGenerator(func(ctx context.Context, tokens []string, prefix string, partial string) []spec.Suggestion {
			started := time.Now()
			var providerErr error
			defer func() { health.Report(ctx, health.JustRecipes, time.Since(started), providerErr) }()
			cwd := spec.GetCWD()
			file, err := os.Open(filepath.Join(cwd, "justfile"))
			if err != nil {
				lowerErr := err
				// try uppercase
				file, err = os.Open(filepath.Join(cwd, "Justfile"))
				if err != nil {
					providerErr = err
					// A missing fallback must not hide an unreadable primary.
					if errors.Is(err, os.ErrNotExist) && !errors.Is(lowerErr, os.ErrNotExist) {
						providerErr = lowerErr
					}
					return nil
				}
			}
			defer func() { _ = file.Close() }()

			var suggestions []spec.Suggestion
			seen := make(map[string]bool)
			scanner := bufio.NewScanner(file)
			// matching recipes like `build:`
			recipeRegex := regexp.MustCompile(`^([a-zA-Z0-9_-]+):`)

			lastComment := ""
			for scanner.Scan() {
				if ctx.Err() != nil {
					return nil
				}
				line := strings.TrimSpace(scanner.Text())

				// Handle comments
				if after, ok := strings.CutPrefix(line, "#"); ok {
					lastComment = strings.TrimSpace(after)
					continue
				}

				// Skip blank lines or group attributes
				if line == "" || strings.HasPrefix(line, "[") {
					continue
				}

				if matches := recipeRegex.FindStringSubmatch(line); len(matches) > 1 {
					recipe := matches[1]
					if seen[recipe] {
						lastComment = ""
						continue
					}
					seen[recipe] = true

					cmd := recipe

					desc := "just recipe"
					if lastComment != "" {
						desc = lastComment
					}

					suggestions = append(suggestions, spec.Suggestion{
						Cmd:  cmd,
						Desc: desc,
					})
					lastComment = ""
				} else {
					// If it's something else (like a command line), reset comment
					// unless it's just indented (which we already skip or handle)
					if !strings.HasPrefix(scanner.Text(), " ") && !strings.HasPrefix(scanner.Text(), "\t") {
						lastComment = ""
					}
				}
			}
			providerErr = scanner.Err()
			if providerErr != nil {
				return nil
			}
			return suggestions
		}),
	})
}

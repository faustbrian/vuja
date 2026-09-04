package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type SpecInfo struct {
	Name        string
	Description string
	File        string
}

type Section struct {
	Cat   string
	Title string
	Specs []SpecInfo
}

type CategoryMeta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type CategoriesConfig struct {
	Categories []CategoryMeta `json:"categories"`
}

func main() {
	commandsDir := "commands"
	if len(os.Args) > 1 {
		commandsDir = os.Args[1]
	} else if _, err := os.Stat(commandsDir); os.IsNotExist(err) {
		if _, err2 := os.Stat("../commands"); err2 == nil {
			commandsDir = "../commands"
		}
	}

	metaFile := filepath.Join(commandsDir, "categories.json")
	metaData, err := os.ReadFile(metaFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", metaFile, err)
		os.Exit(1)
	}

	var config CategoriesConfig
	if unmarshalErr := json.Unmarshal(metaData, &config); unmarshalErr != nil {
		fmt.Fprintf(os.Stderr, "Error parsing %s: %v\n", metaFile, unmarshalErr)
		os.Exit(1)
	}

	orderMap := make(map[string]int)
	titleMap := make(map[string]string)
	for idx, cat := range config.Categories {
		orderMap[cat.ID] = idx
		titleMap[cat.ID] = cat.Title
	}

	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading commands dir %s: %v\n", commandsDir, err)
		os.Exit(1)
	}

	nameRe := regexp.MustCompile(`Name:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	descRe := regexp.MustCompile(`Description:\s*["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`)

	var sections []Section
	totalCommands := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cat := entry.Name()
		if cat == "core" || strings.HasPrefix(cat, ".") || strings.HasPrefix(cat, "_") {
			continue
		}

		title, ok := titleMap[cat]
		if !ok {
			if len(cat) <= 3 {
				title = strings.ToUpper(cat) + " Tools"
			} else {
				title = strings.ToUpper(cat[:1]) + cat[1:] + " Tools"
			}
		}

		dirPath := filepath.Join(commandsDir, cat)
		files, readDirErr := os.ReadDir(dirPath)
		if readDirErr != nil {
			continue
		}

		var specs []SpecInfo
		seen := make(map[string]bool)

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
				continue
			}
			content, readFileErr := os.ReadFile(filepath.Join(dirPath, f.Name()))
			if readFileErr != nil {
				continue
			}

			blocks := strings.Split(string(content), "spec.Register(")
			for i := 1; i < len(blocks); i++ {
				block := blocks[i]
				nameMatch := nameRe.FindStringSubmatch(block)
				if nameMatch == nil {
					continue
				}
				name := nameMatch[1]
				if seen[name] {
					continue
				}
				seen[name] = true

				desc := "(no description)"
				descMatch := descRe.FindStringSubmatch(block)
				if descMatch != nil && descMatch[1] != "" {
					desc = descMatch[1]
				}
				desc = strings.ReplaceAll(desc, "\n", " ")
				desc = strings.ReplaceAll(desc, "|", "\\|")

				specs = append(specs, SpecInfo{
					Name:        name,
					Description: desc,
					File:        f.Name(),
				})
			}
		}

		if len(specs) == 0 {
			continue
		}

		sort.Slice(specs, func(i, j int) bool {
			return strings.Compare(specs[i].Name, specs[j].Name) < 0
		})

		totalCommands += len(specs)
		sections = append(sections, Section{
			Cat:   cat,
			Title: title,
			Specs: specs,
		})
	}

	sort.Slice(sections, func(i, j int) bool {
		idxI, okI := orderMap[sections[i].Cat]
		idxJ, okJ := orderMap[sections[j].Cat]
		if okI && okJ {
			return idxI < idxJ
		}
		if okI {
			return true
		}
		if okJ {
			return false
		}
		return strings.Compare(sections[i].Cat, sections[j].Cat) < 0
	})

	var sb strings.Builder
	sb.WriteString("## Command reference\n\n")
	sb.WriteString("Command specifications are grouped under [`commands/`](commands), registered through [`commands/all.go`](commands/all.go), and resolved by the [`spec/`](spec) completion engine.\n\n")
	fmt.Fprintf(&sb, "Currently, Vuja natively supports **%d** top-level CLI commands across **%d** categories:\n\n", totalCommands, len(sections))

	for _, sec := range sections {
		fmt.Fprintf(&sb, "- [%s (`%s/`)](#%s): **%d** commands\n", sec.Title, sec.Cat, sec.Cat, len(sec.Specs))
	}
	sb.WriteString("\n")

	for _, sec := range sections {
		fmt.Fprintf(&sb, "<a id=\"%s\"></a>\n", sec.Cat)
		fmt.Fprintf(&sb, "### %s (`%s/`)\n\n", sec.Title, sec.Cat)
		sb.WriteString("| Command | Description | Source File |\n")
		sb.WriteString("| :--- | :--- | :--- |\n")
		for _, spec := range sec.Specs {
			fmt.Fprintf(&sb, "| **`%s`** | %s | [`%s`](commands/%s/%s) |\n", spec.Name, spec.Description, spec.File, sec.Cat, spec.File)
		}
		sb.WriteString("\n")
	}

	const beginMarker = "<!-- BEGIN GENERATED COMMANDS -->"
	const endMarker = "<!-- END GENERATED COMMANDS -->"
	readmeFile := filepath.Clean(filepath.Join(commandsDir, "..", "README.md"))
	readme, readErr := os.ReadFile(readmeFile)
	if readErr != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", readmeFile, readErr)
		os.Exit(1)
	}
	content := string(readme)
	begin := strings.Index(content, beginMarker)
	end := strings.Index(content, endMarker)
	if begin == -1 || end == -1 || end < begin {
		fmt.Fprintf(os.Stderr, "Error: %s must contain generated command markers\n", readmeFile)
		os.Exit(1)
	}
	generated := beginMarker + "\n" + sb.String() + endMarker
	content = content[:begin] + generated + content[end+len(endMarker):]
	if writeErr := os.WriteFile(readmeFile, []byte(content), 0644); writeErr != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", readmeFile, writeErr)
		os.Exit(1)
	}

	fmt.Printf("Updated %s with %d commands across %d categories.\n", readmeFile, totalCommands, len(sections))
}

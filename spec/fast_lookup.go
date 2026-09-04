package spec

import "strings"

// LookupFast returns only in-memory aliases and static command metadata. It
// deliberately skips PATH scans, generators, Cobra completion, and shell
// configuration reads so it can be used for the first suggestion paint.
func LookupFast(input string) []Suggestion {
	aliases := GetAliasesCopy()
	tokens := Tokenize(input)
	if len(tokens) == 0 {
		return nil
	}
	if len(tokens) == 1 {
		query := tokens[0]
		results := make([]Suggestion, 0)
		for name, target := range aliases {
			if query == "" || HasPrefix(name, query) {
				results = append(results, Suggestion{Cmd: name, Desc: target, Icon: "alias", Source: "alias"})
			}
		}
		for name, command := range Registry {
			if query != "" && !HasPrefix(name, query) && !matchesAnyAlias(command.Aliases, query) {
				continue
			}
			icon := command.Icon
			if icon == "" {
				icon = name
			}
			results = append(results, Suggestion{Cmd: name, Desc: command.Description, Icon: icon, Source: "spec"})
		}
		return results
	}

	rootName := tokens[0]
	lookupName := rootName
	if target, ok := aliases[rootName]; ok {
		if expanded := Tokenize(target); len(expanded) > 0 {
			lookupName = expanded[0]
		}
	}
	command, ok := Registry[lookupName]
	if !ok {
		return nil
	}
	subcommands, options := command.Subcommands, command.Options
	for _, token := range tokens[1 : len(tokens)-1] {
		if token == "" || strings.HasPrefix(token, "-") {
			continue
		}
		matched := false
		for _, subcommand := range subcommands {
			if subcommand.Name == token || matchesAnyAlias(subcommand.Aliases, token) {
				subcommands, options = subcommand.Subcommands, subcommand.Options
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
	}

	partial := tokens[len(tokens)-1]
	prefix := strings.TrimSpace(strings.Join(tokens[:len(tokens)-1], " "))
	results := make([]Suggestion, 0, len(subcommands)+len(options))
	for _, subcommand := range subcommands {
		if partial != "" && !HasPrefix(subcommand.Name, partial) && !matchesAnyAlias(subcommand.Aliases, partial) {
			continue
		}
		results = append(results, Suggestion{
			Cmd: prefix + " " + subcommand.Name, Desc: subcommand.Description,
			Icon: subcommand.Icon, Source: "spec", Priority: subcommand.Priority,
		})
	}
	for _, option := range options {
		if partial == "" || HasPrefix(option.Name, partial) {
			results = append(results, Suggestion{
				Cmd: prefix + " " + option.Name, Desc: option.Description,
				Icon: lookupName, Source: "spec", Priority: option.Priority,
			})
		}
	}
	return results
}

func matchesAnyAlias(aliases []string, query string) bool {
	for _, alias := range aliases {
		if HasPrefix(alias, query) {
			return true
		}
	}
	return false
}

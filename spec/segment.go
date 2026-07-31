package spec

import "strings"

type CommandSegment struct {
	Prefix string
	Query  string
}

// ActiveCommandSegment isolates the command currently being edited after a
// shell control operator, while retaining sudo and environment assignments.
func ActiveCommandSegment(input string) CommandSegment {
	start := 0
	var quote rune
	escaped := false
	runes := []rune(input)
	for i, r := range runes {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		switch r {
		case ';', '|':
			start = i + 1
			if r == '|' && start < len(runes) && runes[start] == '|' {
				start++
			}
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				start = i + 2
			}
		}
	}

	segment := string(runes[start:])
	leading := len(segment) - len(strings.TrimLeft(segment, " \t"))
	prefix := string(runes[:start]) + segment[:leading]
	query := segment[leading:]

	for {
		fields := strings.Fields(query)
		if len(fields) == 0 {
			break
		}
		token := fields[0]
		isAssignment := strings.Contains(token, "=") && !strings.HasPrefix(token, "=")
		if token != "sudo" && token != "command" && token != "env" && !isAssignment {
			break
		}
		offset := strings.Index(query, token) + len(token)
		for offset < len(query) && (query[offset] == ' ' || query[offset] == '\t') {
			offset++
		}
		prefix += query[:offset]
		query = query[offset:]
	}
	return CommandSegment{Prefix: prefix, Query: query}
}

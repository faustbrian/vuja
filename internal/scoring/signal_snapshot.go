package scoring

import (
	"context"
	"sort"
	"strings"
)

// SignalSnapshot is one consistent read of every scoring table used for a
// suggestion generation.
type SignalSnapshot struct {
	Local                 []FrecencyEntry
	Project               []FrecencyEntry
	Global                []FrecencyEntry
	Transitions           []TransitionEntry
	TransitionsLocal      bool
	ExactTransitions      []ExactTransitionEntry
	ExactTransitionsLocal bool
	Feedback              []FeedbackEntry
	Outcomes              []OutcomeEntry
}

func (f *FrecencyStore) QuerySignalSnapshot(
	ctx context.Context,
	cwd string,
	root string,
	prefix string,
	limit int,
	previousCommand string,
	previousSkeleton string,
) (SignalSnapshot, error) {
	if f == nil {
		return SignalSnapshot{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = 50
	}
	prefixPattern := strings.TrimSpace(prefix) + "%"
	rows, err := f.db.QueryContext(ctx, `
WITH history_candidates AS (
	SELECT cmd, cwd, count, last_used, 'vuja' AS source FROM history_entries
	UNION ALL
	SELECT cmd, cwd, count, last_used, source FROM imported_history_entries
), signal_rows AS (
	SELECT 'local' AS kind, cmd AS text1, cwd AS text2, source AS text3,
		SUM(count) AS n1, 0 AS n2, 0 AS n3, 0 AS n4, MAX(last_used) AS last_used
	FROM history_candidates
	WHERE cwd = ? AND cmd LIKE ?
	GROUP BY cmd, cwd, source
	UNION ALL
	SELECT 'project', cmd, ?, source, SUM(count), 0, 0, 0, MAX(last_used)
	FROM history_candidates
	WHERE ? != '' AND (cwd = ? OR substr(cwd, 1, length(?) + 1) = ? || '/') AND cmd LIKE ?
	GROUP BY cmd, source
	UNION ALL
	SELECT 'global', cmd, '', source, SUM(count), 0, 0, 0, MAX(last_used)
	FROM history_candidates
	WHERE cmd LIKE ?
	GROUP BY cmd, source
	UNION ALL
	SELECT 'feedback', cmd, '', '', SUM(accepted), SUM(typed), SUM(edited), SUM(dismissed), MAX(last_used)
	FROM suggestion_feedback
	WHERE cmd LIKE ? AND (cwd = ? OR (? != '' AND (cwd = ? OR cwd LIKE ?)))
	GROUP BY cmd
	UNION ALL
	SELECT 'outcome', cmd, '', '', SUM(successes), SUM(failures), 0, 0, MAX(last_used)
	FROM command_outcomes
	WHERE cmd LIKE ? AND (cwd = ? OR (? != '' AND (cwd = ? OR cwd LIKE ?)))
	GROUP BY cmd
	UNION ALL
	SELECT 'transition-local', prev_skeleton, next_skeleton, cwd, SUM(count), 0, 0, 0, MAX(last_used)
	FROM command_transitions
	WHERE ? != '' AND cwd = ? AND count > 0 AND (? = prev_skeleton OR ? LIKE prev_skeleton || ' %')
	GROUP BY prev_skeleton, next_skeleton, cwd
	UNION ALL
	SELECT 'transition-global', prev_skeleton, next_skeleton, '', SUM(count), 0, 0, 0, MAX(last_used)
	FROM command_transitions
	WHERE ? != '' AND count > 0 AND (? = prev_skeleton OR ? LIKE prev_skeleton || ' %')
	GROUP BY prev_skeleton, next_skeleton
	UNION ALL
	SELECT 'exact-local', prev_command, next_command, cwd, SUM(count), 0, 0, 0, MAX(last_used)
	FROM exact_command_transitions
	WHERE ? != '' AND prev_command = ? AND cwd = ? AND count > 0
	GROUP BY prev_command, next_command, cwd
	UNION ALL
	SELECT 'exact-global', prev_command, next_command, '', SUM(count), 0, 0, 0, MAX(last_used)
	FROM exact_command_transitions
	WHERE ? != '' AND prev_command = ? AND count > 0
	GROUP BY prev_command, next_command
)
SELECT kind, text1, text2, text3, n1, n2, n3, n4, last_used
FROM signal_rows
ORDER BY last_used DESC, text1 ASC, text2 ASC
`,
		cwd, prefixPattern,
		root, root, root, root, root, prefixPattern,
		prefixPattern,
		prefixPattern, cwd, root, root, strings.TrimSuffix(root, "/")+"/%",
		prefixPattern, cwd, root, root, strings.TrimSuffix(root, "/")+"/%",
		previousSkeleton, cwd, previousSkeleton, previousSkeleton,
		previousSkeleton, previousSkeleton, previousSkeleton,
		previousCommand, previousCommand, cwd,
		previousCommand, previousCommand,
	)
	if err != nil {
		return SignalSnapshot{}, err
	}
	defer rows.Close()

	var snapshot SignalSnapshot
	var transitionLocal, transitionGlobal []TransitionEntry
	var exactLocal, exactGlobal []ExactTransitionEntry
	for rows.Next() {
		var kind, text1, text2, text3, lastUsedRaw string
		var n1, n2, n3, n4 int
		if err := rows.Scan(&kind, &text1, &text2, &text3, &n1, &n2, &n3, &n4, &lastUsedRaw); err != nil {
			return SignalSnapshot{}, err
		}
		lastUsed := parseKnownTimestamp(lastUsedRaw)
		switch kind {
		case "local", "project", "global":
			entry := FrecencyEntry{Cmd: text1, Cwd: text2, Count: n1, LastUsed: lastUsed, RawScore: f.RawScore(n1, lastUsed)}
			switch kind {
			case "local":
				snapshot.Local = append(snapshot.Local, entry)
			case "project":
				snapshot.Project = append(snapshot.Project, entry)
			case "global":
				snapshot.Global = append(snapshot.Global, entry)
			}
		case "feedback":
			snapshot.Feedback = append(snapshot.Feedback, FeedbackEntry{Cmd: text1, Accepted: n1, Typed: n2, Edited: n3, Dismissed: n4})
		case "outcome":
			snapshot.Outcomes = append(snapshot.Outcomes, OutcomeEntry{Cmd: text1, Successes: n1, Failures: n2})
		case "transition-local", "transition-global":
			entry := TransitionEntry{PrevSkeleton: text1, NextSkeleton: text2, Cwd: text3, Count: n1, LastUsed: lastUsed}
			if kind == "transition-local" {
				transitionLocal = append(transitionLocal, entry)
			} else {
				transitionGlobal = append(transitionGlobal, entry)
			}
		case "exact-local", "exact-global":
			entry := ExactTransitionEntry{PrevCommand: text1, NextCommand: text2, Cwd: text3, Count: n1, LastUsed: lastUsed}
			if kind == "exact-local" {
				exactLocal = append(exactLocal, entry)
			} else {
				exactGlobal = append(exactGlobal, entry)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return SignalSnapshot{}, err
	}

	trimFrecency := func(entries []FrecencyEntry) []FrecencyEntry {
		sort.SliceStable(entries, func(i, j int) bool { return frecencyEntryLess(entries[i], entries[j]) })
		if len(entries) > limit {
			entries = entries[:limit]
		}
		return entries
	}
	snapshot.Local = trimFrecency(snapshot.Local)
	snapshot.Project = trimFrecency(snapshot.Project)
	snapshot.Global = trimFrecency(snapshot.Global)
	if len(snapshot.Feedback) > 50 {
		snapshot.Feedback = snapshot.Feedback[:50]
	}
	if len(snapshot.Outcomes) > 50 {
		snapshot.Outcomes = snapshot.Outcomes[:50]
	}
	snapshot.Transitions, snapshot.TransitionsLocal = selectTransitionDepth(transitionLocal, transitionGlobal)
	if len(exactLocal) > 0 {
		snapshot.ExactTransitions, snapshot.ExactTransitionsLocal = exactLocal, true
	} else {
		snapshot.ExactTransitions = exactGlobal
	}
	return snapshot, nil
}

func selectTransitionDepth(local, global []TransitionEntry) ([]TransitionEntry, bool) {
	selectDeepest := func(entries []TransitionEntry) []TransitionEntry {
		deepest := 0
		for _, entry := range entries {
			deepest = max(deepest, len(entry.PrevSkeleton))
		}
		selected := entries[:0]
		for _, entry := range entries {
			if len(entry.PrevSkeleton) == deepest {
				selected = append(selected, entry)
			}
		}
		sort.SliceStable(selected, func(i, j int) bool {
			if selected[i].Count != selected[j].Count {
				return selected[i].Count > selected[j].Count
			}
			return selected[i].NextSkeleton < selected[j].NextSkeleton
		})
		return selected
	}
	if len(local) > 0 {
		return selectDeepest(local), true
	}
	return selectDeepest(global), false
}

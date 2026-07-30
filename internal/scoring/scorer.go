package scoring

import (
	"math"
	"sort"
	"strings"

	"github.com/faustbrian/vuja/spec"
)

type ScoreBreakdown struct {
	BasePriority      int `json:"base_priority"`
	ContextBonus      int `json:"context_bonus"`
	Frecency          int `json:"frecency"`
	Transition        int `json:"transition"`
	MatchQuality      int `json:"match_quality"`
	DirectoryAffinity int `json:"directory_affinity"`
	Feedback          int `json:"feedback"`
	Outcome           int `json:"outcome"`
}

type ScoredSuggestion struct {
	spec.Suggestion
	Score     float64
	Breakdown ScoreBreakdown
}

type ScoreConfig struct {
	WeightBasePriority      float64
	WeightContextBonus      float64
	WeightFrecency          float64
	WeightTransition        float64
	WeightMatchQuality      float64
	WeightDirectoryAffinity float64
	WeightFeedback          float64
	WeightOutcome           float64
}

var DefaultScoreConfig = ScoreConfig{
	WeightBasePriority:      0.05,
	WeightContextBonus:      0.15,
	WeightFrecency:          0.15,
	WeightTransition:        0.15,
	WeightMatchQuality:      0.25,
	WeightDirectoryAffinity: 0.10,
	WeightFeedback:          0.10,
	WeightOutcome:           0.05,
}

func Score(suggestions []spec.Suggestion, signals SignalSet) []ScoredSuggestion {
	return ScoreWithConfig(suggestions, signals, DefaultScoreConfig)
}

func ScoreWithConfig(suggestions []spec.Suggestion, signals SignalSet, config ScoreConfig) []ScoredSuggestion {
	if len(suggestions) == 0 {
		return nil
	}

	localMap := make(map[string]float64, len(signals.LocalFrecency))
	for _, e := range signals.LocalFrecency {
		localMap[e.Cmd] = e.RawScore
	}
	projectMap := make(map[string]float64, len(signals.ProjectFrecency))
	for _, e := range signals.ProjectFrecency {
		projectMap[e.Cmd] = e.RawScore
	}
	globalMap := make(map[string]float64, len(signals.GlobalFrecency))
	for _, e := range signals.GlobalFrecency {
		globalMap[e.Cmd] = e.RawScore
	}

	rawFrec := make([]float64, len(suggestions))
	for i, s := range suggestions {
		if score, ok := localMap[s.Cmd]; ok {
			rawFrec[i] = score
		} else if score, ok := projectMap[s.Cmd]; ok {
			rawFrec[i] = score * 0.85
		} else if score, ok := globalMap[s.Cmd]; ok {
			rawFrec[i] = score * 0.7
		} else {
			rawFrec[i] = 0
		}
	}

	normFrec := normalizeFrecency(rawFrec)

	scored := make([]ScoredSuggestion, len(suggestions))
	for i, s := range suggestions {
		bp := basePriorityFor(s)
		cb := ApplyContextRules(signals.Workspace, s.Cmd)
		frec := normFrec[i]
		trans := max(
			transitionScoreFor(ExtractSkeleton(s.Cmd), signals.TransitionEntries, signals.TransitionIsLocal),
			exactTransitionScoreFor(s.Cmd, signals.ExactTransitionEntries, signals.ExactTransitionIsLocal),
		)
		mq := matchQualityScore(s.Cmd, signals.Query)
		directoryAffinity := directoryAffinityFor(s.Cmd, localMap, projectMap)
		feedback := feedbackScoreFor(s.Cmd, signals.Feedback)
		outcome := outcomeScoreFor(s.Cmd, signals.Outcomes)

		total := config.WeightBasePriority*float64(bp) +
			config.WeightContextBonus*float64(cb) +
			config.WeightFrecency*float64(frec) +
			config.WeightTransition*float64(trans) +
			config.WeightMatchQuality*float64(mq) +
			config.WeightDirectoryAffinity*float64(directoryAffinity) +
			config.WeightFeedback*float64(feedback) +
			config.WeightOutcome*float64(outcome)

		scored[i] = ScoredSuggestion{
			Suggestion: s,
			Score:      total,
			Breakdown: ScoreBreakdown{
				BasePriority:      bp,
				ContextBonus:      cb,
				Frecency:          frec,
				Transition:        trans,
				MatchQuality:      mq,
				DirectoryAffinity: directoryAffinity,
				Feedback:          feedback,
				Outcome:           outcome,
			},
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		iFilesystemMatch := isLiveFilesystemMatch(scored[i].Suggestion, signals.Query)
		jFilesystemMatch := isLiveFilesystemMatch(scored[j].Suggestion, signals.Query)
		if iFilesystemMatch != jFilesystemMatch {
			return iFilesystemMatch
		}
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Breakdown.Transition != scored[j].Breakdown.Transition {
			return scored[i].Breakdown.Transition > scored[j].Breakdown.Transition
		}
		if scored[i].Breakdown.DirectoryAffinity != scored[j].Breakdown.DirectoryAffinity {
			return scored[i].Breakdown.DirectoryAffinity > scored[j].Breakdown.DirectoryAffinity
		}
		if scored[i].Breakdown.Frecency != scored[j].Breakdown.Frecency {
			return scored[i].Breakdown.Frecency > scored[j].Breakdown.Frecency
		}
		if scored[i].Breakdown.ContextBonus != scored[j].Breakdown.ContextBonus {
			return scored[i].Breakdown.ContextBonus > scored[j].Breakdown.ContextBonus
		}
		return scored[i].Cmd < scored[j].Cmd
	})

	return scored
}

func outcomeScoreFor(command string, entries []OutcomeEntry) int {
	for _, entry := range entries {
		if entry.Cmd != command {
			continue
		}
		total := entry.Successes + entry.Failures
		if total == 0 {
			return 0
		}
		return (entry.Successes - entry.Failures) * 100 / total
	}
	return 0
}

func feedbackScoreFor(command string, entries []FeedbackEntry) int {
	for _, entry := range entries {
		if entry.Cmd != command {
			continue
		}
		total := entry.Accepted + entry.Typed + entry.Edited + entry.Dismissed
		if total == 0 {
			return 0
		}
		score := (entry.Accepted*4 + entry.Typed*2 - entry.Edited*2 - entry.Dismissed*3) * 100 / (4 * total)
		return max(-100, min(100, score))
	}
	return 0
}

func isLiveFilesystemMatch(suggestion spec.Suggestion, query string) bool {
	if suggestion.Source != "filesystem" {
		return false
	}
	query = strings.TrimSpace(query)
	return query != "" && strings.HasPrefix(strings.ToLower(suggestion.Cmd), strings.ToLower(query))
}

func exactTransitionScoreFor(command string, entries []ExactTransitionEntry, isLocal bool) int {
	if len(entries) == 0 {
		return 0
	}
	maxCount := entries[0].Count
	if maxCount <= 0 {
		return 0
	}
	for _, entry := range entries {
		if entry.NextCommand == command {
			score := float64(entry.Count) / float64(maxCount) * 100
			if !isLocal {
				score *= 0.7
			}
			return int(math.Round(score))
		}
	}
	return 0
}

func directoryAffinityFor(command string, local, project map[string]float64) int {
	if local[command] > 0 {
		return 100
	}
	if project[command] > 0 {
		return 70
	}
	return 0
}

func transitionScoreFor(cmdSkeleton string, entries []TransitionEntry, isLocal bool) int {
	if len(entries) == 0 {
		return 0 // cold-start: no data, contributes 0 (must check before accessing entries[0])
	}
	maxCount := entries[0].Count
	if maxCount <= 0 {
		return 0
	}
	for _, e := range entries {
		if e.NextSkeleton == cmdSkeleton {
			score := (float64(e.Count) / float64(maxCount)) * 100.0
			if !isLocal {
				score *= 0.7
			}
			return int(math.Round(score))
		}
	}
	return 0
}

func basePriorityFor(s spec.Suggestion) int {
	if s.Priority > 0 {
		if s.Priority > 100 {
			return 100
		}
		return s.Priority
	}

	switch s.Source {
	case "spec":
		return 60
	case "ai":
		if s.Confidence > 0 {
			if s.Confidence > 100 {
				return 100
			}
			return s.Confidence
		}
		return 50
	case "history":
		if s.Confidence > 0 {
			if s.Confidence > 100 {
				return 100
			}
			return s.Confidence
		}
		return 40
	default:
		return 50
	}
}

func matchQualityScore(cmd, query string) int {
	cmd = strings.TrimSpace(cmd)
	query = strings.TrimSpace(query)
	if query == "" {
		return 100
	}
	if cmd == query {
		return 100
	}
	if strings.HasPrefix(cmd, query) {
		return 100
	}
	if strings.HasPrefix(strings.ToLower(cmd), strings.ToLower(query)) {
		return 80
	}
	if strings.Contains(strings.ToLower(cmd), strings.ToLower(query)) {
		return 50
	}
	if isSubsequence(strings.ToLower(query), strings.ToLower(cmd)) {
		return 30
	}
	return 0
}

func isSubsequence(sub, full string) bool {
	subRunes := []rune(sub)
	fullRunes := []rune(full)
	if len(subRunes) == 0 {
		return true
	}
	i := 0
	for j := 0; j < len(fullRunes) && i < len(subRunes); j++ {
		if subRunes[i] == fullRunes[j] {
			i++
		}
	}
	return i == len(subRunes)
}

func normalizeFrecency(raw []float64) []int {
	if len(raw) == 0 {
		return nil
	}
	maxRaw := 0.0
	for _, r := range raw {
		if r > maxRaw {
			maxRaw = r
		}
	}
	if maxRaw <= 0 {
		res := make([]int, len(raw))
		return res
	}

	res := make([]int, len(raw))
	for i, r := range raw {
		val := int(math.Round((r / maxRaw) * 100.0))
		if val > 100 {
			val = 100
		} else if val < 0 {
			val = 0
		}
		res[i] = val
	}
	return res
}

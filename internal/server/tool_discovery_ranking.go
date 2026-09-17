package server

import (
	"sort"
	"strings"

	"synon-go/internal/discoveryquery"
)

type toolDiscoveryCandidate struct {
	Name     string
	Haystack string
}

// rankToolDiscoveryCandidates is the single relevance authority for native,
// dynamic MCP, and admitted agent-runtime tool discovery. Rare capability
// terms receive more weight than catalog-wide words such as search or source,
// so a focused provider is not crowded out by many generic tools from one
// domain. Authorization is applied before candidates reach this function.
func rankToolDiscoveryCandidates(query string, candidates []toolDiscoveryCandidate, maxResults int) []string {
	if maxResults <= 0 {
		return []string{}
	}
	if selected, ok := strings.CutPrefix(strings.ToLower(query), "select:"); ok {
		requested := strings.Split(selected, ",")
		matches := make([]string, 0, minInt(maxResults, len(requested)))
		for _, raw := range requested {
			wanted := strings.TrimSpace(raw)
			if wanted == "" {
				continue
			}
			for _, candidate := range candidates {
				if strings.EqualFold(candidate.Name, wanted) && !stringSliceContains(matches, candidate.Name) {
					matches = append(matches, candidate.Name)
					break
				}
			}
			if len(matches) >= maxResults {
				break
			}
		}
		return matches
	}

	terms := discoveryquery.Terms(query)
	if len(terms) == 0 {
		return []string{}
	}
	type preparedCandidate struct {
		Name      string
		lowerName string
		haystack  string
	}
	prepared := make([]preparedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			continue
		}
		lowerName := strings.ToLower(name)
		prepared = append(prepared, preparedCandidate{
			Name:      name,
			lowerName: lowerName,
			haystack:  strings.ToLower(strings.Join([]string{name, splitToolName(name), candidate.Haystack}, " ")),
		})
	}
	frequencies := make(map[string]int, len(terms))
	for _, term := range terms {
		for _, candidate := range prepared {
			if strings.Contains(candidate.haystack, term) {
				frequencies[term]++
			}
		}
	}
	type scoredCandidate struct {
		Name  string
		Score int
	}
	scored := make([]scoredCandidate, 0, len(prepared))
	for _, candidate := range prepared {
		score := 0
		for _, term := range terms {
			frequency := frequencies[term]
			if frequency == 0 {
				continue
			}
			boost := discoveryRarityBoost(len(prepared), frequency)
			switch {
			case candidate.lowerName == term:
				score += 100 * boost
			case strings.Contains(candidate.lowerName, term):
				score += 20 * boost
			case strings.Contains(candidate.haystack, term):
				score += 5 * boost
			}
		}
		if score > 0 {
			scored = append(scored, scoredCandidate{Name: candidate.Name, Score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Name < scored[j].Name
		}
		return scored[i].Score > scored[j].Score
	})
	matches := make([]string, 0, minInt(maxResults, len(scored)))
	for _, item := range scored {
		if len(matches) >= maxResults {
			break
		}
		matches = append(matches, item.Name)
	}
	return matches
}

func discoveryRarityBoost(candidateCount, frequency int) int {
	switch {
	case candidateCount <= 0 || frequency <= 0:
		return 1
	case frequency == 1:
		return 5
	case frequency*4 <= candidateCount:
		return 3
	case frequency*2 <= candidateCount:
		return 2
	default:
		return 1
	}
}

package server

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

type runnerCrossArtifactRankPair struct {
	score string
	rank  string
}

var runnerCrossArtifactScenarioRankPairs = []runnerCrossArtifactRankPair{
	{score: "score_pessimistic", rank: "rank_pessimistic"},
	{score: "score_neutral", rank: "rank_neutral"},
	{score: "score_optimistic", rank: "rank_optimistic"},
}

// runnerCrossArtifactRankFailures makes scenario rankings independently
// auditable. A rank without its underlying score cannot prove that rows were
// mapped back from a sorted index correctly, which is a common numerical error.
func runnerCrossArtifactRankFailures(table runnerCrossArtifactTable) []string {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(table.source)))
	if extension != ".csv" && extension != ".tsv" {
		return nil
	}
	headers := make(map[string]int, len(table.headers))
	for index, header := range table.headers {
		headers[canonicalRunnerTableHeader(header)] = index
	}

	failures := make([]string, 0)
	for _, pair := range runnerCrossArtifactScenarioRankPairs {
		rankIndex, hasRank := headers[pair.rank]
		if !hasRank {
			continue
		}
		scoreIndex, hasScore := headers[pair.score]
		if !hasScore {
			failures = append(failures, fmt.Sprintf(
				"rank_score_pair_missing:%s rank=%s score=%s", table.source, pair.rank, pair.score,
			))
			continue
		}
		failures = append(failures, runnerCrossArtifactRankPairFailures(table, scoreIndex, rankIndex, pair)...)
	}
	return failures
}

func runnerCrossArtifactRankPairFailures(
	table runnerCrossArtifactTable,
	scoreIndex int,
	rankIndex int,
	pair runnerCrossArtifactRankPair,
) []string {
	type rowValue struct {
		key   string
		score float64
		rank  float64
	}
	values := make([]rowValue, 0, len(table.rows))
	failures := make([]string, 0)
	for _, row := range table.rows {
		if len(row) <= scoreIndex || len(row) <= rankIndex || len(row) == 0 {
			continue
		}
		score, _, scoreOK := parseRunnerComparableNumber(row[scoreIndex])
		rank, _, rankOK := parseRunnerComparableNumber(row[rankIndex])
		if !scoreOK || !rankOK {
			failures = append(failures, fmt.Sprintf(
				"rank_score_non_numeric:%s key=%s columns=%s/%s", table.source,
				strings.TrimSpace(row[0]), pair.score, pair.rank,
			))
			continue
		}
		values = append(values, rowValue{key: strings.TrimSpace(row[0]), score: score, rank: rank})
	}
	for _, value := range values {
		expectedRank := 1
		for _, candidate := range values {
			if candidate.score > value.score+1e-12 {
				expectedRank++
			}
		}
		if math.Abs(value.rank-float64(expectedRank)) <= 1e-9 {
			continue
		}
		failures = append(failures, fmt.Sprintf(
			"rank_score_mismatch:%s key=%s columns=%s/%s score=%g rank=%g expected_rank=%d",
			table.source, value.key, pair.score, pair.rank, value.score, value.rank, expectedRank,
		))
	}
	return failures
}

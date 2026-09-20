package server

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

func inspectRunnerCrossArtifactRanks(store *runnerArtifactScanStore, table runnerCrossArtifactTable) ([]string, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(table.source)))
	if extension != ".csv" && extension != ".tsv" {
		return nil, nil
	}
	headers := make(map[string]int, len(table.headers))
	for index, header := range table.headers {
		headers[canonicalRunnerTableHeader(header)] = index
	}
	var failures []string
	for _, pair := range runnerCrossArtifactScenarioRankPairs {
		rankIndex, hasRank := headers[pair.rank]
		if !hasRank {
			continue
		}
		scoreIndex, hasScore := headers[pair.score]
		if !hasScore {
			failures = append(failures, fmt.Sprintf("rank_score_pair_missing:%s rank=%s score=%s", table.source, pair.rank, pair.score))
			continue
		}
		found, err := inspectRunnerCrossArtifactRankPair(store, table, scoreIndex, rankIndex, pair)
		if err != nil {
			return nil, err
		}
		failures = append(failures, found...)
	}
	return failures, nil
}

func inspectRunnerCrossArtifactRankPair(store *runnerArtifactScanStore, table runnerCrossArtifactTable, scoreIndex, rankIndex int, pair runnerCrossArtifactRankPair) ([]string, error) {
	scope := store.scope()
	var failures []string
	err := table.eachRow(store.ctx, func(ordinal int, row []string) error {
		if len(row) == 0 || len(row) <= scoreIndex || len(row) <= rankIndex {
			return nil
		}
		score, _, scoreOK := parseRunnerComparableNumber(row[scoreIndex])
		rank, _, rankOK := parseRunnerComparableNumber(row[rankIndex])
		if !scoreOK || !rankOK {
			failures = append(failures, fmt.Sprintf("rank_score_non_numeric:%s key=%s columns=%s/%s", table.source, strings.TrimSpace(row[0]), pair.score, pair.rank))
			return nil
		}
		_, err := store.exec(`INSERT INTO rank_values(scope,ordinal,key,score,rank) VALUES(?,?,?,?,?)`, scope, ordinal, strings.TrimSpace(row[0]), score, rank)
		return err
	})
	if err != nil {
		return nil, err
	}
	// Distinct score groups and cumulative counts preserve strict epsilon ties.
	// Indexed threshold lookup replaces the former quadratic all-row recount.
	_, err = store.exec(`INSERT INTO rank_scores(scope,score,through_count)
		SELECT ?,score,SUM(COUNT(*)) OVER (ORDER BY score DESC ROWS UNBOUNDED PRECEDING)
		FROM rank_values WHERE scope=? GROUP BY score`, scope, scope)
	if err != nil {
		return nil, err
	}
	rows, err := store.tx.QueryContext(store.ctx, `SELECT key,score,rank FROM rank_values WHERE scope=? ORDER BY ordinal`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var score, rank float64
		if err := rows.Scan(&key, &score, &rank); err != nil {
			return nil, err
		}
		var better int64
		err := store.tx.QueryRowContext(store.ctx, `SELECT through_count FROM rank_scores WHERE scope=? AND score>? ORDER BY score ASC LIMIT 1`, scope, score+1e-12).Scan(&better)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		expected := better + 1
		if math.Abs(rank-float64(expected)) <= 1e-9 {
			continue
		}
		failures = append(failures, fmt.Sprintf("rank_score_mismatch:%s key=%s columns=%s/%s score=%g rank=%g expected_rank=%d", table.source, key, pair.score, pair.rank, score, rank, expected))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, statement := range []string{`DELETE FROM rank_values WHERE scope=?`, `DELETE FROM rank_scores WHERE scope=?`} {
		if _, err := store.exec(statement, scope); err != nil {
			return nil, err
		}
	}
	return failures, nil
}

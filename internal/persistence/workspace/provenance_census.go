package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

const defaultProvenanceDetectionWindowDays = 7

type ProvenanceCensusOptions struct {
	DetectionWindowDays float64
	SplitAt             *time.Time
}

type ProvenanceCensusClass struct {
	Class               string `json:"class"`
	N                   int64  `json:"n"`
	WithProducingCell   int64  `json:"with_producing_cell"`
	WithExtractedCode   int64  `json:"with_extracted_code"`
	WithDependencyEdges int64  `json:"with_dependency_edges"`
	WithRealChecksum    int64  `json:"with_real_checksum"`
}

type ProvenancePendingMappings struct {
	Total      int64            `json:"total"`
	ByAttempts map[string]int64 `json:"by_attempts"`
}

type ProvenanceDetectionCount struct {
	Backend  *string `json:"backend"`
	Fidelity *string `json:"fidelity"`
	N        int64   `json:"n"`
}

type ProvenanceDetectionCensus struct {
	WindowDays float64                    `json:"window_days"`
	Mix        []ProvenanceDetectionCount `json:"mix"`
}

type ProvenanceCensusSplit struct {
	At   string                  `json:"at"`
	Pre  []ProvenanceCensusClass `json:"pre"`
	Post []ProvenanceCensusClass `json:"post"`
}

type ProvenanceCensusResult struct {
	ComputedAt      string                    `json:"computed_at"`
	DurationMS      int64                     `json:"duration_ms"`
	Classes         []ProvenanceCensusClass   `json:"classes"`
	PendingMappings ProvenancePendingMappings `json:"pending_mappings"`
	TornFinalRows   int64                     `json:"torn_final_rows"`
	OrphanEdgeRows  int64                     `json:"orphan_edge_rows"`
	Detection       ProvenanceDetectionCensus `json:"detection"`
	Split           *ProvenanceCensusSplit    `json:"split,omitempty"`
}

// GetProvenanceCensus computes one transactionally consistent view of artifact
// lineage coverage and integrity. The query mirrors the v1.1 census while
// reading the normalized Go provenance tables.
func (s *Store) GetProvenanceCensus(ctx context.Context, options ProvenanceCensusOptions) (ProvenanceCensusResult, error) {
	if s == nil || s.db == nil {
		return ProvenanceCensusResult{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now()
	windowDays := normalizeProvenanceWindowDays(options.DetectionWindowDays)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProvenanceCensusResult{}, fmt.Errorf("begin provenance census: %w", err)
	}
	defer tx.Rollback()

	classes, err := queryProvenanceClasses(ctx, tx, nil, nil)
	if err != nil {
		return ProvenanceCensusResult{}, err
	}
	pending, err := queryPendingProvenanceMappings(ctx, tx)
	if err != nil {
		return ProvenanceCensusResult{}, err
	}
	tornFinalRows, err := queryTornProvenanceRows(ctx, tx)
	if err != nil {
		return ProvenanceCensusResult{}, err
	}
	orphanEdgeRows, err := queryOrphanProvenanceEdges(ctx, tx)
	if err != nil {
		return ProvenanceCensusResult{}, err
	}

	var split *ProvenanceCensusSplit
	if options.SplitAt != nil {
		splitAt := options.SplitAt.UTC()
		pre, err := queryProvenanceClasses(ctx, tx, &splitAt, nil)
		if err != nil {
			return ProvenanceCensusResult{}, err
		}
		post, err := queryProvenanceClasses(ctx, tx, nil, &splitAt)
		if err != nil {
			return ProvenanceCensusResult{}, err
		}
		split = &ProvenanceCensusSplit{
			At: splitAt.Format(time.RFC3339Nano), Pre: pre, Post: post,
		}
	}
	if err := tx.Commit(); err != nil {
		return ProvenanceCensusResult{}, fmt.Errorf("commit provenance census snapshot: %w", err)
	}

	return ProvenanceCensusResult{
		ComputedAt:      s.now().UTC().Format(time.RFC3339Nano),
		DurationMS:      max(time.Since(startedAt).Milliseconds(), 0),
		Classes:         classes,
		PendingMappings: pending,
		TornFinalRows:   tornFinalRows,
		OrphanEdgeRows:  orphanEdgeRows,
		Detection: ProvenanceDetectionCensus{
			WindowDays: windowDays,
			Mix:        make([]ProvenanceDetectionCount, 0),
		},
		Split: split,
	}, nil
}

func normalizeProvenanceWindowDays(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return defaultProvenanceDetectionWindowDays
	}
	return min(value, 365)
}

func queryProvenanceClasses(
	ctx context.Context,
	tx *sql.Tx,
	before *time.Time,
	atOrAfter *time.Time,
) ([]ProvenanceCensusClass, error) {
	where := ""
	args := make([]any, 0, 1)
	if before != nil {
		where = "WHERE c.created_at < ?"
		args = append(args, before.UTC())
	} else if atOrAfter != nil {
		where = "WHERE c.created_at >= ?"
		args = append(args, atOrAfter.UTC())
	}
	rows, err := tx.QueryContext(ctx, `
		WITH classified AS (
			SELECT v.id, v.created_at, v.content_sha256,
				p.producing_cell_id, p.extracted_code,
				CASE
					WHEN COALESCE(m.is_user_upload, 0) != 0 THEN 'upload'
					WHEN v.storage_path LIKE '~/%' THEN 'reference'
					ELSE 'managed'
				END AS class
			FROM artifact_versions v
			LEFT JOIN artifact_runtime_metadata m ON m.artifact_id = v.artifact_id
			LEFT JOIN artifact_version_provenance p ON p.version_id = v.id
		)
		SELECT c.class, COUNT(*),
			SUM(CASE WHEN c.producing_cell_id IS NOT NULL THEN 1 ELSE 0 END),
			SUM(CASE WHEN c.extracted_code IS NOT NULL AND length(c.extracted_code) > 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN EXISTS (
				SELECT 1 FROM artifact_version_dependencies d
				WHERE d.artifact_version_id = c.id
			) THEN 1 ELSE 0 END),
			SUM(CASE WHEN c.content_sha256 IS NOT NULL AND c.content_sha256 != '' THEN 1 ELSE 0 END)
		FROM classified c
		`+where+`
		GROUP BY c.class
		ORDER BY c.class`, args...)
	if err != nil {
		return nil, fmt.Errorf("query provenance classes: %w", err)
	}
	defer rows.Close()
	classes := make([]ProvenanceCensusClass, 0, 3)
	for rows.Next() {
		var class ProvenanceCensusClass
		if err := rows.Scan(
			&class.Class,
			&class.N,
			&class.WithProducingCell,
			&class.WithExtractedCode,
			&class.WithDependencyEdges,
			&class.WithRealChecksum,
		); err != nil {
			return nil, fmt.Errorf("scan provenance class: %w", err)
		}
		classes = append(classes, class)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provenance classes: %w", err)
	}
	return classes, nil
}

func queryPendingProvenanceMappings(ctx context.Context, tx *sql.Tx) (ProvenancePendingMappings, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT CAST(COALESCE(json_extract(
			CASE WHEN json_valid(dependency_mappings) THEN dependency_mappings ELSE '{}' END,
			'$.mapping_attempts'
		), 1) AS TEXT), COUNT(*)
		FROM artifact_version_provenance
		WHERE json_extract(
			CASE WHEN json_valid(dependency_mappings) THEN dependency_mappings ELSE '{}' END,
			'$.mapping_status'
		) = 'pending'
		GROUP BY CAST(COALESCE(json_extract(
			CASE WHEN json_valid(dependency_mappings) THEN dependency_mappings ELSE '{}' END,
			'$.mapping_attempts'
		), 1) AS TEXT)
		ORDER BY 1`)
	if err != nil {
		return ProvenancePendingMappings{}, fmt.Errorf("query pending provenance mappings: %w", err)
	}
	defer rows.Close()
	result := ProvenancePendingMappings{ByAttempts: make(map[string]int64)}
	for rows.Next() {
		var attempts string
		var count int64
		if err := rows.Scan(&attempts, &count); err != nil {
			return ProvenancePendingMappings{}, fmt.Errorf("scan pending provenance mappings: %w", err)
		}
		result.ByAttempts[attempts] = count
		result.Total += count
	}
	if err := rows.Err(); err != nil {
		return ProvenancePendingMappings{}, fmt.Errorf("iterate pending provenance mappings: %w", err)
	}
	return result, nil
}

func queryTornProvenanceRows(ctx context.Context, tx *sql.Tx) (int64, error) {
	return queryProvenanceIntegrityCount(ctx, tx, `
		SELECT COUNT(*)
		FROM artifact_version_provenance p
		WHERE p.dependency_mappings IS NOT NULL
			AND json_valid(p.dependency_mappings)
			AND json_extract(p.dependency_mappings, '$.mapping_status') IS NULL
			AND json_extract(p.dependency_mappings, '$.provenance_inherited_from') IS NULL
			AND EXISTS (
				SELECT 1 FROM json_each(p.dependency_mappings, '$.inputs') j
				WHERE json_extract(j.value, '$.version_id') IS NOT NULL
					AND json_extract(j.value, '$.version_id') != p.version_id
					AND NOT EXISTS (
						SELECT 1 FROM artifact_version_dependencies d
						WHERE d.artifact_version_id = p.version_id
							AND d.depends_on_version_id = json_extract(j.value, '$.version_id')
					)
					AND EXISTS (
						SELECT 1 FROM artifact_versions target
						WHERE target.id = json_extract(j.value, '$.version_id')
					)
					AND NOT EXISTS (
						SELECT 1 FROM json_each(p.dependency_mappings, '$.outputs') output
						WHERE json_extract(output.value, '$.version_id') = json_extract(j.value, '$.version_id')
					)
			)`, "query torn provenance rows")
}

func queryOrphanProvenanceEdges(ctx context.Context, tx *sql.Tx) (int64, error) {
	return queryProvenanceIntegrityCount(ctx, tx, `
		SELECT COUNT(*)
		FROM artifact_version_provenance p
		WHERE p.dependency_mappings IS NOT NULL
			AND json_valid(p.dependency_mappings)
			AND json_extract(p.dependency_mappings, '$.mapping_status') IS NULL
			AND EXISTS (
				SELECT 1 FROM artifact_version_dependencies d
				WHERE d.artifact_version_id = p.version_id
					AND NOT EXISTS (
						SELECT 1 FROM json_each(p.dependency_mappings, '$.inputs') j
						WHERE json_extract(j.value, '$.version_id') = d.depends_on_version_id
					)
			)`, "query orphan provenance edges")
}

func queryProvenanceIntegrityCount(ctx context.Context, tx *sql.Tx, query, operation string) (int64, error) {
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("%s: %w", operation, err)
	}
	return count, nil
}

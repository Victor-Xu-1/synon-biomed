package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptHistoryV27PreflightIdentity = "canonical-transcript-v26-history-classification-empty-v1"
	transcriptHistoryV27RuleSpec          = "synon.workspace.transcript-history-classification.v27"
)

var transcriptHistoryClassificationV27Migration = versionedSchemaMigration{
	version:    27,
	name:       "transcript-history-classification-ledger",
	statements: transcriptstore.HistoryClassificationV27Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptHistoryV27RuleSpec,
		PreflightIdentity: transcriptHistoryV27PreflightIdentity,
	},
}

func preflightTranscriptHistoryClassificationV27(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range transcriptstore.HistoryClassificationV27ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript history classification cohort")
		}
		if count != 0 {
			return errors.New("transcript history classification cohort is polluted")
		}
	}
	digest, err := transcriptSchemaCohortDigestFor(
		ctx, executor, transcriptstore.SchemaV25ObjectNames(), transcriptstore.SchemaV25TableNames(),
	)
	if err != nil || digest != transcriptV25CohortSHA256 {
		return errors.New("transcript v25 cohort identity mismatch")
	}
	if err := validateFrameIncarnationV26Shape(ctx, executor); err != nil {
		return err
	}
	return nil
}

func validateFrameIncarnationV26Shape(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(frames)`)
	if err != nil {
		return errors.New("inspect frame incarnation column")
	}
	found := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return errors.New("scan frame incarnation column")
		}
		if name == "incarnation_id" {
			if !strings.EqualFold(columnType, "TEXT") || notNull != 1 || !defaultValue.Valid || defaultValue.String != "''" {
				_ = rows.Close()
				return errors.New("frame incarnation v26 column identity mismatch")
			}
			found++
		}
	}
	if err := rows.Close(); err != nil {
		return errors.New("close frame incarnation column inspection")
	}
	if err := rows.Err(); err != nil || found != 1 {
		return errors.New("frame incarnation v26 column identity mismatch")
	}
	var statement string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
		WHERE type='index' AND name='frames_incarnation_id_unique' AND tbl_name='frames'`).Scan(&statement); err != nil {
		return errors.New("frame incarnation v26 index identity mismatch")
	}
	want := "create unique index frames_incarnation_id_unique on frames(incarnation_id) where incarnation_id<>''"
	if strings.ToLower(strings.Join(strings.Fields(statement), " ")) != want {
		return errors.New("frame incarnation v26 index identity mismatch")
	}
	indexRows, err := executor.QueryContext(ctx, `PRAGMA index_list(frames)`)
	if err != nil {
		return errors.New("inspect frame incarnation indexes")
	}
	validIndex := false
	for indexRows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := indexRows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = indexRows.Close()
			return errors.New("scan frame incarnation indexes")
		}
		if name == "frames_incarnation_id_unique" && unique == 1 && partial == 1 {
			validIndex = true
		}
	}
	if err := indexRows.Close(); err != nil || indexRows.Err() != nil || !validIndex {
		return errors.New("frame incarnation v26 index identity mismatch")
	}
	columns, err := executor.QueryContext(ctx, `PRAGMA index_info(frames_incarnation_id_unique)`)
	if err != nil {
		return errors.New("inspect frame incarnation index columns")
	}
	columnCount := 0
	for columns.Next() {
		var sequence, cid int
		var name string
		if err := columns.Scan(&sequence, &cid, &name); err != nil || sequence != 0 || name != "incarnation_id" {
			_ = columns.Close()
			return errors.New("frame incarnation v26 index identity mismatch")
		}
		columnCount++
	}
	if err := columns.Close(); err != nil || columns.Err() != nil || columnCount != 1 {
		return errors.New("frame incarnation v26 index identity mismatch")
	}
	return nil
}

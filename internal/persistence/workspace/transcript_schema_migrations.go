package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptSchemaNoopCallbackID = "none"
	transcriptV25CallbackID        = "transcript-branch-backfill-after-statements-v1"

	transcriptV23PreflightIdentity = "empty-transcript-cohort-v1"
	transcriptV23RuleSpec          = "synon.workspace.transcript-authority.v23"
	transcriptV24PreflightIdentity = "canonical-transcript-v23-cohort-v1"
	transcriptV24RuleSpec          = "synon.workspace.transcript-artifact-lineage.v24"
	transcriptV25PreflightIdentity = "canonical-transcript-v24-cohort-v1"
	transcriptV25RuleSpec          = "synon.workspace.transcript-branch-lineage.v25"

	transcriptV23CohortSHA256 = "423869f699443f051f39c8fa12fc857e4c94cc06727f970df7887ec966487fc6"
	transcriptV24CohortSHA256 = "d3835b6a9d2354fbc9d480aa0695e3790db9d5060e593814f96e7c3083d478a4"
	transcriptV25CohortSHA256 = "bbf38086b6a2ab90cb7019c05022b57619bf31fa6010dae4ffc7b23ce2494126"
)

var transcriptV23Migration = versionedSchemaMigration{
	version:    23,
	name:       "canonical-transcript-runner-authority",
	statements: transcriptstore.SchemaV23Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptV23RuleSpec,
		PreflightIdentity: transcriptV23PreflightIdentity,
	},
}

var transcriptV24Migration = versionedSchemaMigration{
	version:    24,
	name:       "canonical-transcript-artifact-lineage",
	statements: transcriptstore.ArtifactV24Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptV24RuleSpec,
		PreflightIdentity: transcriptV24PreflightIdentity,
	},
}

var transcriptV25Migration = versionedSchemaMigration{
	version:    25,
	name:       "canonical-transcript-branch-lineage",
	statements: transcriptstore.BranchV25Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptV25CallbackID,
		RuleSpec:          transcriptV25RuleSpec,
		PreflightIdentity: transcriptV25PreflightIdentity,
	},
}

var errTranscriptSchemaCohortUnknown = errors.New("transcript schema cohort contains an unknown object")

func transcriptSchemaIdentityKnown(identity schemaMigrationIdentityV2) bool {
	return identity.CallbackID == transcriptSchemaNoopCallbackID &&
		(identity.PreflightIdentity == transcriptV23PreflightIdentity && identity.RuleSpec == transcriptV23RuleSpec ||
			identity.PreflightIdentity == transcriptV24PreflightIdentity && identity.RuleSpec == transcriptV24RuleSpec ||
			identity.PreflightIdentity == frameIncarnationV26PreflightIdentity && identity.RuleSpec == frameIncarnationV26RuleSpec ||
			identity.PreflightIdentity == transcriptHistoryV27PreflightIdentity && identity.RuleSpec == transcriptHistoryV27RuleSpec ||
			identity.PreflightIdentity == notificationsV28PreflightIdentity && identity.RuleSpec == notificationsV28RuleSpec ||
			identity.PreflightIdentity == transcriptHistoryBackfillV29PreflightIdentity && identity.RuleSpec == transcriptHistoryBackfillV29RuleSpec ||
			identity.PreflightIdentity == transcriptHistoryCutoverV30PreflightIdentity && identity.RuleSpec == transcriptHistoryCutoverV30RuleSpec ||
			identity.PreflightIdentity == transcriptPayloadGenesisV32PreflightIdentity && identity.RuleSpec == transcriptPayloadGenesisV32RuleSpec ||
			identity.PreflightIdentity == transcriptHistoryOrdinaryV33PreflightIdentity && identity.RuleSpec == transcriptHistoryOrdinaryV33RuleSpec ||
			identity.PreflightIdentity == transcriptHistoryOrdinaryCursorV34PreflightIdentity && identity.RuleSpec == transcriptHistoryOrdinaryCursorV34RuleSpec ||
			identity.PreflightIdentity == transcriptTypedHistoryBootstrapV35PreflightIdentity && identity.RuleSpec == transcriptTypedHistoryBootstrapV35RuleSpec) ||
		identity.CallbackID == transcriptV25CallbackID &&
			identity.PreflightIdentity == transcriptV25PreflightIdentity && identity.RuleSpec == transcriptV25RuleSpec ||
		identity.CallbackID == transcriptHistoryActivationV31CallbackID &&
			identity.PreflightIdentity == transcriptHistoryActivationV31PreflightIdentity &&
			identity.RuleSpec == transcriptHistoryActivationV31RuleSpec
}

func preflightTranscriptV23(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	shape, err := inspectProviderV22Shape(ctx, executor)
	if err != nil {
		return err
	}
	if !shape.canonical {
		return errors.New("provider v22 schema is required before transcript migration")
	}
	objects, err := inspectTranscriptSchemaObjects(ctx, executor)
	if err != nil {
		if errors.Is(err, errTranscriptSchemaCohortUnknown) {
			return errors.New("transcript v23 admission cohort is polluted")
		}
		return err
	}
	if len(objects) != 0 {
		return errors.New("transcript v23 admission cohort is polluted")
	}
	return nil
}

func preflightTranscriptV24(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	digest, err := transcriptSchemaCohortDigest(ctx, executor)
	if err != nil {
		if errors.Is(err, errTranscriptSchemaCohortUnknown) {
			return errors.New("transcript v23 cohort identity mismatch")
		}
		return err
	}
	if digest != transcriptV23CohortSHA256 {
		return errors.New("transcript v23 cohort identity mismatch")
	}
	return nil
}

func preflightTranscriptV25(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	digest, err := transcriptSchemaCohortDigest(ctx, executor)
	if err != nil {
		if errors.Is(err, errTranscriptSchemaCohortUnknown) {
			return errors.New("transcript v24 cohort identity mismatch")
		}
		return err
	}
	if digest != transcriptV24CohortSHA256 {
		return errors.New("transcript v24 cohort identity mismatch")
	}
	return nil
}

func backfillTranscriptBranchesV25(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return errors.New("transcript v25 migration transaction is required")
	}
	rows, err := tx.QueryContext(ctx, `SELECT stream_uid,created_at FROM transcript_streams ORDER BY stream_uid`)
	if err != nil {
		return errors.New("load transcript streams for branch backfill")
	}
	type streamRecord struct {
		uid       string
		createdAt time.Time
	}
	streams := []streamRecord{}
	for rows.Next() {
		var stream streamRecord
		if err := rows.Scan(&stream.uid, &stream.createdAt); err != nil {
			_ = rows.Close()
			return errors.New("scan transcript stream for branch backfill")
		}
		streams = append(streams, stream)
	}
	if err := rows.Close(); err != nil {
		return errors.New("close transcript stream branch backfill")
	}
	if err := rows.Err(); err != nil {
		return errors.New("iterate transcript streams for branch backfill")
	}
	for _, stream := range streams {
		branchID, mutationID, requestDigest := transcriptstore.BaseBranchIdentity(stream.uid)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_branches(
				stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
				client_mutation_id,request_sha256,source_message_id,created_at,updated_at
			) VALUES(?,?,NULL,NULL,0,'base',?,?, '',?,?)`,
			stream.uid, branchID, mutationID, requestDigest, stream.createdAt, stream.createdAt,
		); err != nil {
			return errors.New("insert transcript base branch")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
			VALUES(?,?,1,?)`, stream.uid, branchID, stream.createdAt); err != nil {
			return errors.New("insert transcript branch state")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
			SELECT stream_uid,?,publication_seq,event_id FROM transcript_events
			WHERE stream_uid=? ORDER BY publication_seq`, branchID, stream.uid); err != nil {
			return errors.New("backfill transcript branch events")
		}
	}
	return nil
}

func transcriptSchemaCohortDigest(ctx context.Context, executor schemaMigrationQueryExecutor) (string, error) {
	return transcriptSchemaCohortDigestFor(
		ctx, executor, transcriptstore.SchemaV24ObjectNames(), transcriptstore.SchemaV24TableNames(),
	)
}

func transcriptSchemaCohortDigestFor(
	ctx context.Context,
	executor schemaMigrationQueryExecutor,
	objectNames, tableNames []string,
) (string, error) {
	objects, err := inspectTranscriptSchemaObjectsFor(ctx, executor, objectNames, tableNames)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	writeSchemaIdentityFrame(digest, []byte("synon.workspace.transcript-cohort.v1"))
	for _, object := range objects {
		writeSchemaIdentityFrame(digest, []byte(object.objectType))
		writeSchemaIdentityFrame(digest, []byte(object.name))
		writeSchemaIdentityFrame(digest, []byte(object.tableName))
		writeSchemaIdentityFrame(digest, []byte(object.statement))
	}
	writeSchemaIdentityUint64(digest, uint64(len(objects)))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type transcriptSchemaObject struct {
	objectType string
	name       string
	tableName  string
	statement  string
}

func inspectTranscriptSchemaObjects(ctx context.Context, executor schemaMigrationQueryExecutor) ([]transcriptSchemaObject, error) {
	return inspectTranscriptSchemaObjectsFor(
		ctx, executor, transcriptstore.SchemaV24ObjectNames(), transcriptstore.SchemaV24TableNames(),
	)
}

func inspectTranscriptSchemaObjectsFor(
	ctx context.Context,
	executor schemaMigrationQueryExecutor,
	knownObjectNames, knownTableNames []string,
) ([]transcriptSchemaObject, error) {
	rows, err := executor.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		return nil, errors.New("inspect transcript schema cohort")
	}
	defer rows.Close()
	objectNames := transcriptStringSet(knownObjectNames)
	tableNames := transcriptStringSet(knownTableNames)
	objects := make([]transcriptSchemaObject, 0, len(objectNames))
	for rows.Next() {
		var object transcriptSchemaObject
		if err := rows.Scan(&object.objectType, &object.name, &object.tableName, &object.statement); err != nil {
			return nil, errors.New("scan transcript schema cohort")
		}
		if _, ok := objectNames[object.name]; ok {
			objects = append(objects, object)
			continue
		}
		if _, ok := tableNames[object.tableName]; ok {
			objects = append(objects, object)
			continue
		}
		if isLegacyTranscriptAnnotationObject(object.name, object.tableName) {
			continue
		}
		if strings.HasPrefix(object.name, "transcript_") || strings.HasPrefix(object.tableName, "transcript_") {
			return nil, errTranscriptSchemaCohortUnknown
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("iterate transcript schema cohort")
	}
	return objects, nil
}

func transcriptStringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func isLegacyTranscriptAnnotationObject(name, tableName string) bool {
	return name == "transcript_annotations" || name == "transcript_annotations_root_created_idx" ||
		tableName == "transcript_annotations"
}

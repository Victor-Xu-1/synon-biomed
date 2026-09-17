package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	transcriptPayloadGenesisV32PreflightIdentity = "canonical-transcript-v31-payload-genesis-empty-v1"
	transcriptPayloadGenesisV32RuleSpec          = "synon.workspace.transcript-payload-genesis.v32"
)

var transcriptPayloadGenesisV32Migration = versionedSchemaMigration{
	version:    32,
	name:       "transcript-payload-genesis-authority",
	statements: transcriptstore.PayloadGenesisV32Statements(),
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          transcriptPayloadGenesisV32RuleSpec,
		PreflightIdentity: transcriptPayloadGenesisV32PreflightIdentity,
	},
}

func preflightTranscriptPayloadGenesisV32(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range []string{
		"transcript_payload_genesis_receipts",
		"transcript_payload_genesis_validate",
		"transcript_payload_genesis_immutable",
		"transcript_frame_authority_v31_staging",
	} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript payload genesis cohort")
		}
		if count != 0 {
			return errors.New("transcript payload genesis cohort is polluted")
		}
	}
	if err := validateMigrationObjects(ctx, executor,
		transcriptstore.HistoryActivationV31ObjectNames(), transcriptstore.HistoryActivationV31Statements()); err != nil {
		return errors.New("transcript history activation v31 cohort identity mismatch")
	}
	return nil
}

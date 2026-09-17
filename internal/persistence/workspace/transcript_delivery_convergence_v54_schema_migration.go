package workspace

import (
	"context"
	"errors"
)

const (
	transcriptDeliveryConvergenceV54CallbackID        = "transcript-delivery-convergence-v54-noop"
	transcriptDeliveryConvergenceV54PreflightIdentity = "transcript-delivery-frontier-index-absent-v1"
	transcriptDeliveryConvergenceV54RuleSpec          = "synon.workspace.transcript-delivery-convergence.v54"
)

var transcriptDeliveryConvergenceV54Migration = versionedSchemaMigration{
	version: 54,
	name:    "transcript-delivery-frontier-convergence",
	statements: []string{
		`CREATE INDEX transcript_delivery_order
			ON transcript_delivery_intents(destination,stream_uid,route_generation,publication_seq,status)`,
		`UPDATE transcript_delivery_intents
		SET status='pending',claim_token_sha256=NULL,claimed_by=NULL,
			lease_expires_at=NULL,next_attempt_at=NULL,delivered_at=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE destination='ws' AND status='failed'
			AND attempt_count BETWEEN 0 AND 99
			AND last_error_code='projection_failed'
			AND EXISTS (
				SELECT 1 FROM transcript_events event
				WHERE event.stream_uid=transcript_delivery_intents.stream_uid
					AND event.publication_seq=transcript_delivery_intents.publication_seq
					AND event.event_type IN ('user_input_response','tool_recovery_settlement')
			)
			AND EXISTS (
				SELECT 1 FROM transcript_delivery_routes route
				WHERE route.stream_uid=transcript_delivery_intents.stream_uid
					AND route.destination=transcript_delivery_intents.destination
					AND route.current_generation=transcript_delivery_intents.route_generation
					AND route.status='active'
			)`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptDeliveryConvergenceV54CallbackID,
		RuleSpec:          transcriptDeliveryConvergenceV54RuleSpec,
		PreflightIdentity: transcriptDeliveryConvergenceV54PreflightIdentity,
	},
}

func preflightTranscriptDeliveryConvergenceV54(
	ctx context.Context,
	executor schemaMigrationQueryExecutor,
) error {
	var tables int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN (
			'transcript_delivery_intents','transcript_delivery_routes','transcript_events'
		)`).Scan(&tables); err != nil {
		return errors.New("inspect transcript delivery convergence schema")
	}
	if tables != 3 {
		return errors.New("transcript delivery convergence base schema is unavailable")
	}
	var indexes int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='index' AND name='transcript_delivery_order'`).Scan(&indexes); err != nil {
		return errors.New("inspect transcript delivery convergence index")
	}
	if indexes != 0 {
		return errors.New("transcript delivery convergence index already exists")
	}
	return nil
}

package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	notificationsV28PreflightIdentity = "canonical-transcript-v27-notifications-empty-v1"
	notificationsV28RuleSpec          = "synon.workspace.notifications.v28"
)

var notificationV28ObjectNames = []string{
	"notifications",
	"notifications_recipient_unread_idx",
	"notifications_recipient_type_idx",
}

var notificationsV28Migration = versionedSchemaMigration{
	version: 28,
	name:    "durable-frame-notifications",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        transcriptSchemaNoopCallbackID,
		RuleSpec:          notificationsV28RuleSpec,
		PreflightIdentity: notificationsV28PreflightIdentity,
	},
	statements: []string{
		`CREATE TABLE notifications (
			id TEXT PRIMARY KEY,
			sequence INTEGER NOT NULL UNIQUE CHECK(sequence > 0),
			sender_frame_id TEXT NOT NULL,
			recipient_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			notification_type TEXT NOT NULL,
			payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
			claim_token TEXT NOT NULL DEFAULT '',
			claim_expires_at TIMESTAMP,
			read_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX notifications_recipient_unread_idx
			ON notifications(recipient_frame_id,sequence) WHERE read_at IS NULL`,
		`CREATE INDEX notifications_recipient_type_idx
			ON notifications(recipient_frame_id,notification_type,sequence)`,
	},
}

func preflightNotificationsV28(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	for _, object := range notificationV28ObjectNames {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect durable notification cohort")
		}
		if count != 0 {
			return errors.New("durable notification cohort is polluted")
		}
	}
	if err := validateFrameIncarnationV26Shape(ctx, executor); err != nil {
		return err
	}
	for _, object := range transcriptstore.HistoryClassificationV27ObjectNames() {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil {
			return errors.New("inspect transcript history classification v27 cohort")
		}
		if count != 1 {
			return errors.New("transcript history classification v27 cohort identity mismatch")
		}
	}
	return nil
}

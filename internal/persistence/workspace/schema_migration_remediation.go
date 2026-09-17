package workspace

import (
	"context"
	"database/sql"
	"errors"
)

const (
	transcriptHistoryOrdinaryV33Checksum = "d8b401e9f842e2957ef49761659cd0fa2064890fa083d136d780b00763b1c74b"
	transcriptHistoryOrdinaryV33RepairID = "sqlite-deferred-fk-rebind-v1"
)

type schemaMigrationRemediation struct {
	version                  int
	name, checksum, repairID string
	run                      func(context.Context, *sql.Tx) error
}

var schemaMigrationRemediations = []schemaMigrationRemediation{
	{
		version: 33, name: "transcript-ordinary-history-authority",
		checksum: transcriptHistoryOrdinaryV33Checksum,
		repairID: transcriptHistoryOrdinaryV33RepairID,
		run:      rebindActivatedFrameAuthorityAfterV33ReceiptRebuild,
	},
}

func schemaMigrationRemediationID(migration versionedSchemaMigration) string {
	checksum, err := migration.validatedChecksum()
	if err != nil {
		return ""
	}
	return schemaMigrationRemediationIDForRecord(migration.version, migration.name, checksum)
}

func schemaMigrationRemediationIDForRecord(version int, name, checksum string) string {
	for _, remediation := range schemaMigrationRemediations {
		if remediation.version == version && remediation.name == name && remediation.checksum == checksum {
			return remediation.repairID
		}
	}
	return ""
}

func runSchemaMigrationRemediation(ctx context.Context, tx *sql.Tx, migration versionedSchemaMigration) error {
	checksum, err := migration.validatedChecksum()
	if err != nil {
		return err
	}
	for _, remediation := range schemaMigrationRemediations {
		if remediation.version == migration.version && remediation.name == migration.name && remediation.checksum == checksum {
			if remediation.run == nil {
				return errors.New("schema migration remediation handler is unavailable")
			}
			return remediation.run(ctx, tx)
		}
	}
	return nil
}

func validateSchemaMigrationRemediationRegistry(migrations []versionedSchemaMigration) error {
	seenRepairIDs := map[string]bool{}
	for _, remediation := range schemaMigrationRemediations {
		if remediation.version <= 0 || remediation.name == "" || remediation.checksum == "" ||
			remediation.repairID == "" || remediation.run == nil || seenRepairIDs[remediation.repairID] {
			return errors.New("schema migration remediation registry is invalid")
		}
		seenRepairIDs[remediation.repairID] = true
		matched := false
		for _, migration := range migrations {
			checksum, err := migration.validatedChecksum()
			if err != nil {
				return err
			}
			if migration.version == remediation.version && migration.name == remediation.name &&
				checksum == remediation.checksum {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("schema migration remediation identity is not registered")
		}
	}
	return nil
}

func rebindActivatedFrameAuthorityAfterV33ReceiptRebuild(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE synon_v33_authority_rebind AS
		SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,genesis_id,updated_at
		FROM transcript_frame_authority WHERE 0`); err != nil {
		return errors.New("prepare activated frame authority receipt rebind")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO synon_v33_authority_rebind
		SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,genesis_id,updated_at
		FROM transcript_frame_authority WHERE activation_id IS NOT NULL`); err != nil {
		return errors.New("stage activated frame authority receipt rebind")
	}
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM synon_v33_authority_rebind`).Scan(&expected); err != nil {
		return errors.New("count activated frame authority receipt rebind")
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM transcript_frame_authority WHERE activation_id IS NOT NULL`)
	if err != nil {
		return errors.New("detach activated frame authority from rebuilt receipts")
	}
	if count, err := deleted.RowsAffected(); err != nil || count != expected {
		return errors.New("detach activated frame authority count mismatch")
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,genesis_id,updated_at
		FROM synon_v33_authority_rebind ORDER BY owner_id,session_id`)
	if err != nil {
		return errors.New("restore activated frame authority after receipt rebuild")
	}
	if count, err := inserted.RowsAffected(); err != nil || count != expected {
		return errors.New("restore activated frame authority count mismatch")
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE synon_v33_authority_rebind`); err != nil {
		return errors.New("remove activated frame authority receipt rebind staging")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return errors.New("inspect foreign keys after receipt rebuild")
	}
	if rows.Next() {
		_ = rows.Close()
		return errors.New("receipt rebuild leaves foreign key violations")
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errors.New("inspect foreign keys after receipt rebuild")
	}
	if err := rows.Close(); err != nil {
		return errors.New("close foreign key inspection after receipt rebuild")
	}
	// foreign_key_check above proves the final graph is valid. Clearing the
	// transaction-local defer flag discards SQLite's stale parent-DROP counter
	// before the immutable journal row and COMMIT; no unchecked business write
	// follows this remediation.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=OFF`); err != nil {
		return errors.New("finalize deferred foreign key receipt rebind")
	}
	return nil
}

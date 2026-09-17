package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const workspaceSchemaVersion = 69

type SchemaMigrationRecord struct {
	Version       int       `json:"version"`
	Name          string    `json:"name"`
	Checksum      string    `json:"checksum"`
	RemediationID string    `json:"remediationId,omitempty"`
	AppliedAt     time.Time `json:"appliedAt"`
}

type SchemaStatus struct {
	CurrentVersion int                     `json:"currentVersion"`
	TargetVersion  int                     `json:"targetVersion"`
	Migrations     []SchemaMigrationRecord `json:"migrations"`
}

// ExpectedSchemaStatus returns the immutable migration identities compiled
// into this binary without opening or modifying a workspace database.
func ExpectedSchemaStatus() (SchemaStatus, error) {
	if err := validateSchemaMigrationIdentityRegistry(workspaceSchemaMigrations); err != nil {
		return SchemaStatus{}, err
	}
	status := SchemaStatus{
		CurrentVersion: workspaceSchemaVersion,
		TargetVersion:  workspaceSchemaVersion,
		Migrations:     make([]SchemaMigrationRecord, 0, len(workspaceSchemaMigrations)),
	}
	for _, migration := range workspaceSchemaMigrations {
		checksum, err := migration.validatedChecksum()
		if err != nil {
			return SchemaStatus{}, fmt.Errorf("workspace schema migration %d identity: %w", migration.version, err)
		}
		status.Migrations = append(status.Migrations, SchemaMigrationRecord{
			Version: migration.version, Name: migration.name, Checksum: checksum,
			RemediationID: schemaMigrationRemediationID(migration),
		})
	}
	return status, nil
}

type versionedSchemaMigration struct {
	version    int
	name       string
	statements []string
	identityV2 *schemaMigrationIdentityV2
}

var workspaceSchemaMigrations = append(append([]versionedSchemaMigration{
	{
		version: 1,
		name:    "legacy-idempotent-baseline",
	},
	{
		version: 2,
		name:    "v11-ranked-memory-scope",
		statements: []string{
			`ALTER TABLE projects ADD COLUMN memory_enabled INTEGER`,
			`ALTER TABLE memories ADD COLUMN subject_artifact_id TEXT REFERENCES artifacts(id) ON DELETE CASCADE`,
			`ALTER TABLE memories ADD COLUMN subject_version_id TEXT REFERENCES artifact_versions(id) ON DELETE SET NULL`,
			`ALTER TABLE memories ADD COLUMN subject_frame_id TEXT REFERENCES frames(id) ON DELETE SET NULL`,
			`ALTER TABLE memories ADD COLUMN source_frame_id TEXT REFERENCES frames(id) ON DELETE SET NULL`,
			`ALTER TABLE memories ADD COLUMN last_surfaced_at TIMESTAMP`,
			`ALTER TABLE memories ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0 CHECK (access_count >= 0)`,
			`ALTER TABLE memories ADD COLUMN importance REAL NOT NULL DEFAULT 1 CHECK (importance >= 0 AND importance <= 10)`,
			`CREATE INDEX IF NOT EXISTS memories_subject_artifact_idx ON memories (subject_artifact_id)`,
			`CREATE INDEX IF NOT EXISTS memories_subject_version_idx ON memories (subject_version_id)`,
			`CREATE INDEX IF NOT EXISTS memories_subject_frame_idx ON memories (subject_frame_id)`,
			`CREATE INDEX IF NOT EXISTS memories_recall_idx ON memories (user_id, superseded_by, updated_at DESC)`,
		},
	},
	{
		version: 3,
		name:    "web-memory-preferences",
		statements: []string{
			`CREATE TABLE memory_user_settings (
				user_id TEXT PRIMARY KEY,
				enabled INTEGER NOT NULL DEFAULT 1,
				updated_at TIMESTAMP NOT NULL
			)`,
		},
	},
}, rescuedWorkspaceSchemaMigrations...), schemaRescueV21Migration, providerV22Migration, transcriptV23Migration, transcriptV24Migration, transcriptV25Migration, frameIncarnationV26Migration, transcriptHistoryClassificationV27Migration, notificationsV28Migration, transcriptHistoryBackfillV29Migration, transcriptHistoryCutoverV30Migration, transcriptHistoryActivationV31Migration, transcriptPayloadGenesisV32Migration, transcriptHistoryOrdinaryV33Migration, transcriptHistoryOrdinaryCursorV34Migration, transcriptTypedHistoryBootstrapV35Migration, modelProviderGenerationV36Migration, scientificComputeAuthorityV37Migration, transcriptWebReadModelV38Migration, kernelLocalOperationV39Migration, toolCallBatchV40Migration, kernelToolResultV41Migration, scientificComputeSubmissionV42Migration, artifactCollectionV43Migration, kernelDetachedExecutionV44Migration, kernelDetachedExecutionV45Migration, runtimeAuditDeleteV46Migration, transcriptWebProjectorV47Migration, transcriptWebProjectorV48Migration, transcriptWebProjectorV49Migration, runnerLargeToolResultV50Migration, kernelDetachedExecutionV51Migration, runnerLargeToolResultV52Migration, mcpToolCatalogV53Migration, transcriptDeliveryConvergenceV54Migration, kernelSoftwareRuntimeV55Migration, kernelBashV56Migration, toolBatchV57Migration, transcriptWebProjectorV58Migration, transcriptWebProjectorV59Migration, transcriptWebProjectorV60Migration, generatedPlanRetentionV61Migration, kernelTrustedMountV62Migration, kernelResultSpoolV63Migration, transcriptWebProjectorV64Migration, transcriptWebProjectorV65Migration, transcriptWebProjectorV66Migration, transcriptWebProjectorV67Migration, kernelProviderCancelV68Migration, kernelStartupV69Migration)

func schemaMigrationNeedsDedicatedConnection(migration versionedSchemaMigration) bool {
	if migration.identityV2 == nil {
		return false
	}
	switch migration.identityV2.CallbackID {
	case kernelSoftwareRuntimeV55CallbackID, kernelBashV56CallbackID, kernelProviderCancelV68CallbackID, kernelStartupV69CallbackID:
		return true
	default:
		return false
	}
}

func prepareSchemaJournal(ctx context.Context, executor schemaMigrationExecutor) error {
	if executor == nil {
		return errors.New("workspace schema migration executor is required")
	}
	if _, err := executor.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workspace_schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL
	)`); err != nil {
		return fmt.Errorf("prepare workspace schema journal: %w", err)
	}
	var newest int
	if err := executor.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM workspace_schema_migrations`).Scan(&newest); err != nil {
		return fmt.Errorf("inspect workspace schema version: %w", err)
	}
	if newest > workspaceSchemaVersion {
		return fmt.Errorf("workspace schema version %d is newer than supported version %d", newest, workspaceSchemaVersion)
	}
	return nil
}

func applyVersionedSchemaMigrations(ctx context.Context, executor schemaMigrationExecutor, now func() time.Time) error {
	return applyVersionedSchemaMigrationsThrough(ctx, executor, now, workspaceSchemaVersion)
}

func applyVersionedSchemaMigrationsThrough(ctx context.Context, executor schemaMigrationExecutor, now func() time.Time, targetVersion int) error {
	if err := validateSchemaMigrationIdentityRegistry(workspaceSchemaMigrations); err != nil {
		return fmt.Errorf("validate workspace schema migration registry: %w", err)
	}
	if targetVersion < 0 || targetVersion > len(workspaceSchemaMigrations) {
		return fmt.Errorf("workspace schema target version %d is not supported", targetVersion)
	}
	if err := validateVersionedSchemaJournal(ctx, executor, targetVersion); err != nil {
		return err
	}
	for _, migration := range workspaceSchemaMigrations {
		if migration.version > targetVersion {
			break
		}
		checksum, err := migration.validatedChecksum()
		if err != nil {
			return fmt.Errorf("validate workspace schema migration %d identity: %w", migration.version, err)
		}
		var name, recordedChecksum string
		err = executor.QueryRowContext(ctx, `SELECT name, checksum FROM workspace_schema_migrations WHERE version = ?`, migration.version).Scan(&name, &recordedChecksum)
		switch {
		case err == nil:
			if name != migration.name || recordedChecksum != checksum {
				return fmt.Errorf("workspace schema migration %d identity mismatch", migration.version)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read workspace schema migration %d: %w", migration.version, err)
		}
		migrationExecutor := executor
		var dedicatedConnection *sql.Conn
		if schemaMigrationNeedsDedicatedConnection(migration) {
			if database, ok := executor.(*sql.DB); ok {
				dedicatedConnection, err = database.Conn(ctx)
				if err != nil {
					return fmt.Errorf("acquire dedicated workspace schema migration %d connection: %w", migration.version, err)
				}
				migrationExecutor = dedicatedConnection
			}
		}
		if err := runSchemaMigrationPreflight(ctx, migrationExecutor, migration); err != nil {
			if dedicatedConnection != nil {
				_ = dedicatedConnection.Close()
			}
			return fmt.Errorf("preflight workspace schema migration %d: %w", migration.version, err)
		}
		if err := applyVersionedSchemaMigration(ctx, migrationExecutor, now, migration, checksum); err != nil {
			if dedicatedConnection != nil {
				_ = dedicatedConnection.Close()
			}
			return err
		}
		if dedicatedConnection != nil {
			if err := dedicatedConnection.Close(); err != nil {
				return fmt.Errorf("release dedicated workspace schema migration %d connection: %w", migration.version, err)
			}
		}
	}
	return nil
}

func validateVersionedSchemaJournal(ctx context.Context, executor schemaMigrationExecutor, targetVersion int) error {
	rows, err := executor.QueryContext(ctx, `SELECT version, name, checksum FROM workspace_schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("validate workspace schema journal: %w", err)
	}
	defer rows.Close()
	expectedVersion := 1
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("scan workspace schema journal: %w", err)
		}
		if version != expectedVersion || version > targetVersion || version > len(workspaceSchemaMigrations) {
			return fmt.Errorf("workspace schema migration journal is not a contiguous supported prefix")
		}
		migration := workspaceSchemaMigrations[version-1]
		expectedChecksum, checksumErr := migration.validatedChecksum()
		if checksumErr != nil {
			return fmt.Errorf("validate workspace schema migration %d identity: %w", version, checksumErr)
		}
		if migration.version != version || migration.name != name || expectedChecksum != checksum {
			return fmt.Errorf("workspace schema migration %d identity mismatch", version)
		}
		expectedVersion++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate workspace schema journal: %w", err)
	}
	return nil
}

func applyVersionedSchemaMigration(ctx context.Context, executor schemaMigrationExecutor, now func() time.Time, migration versionedSchemaMigration, checksum string) (returnErr error) {
	disableForeignKeys := schemaMigrationNeedsDedicatedConnection(migration)
	if disableForeignKeys {
		var originalForeignKeys int
		if err := executor.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&originalForeignKeys); err != nil ||
			(originalForeignKeys != 0 && originalForeignKeys != 1) {
			return fmt.Errorf("inspect foreign key enforcement before workspace schema migration %d", migration.version)
		}
		if originalForeignKeys == 1 {
			if _, err := executor.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				return fmt.Errorf("disable foreign keys for workspace schema migration %d", migration.version)
			}
		}
		var disabled int
		if err := executor.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&disabled); err != nil || disabled != 0 {
			return fmt.Errorf("disable foreign keys for workspace schema migration %d", migration.version)
		}
		defer func() {
			restoreStatement := `PRAGMA foreign_keys=OFF`
			if originalForeignKeys == 1 {
				restoreStatement = `PRAGMA foreign_keys=ON`
			}
			if _, err := executor.ExecContext(context.Background(), restoreStatement); err != nil {
				if returnErr == nil {
					returnErr = fmt.Errorf("restore foreign keys after workspace schema migration %d", migration.version)
				}
				return
			}
			var restored int
			if err := executor.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&restored); err != nil || restored != originalForeignKeys {
				if returnErr == nil {
					returnErr = fmt.Errorf("verify foreign keys after workspace schema migration %d", migration.version)
				}
				return
			}
			var failures int
			if err := executor.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&failures); err != nil || failures != 0 {
				if returnErr == nil {
					returnErr = fmt.Errorf("workspace schema migration %d left an invalid foreign key graph", migration.version)
				}
			}
		}()
	}
	tx, err := executor.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace schema migration %d: %w", migration.version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if migration.version == 5 {
		if _, err := tx.ExecContext(ctx, legacyMemoryCategoryAssignmentsDDL); err != nil {
			return fmt.Errorf("prepare workspace schema migration %d: %w", migration.version, err)
		}
	}
	if migration.version == 16 {
		for _, statement := range legacyTaskIntentDDL {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("prepare workspace schema migration %d: %w", migration.version, err)
			}
		}
	}
	callbackAfterStatements := schemaMigrationCallbackRunsAfterStatements(migration)
	if !callbackAfterStatements {
		if err := runSchemaMigrationCallback(ctx, tx, migration); err != nil {
			return fmt.Errorf("apply workspace schema migration %d callback: %w", migration.version, err)
		}
	}
	for index, statement := range migration.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply workspace schema migration %d statement %d: %w", migration.version, index+1, err)
		}
	}
	if callbackAfterStatements {
		if err := runSchemaMigrationCallback(ctx, tx, migration); err != nil {
			return fmt.Errorf("apply workspace schema migration %d callback: %w", migration.version, err)
		}
	}
	if err := runSchemaMigrationRemediation(ctx, tx, migration); err != nil {
		return fmt.Errorf("apply workspace schema migration %d post-statement fence: %w", migration.version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		migration.version, migration.name, checksum, now().UTC()); err != nil {
		return fmt.Errorf("record workspace schema migration %d: %w", migration.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace schema migration %d: %w", migration.version, err)
	}
	return nil
}

func (migration versionedSchemaMigration) checksum() string {
	value := fmt.Sprintf("%d\x00%s\x00%s", migration.version, migration.name, strings.Join(migration.statements, "\x00"))
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Store) SchemaStatus(ctx context.Context) (SchemaStatus, error) {
	if s == nil || s.db == nil {
		return SchemaStatus{}, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT version, name, checksum, applied_at FROM workspace_schema_migrations ORDER BY version`)
	if err != nil {
		return SchemaStatus{}, fmt.Errorf("query workspace schema journal: %w", err)
	}
	defer rows.Close()
	status := SchemaStatus{TargetVersion: workspaceSchemaVersion, Migrations: []SchemaMigrationRecord{}}
	for rows.Next() {
		var record SchemaMigrationRecord
		if err := rows.Scan(&record.Version, &record.Name, &record.Checksum, &record.AppliedAt); err != nil {
			return SchemaStatus{}, fmt.Errorf("scan workspace schema migration: %w", err)
		}
		status.Migrations = append(status.Migrations, record)
		status.Migrations[len(status.Migrations)-1].RemediationID = schemaMigrationRemediationIDForRecord(
			record.Version, record.Name, record.Checksum,
		)
		status.CurrentVersion = record.Version
	}
	if err := rows.Err(); err != nil {
		return SchemaStatus{}, fmt.Errorf("iterate workspace schema journal: %w", err)
	}
	return status, nil
}

package v11

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

func Migrate(ctx context.Context, options Options) (Report, error) {
	started := time.Now().UTC()
	source, err := canonicalRegularFile(options.SourceDB)
	if err != nil {
		return Report{}, fmt.Errorf("validate v1.1 source database: %w", err)
	}
	target, err := cleanAbsoluteDirectoryPath(options.TargetHome)
	if err != nil {
		return Report{}, fmt.Errorf("validate target home: %w", err)
	}
	if pathContains(filepath.Dir(source), target) || pathContains(target, filepath.Dir(source)) {
		return Report{}, errors.New("source and target paths must not overlap")
	}
	if options.ConflictPolicy == "" {
		options.ConflictPolicy = ConflictAbort
	}
	if options.ConflictPolicy != ConflictAbort && options.ConflictPolicy != ConflictReplace {
		return Report{}, fmt.Errorf("unsupported migration conflict policy %q", options.ConflictPolicy)
	}

	targetState, err := inspectTarget(target)
	if err != nil {
		return Report{}, err
	}
	if targetState.report != nil {
		inspection, err := Inspect(ctx, source)
		if err != nil {
			return Report{}, err
		}
		if targetState.report.Status == StatusCompleted && targetState.report.SourceSHA256 == inspection.SourceSHA256 {
			report := *targetState.report
			report.Idempotent = true
			return report, nil
		}
		if options.ConflictPolicy == ConflictAbort {
			return Report{}, errors.New("target already contains a different completed v1.1 migration")
		}
	}
	if targetState.nonEmpty && options.ConflictPolicy == ConflictAbort {
		return Report{}, errors.New("migration target is not empty; use replace policy only after stopping the runtime")
	}

	migrationID := uuid.NewString()
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Report{}, err
	}
	stage := filepath.Join(parent, ".synon-v11-migrate-"+migrationID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return Report{}, fmt.Errorf("create migration staging directory: %w", err)
	}
	stageCommitted := false
	defer func() {
		if !stageCommitted {
			_ = os.RemoveAll(stage)
		}
	}()

	snapshotDirectory := filepath.Join(stage, ".migration-working")
	if err := os.Mkdir(snapshotDirectory, 0o700); err != nil {
		return Report{}, fmt.Errorf("create migration snapshot directory: %w", err)
	}
	snapshot, err := snapshotSQLite(ctx, source, snapshotDirectory)
	if err != nil {
		return Report{}, err
	}
	inspection, err := inspectSnapshot(ctx, snapshot, source)
	if err != nil {
		return Report{}, err
	}
	dispositions, err := planTableDispositions(inspection.TableCounts)
	if err != nil {
		return Report{}, err
	}
	sourceDataDir := strings.TrimSpace(options.SourceDataDir)
	if sourceDataDir == "" {
		sourceDataDir = filepath.Dir(source)
	}
	sourceDataDir, err = canonicalDirectory(sourceDataDir)
	if err != nil {
		return Report{}, fmt.Errorf("validate v1.1 source data directory: %w", err)
	}

	targetDB := filepath.Join(stage, filepath.FromSlash(WorkspaceDatabaseRelativePath))
	store, err := workspace.Open(targetDB)
	if err != nil {
		return Report{}, fmt.Errorf("initialize Go workspace database: %w", err)
	}
	if err := store.Close(); err != nil {
		return Report{}, err
	}
	if err := copyArtifactBlobs(ctx, snapshot, filepath.Join(sourceDataDir, "artifacts"), targetDB+".blobs"); err != nil {
		return Report{}, err
	}
	imported, err := importWorkspace(ctx, snapshot, targetDB, inspection.TableCounts)
	if err != nil {
		return Report{}, err
	}
	modelCount, err := importModelProfiles(ctx, sourceDataDir, stage, targetDB)
	if err != nil {
		return Report{}, err
	}
	imported["model_providers"] = modelCount
	if _, err := importLegacyUserSecrets(ctx, snapshot, sourceDataDir, stage); err != nil {
		return Report{}, err
	}
	if err := importPreferences(ctx, sourceDataDir, stage, targetDB); err != nil {
		return Report{}, err
	}
	verification, err := workspace.Open(targetDB)
	if err != nil {
		return Report{}, fmt.Errorf("open migrated Go workspace: %w", err)
	}
	if err := verification.Close(); err != nil {
		return Report{}, err
	}
	if err := os.RemoveAll(snapshotDirectory); err != nil {
		return Report{}, fmt.Errorf("remove temporary source snapshot: %w", err)
	}

	report := Report{
		Schema: SchemaName, Status: StatusCompleted, MigrationID: migrationID,
		SourceDB: source, SourceSHA256: inspection.SourceSHA256, SourceBytes: inspection.SourceBytes,
		TargetHome:     target,
		TargetDatabase: WorkspaceDatabaseRelativePath, Imported: imported,
		SourceTableCounts: inspection.TableCounts, ExcludedTables: discardedSourceTables(dispositions),
		TableDispositions: dispositions,
		StartedAt:         started, CompletedAt: time.Now().UTC(),
	}
	if targetState.exists {
		report.BackupPath = target + ".pre-v11-" + migrationID
	}
	if err := writeReport(filepath.Join(stage, ManifestFilename), report); err != nil {
		return Report{}, err
	}
	if err := commitStagingTarget(stage, target, report.BackupPath, targetState.exists); err != nil {
		return Report{}, err
	}
	stageCommitted = true
	return report, nil
}

type targetInspection struct {
	exists   bool
	nonEmpty bool
	report   *Report
}

func inspectTarget(target string) (targetInspection, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return targetInspection{}, nil
	}
	if err != nil {
		return targetInspection{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return targetInspection{}, errors.New("migration target must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || resolved != target {
		return targetInspection{}, errors.New("migration target path must not contain symbolic links")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return targetInspection{}, err
	}
	state := targetInspection{exists: true, nonEmpty: len(entries) > 0}
	raw, err := os.ReadFile(filepath.Join(target, ManifestFilename))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return targetInspection{}, err
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return targetInspection{}, fmt.Errorf("decode existing migration manifest: %w", err)
	}
	state.report = &report
	return state, nil
}

func commitStagingTarget(stage, target, backup string, targetExists bool) error {
	if targetExists {
		if backup == "" {
			return errors.New("backup path is required when replacing a target")
		}
		if _, err := os.Lstat(backup); err == nil {
			return errors.New("migration backup path already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("backup existing target: %w", err)
		}
		if err := os.Rename(stage, target); err != nil {
			restoreErr := os.Rename(backup, target)
			return fmt.Errorf("activate migrated target: %w (restore error: %v)", err, restoreErr)
		}
		return nil
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("activate migrated target: %w", err)
	}
	return nil
}

func writeReport(path string, report Report) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Chmod(path, 0o600)
}

func Rollback(ctx context.Context, targetHome, migrationID string) (RollbackReport, error) {
	if err := ctx.Err(); err != nil {
		return RollbackReport{}, err
	}
	migration, err := LoadReport(targetHome)
	if err != nil {
		return RollbackReport{}, fmt.Errorf("load migration manifest: %w", err)
	}
	target := migration.TargetHome
	if migration.MigrationID != strings.TrimSpace(migrationID) {
		return RollbackReport{}, errors.New("migration id does not match active target")
	}
	if migration.BackupPath == "" {
		return RollbackReport{}, errors.New("migration has no replaced target backup to restore")
	}
	backup, err := canonicalDirectory(migration.BackupPath)
	if err != nil {
		return RollbackReport{}, fmt.Errorf("validate migration backup: %w", err)
	}
	preserved := target + ".migrated-" + migration.MigrationID
	if _, err := os.Lstat(preserved); err == nil {
		return RollbackReport{}, errors.New("rollback preservation path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RollbackReport{}, err
	}
	if err := os.Rename(target, preserved); err != nil {
		return RollbackReport{}, fmt.Errorf("preserve migrated target: %w", err)
	}
	if err := os.Rename(backup, target); err != nil {
		restoreErr := os.Rename(preserved, target)
		return RollbackReport{}, fmt.Errorf("restore pre-migration target: %w (recovery error: %v)", err, restoreErr)
	}
	migration.TargetHome = preserved
	if err := writeReport(filepath.Join(preserved, ManifestFilename), migration); err != nil {
		return RollbackReport{}, fmt.Errorf("record preserved migrated target after restoring previous target: %w", err)
	}
	return RollbackReport{
		Schema: SchemaName, Status: StatusRolledBack, MigrationID: migration.MigrationID,
		TargetHome: target, RestoredPath: target, PreservedMigratedPath: preserved,
		CompletedAt: time.Now().UTC(),
	}, nil
}

func cleanAbsoluteDirectoryPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("target home is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := cleanAbsoluteDirectoryPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return "", errors.New("directory path must not contain symbolic links")
	}
	return absolute, nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}

func countTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var count int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&count)
	return count, err
}

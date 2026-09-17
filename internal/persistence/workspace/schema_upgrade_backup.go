package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/sqlitebackup"
)

const workspaceSchemaBackupRetention = 2

func prepareWorkspaceDatabasePath(path string) (string, bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve workspace database path: %w", err)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", false, fmt.Errorf("prepare workspace database parent: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || resolvedParent != parent {
		return "", false, errors.New("workspace database parent must not contain symbolic links")
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return absolute, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect workspace database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, errors.New("workspace database must be a regular file")
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return "", false, fmt.Errorf("secure workspace database: %w", err)
	}
	return absolute, true, nil
}

func backupWorkspaceBeforeSchemaUpgrade(ctx context.Context, db *sql.DB, path string, existed bool) (string, error) {
	if !existed {
		return "", nil
	}
	current, err := currentWorkspaceSchemaVersion(ctx, db)
	if err != nil {
		return "", err
	}
	if current >= workspaceSchemaVersion {
		return "", nil
	}
	root := path + ".schema-backups"
	if err := ensurePrivateDirectory(root); err != nil {
		return "", fmt.Errorf("prepare workspace schema backup root: %w", err)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire workspace schema backup connection: %w", err)
	}
	defer connection.Close()
	result, err := sqlitebackup.BackupFromConn(ctx, connection, root)
	if err != nil {
		return "", fmt.Errorf("back up workspace before schema upgrade: %w", err)
	}
	return result.Path, nil
}

func currentWorkspaceSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var journalTables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name='workspace_schema_migrations'`).Scan(&journalTables); err != nil {
		return 0, fmt.Errorf("inspect workspace schema journal: %w", err)
	}
	if journalTables == 0 {
		return 0, nil
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM workspace_schema_migrations`).Scan(&current); err != nil {
		return 0, fmt.Errorf("inspect workspace schema version: %w", err)
	}
	return current, nil
}

type workspaceSchemaBackup struct {
	directory string
	modified  int64
	current   bool
}

// pruneWorkspaceSchemaBackups bounds rollback snapshots after a successful
// migration and blob reconciliation. The newest two are sufficient for the
// current and previous rollback points; older per-upgrade snapshots are not a
// second history system.
func pruneWorkspaceSchemaBackups(databasePath, currentBackupPath string) error {
	root := databasePath + ".schema-backups"
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list workspace schema backups: %w", err)
	}
	currentDirectory := ""
	if strings.TrimSpace(currentBackupPath) != "" {
		currentDirectory = filepath.Clean(filepath.Dir(currentBackupPath))
	}
	backups := make([]workspaceSchemaBackup, 0, len(entries))
	for _, entry := range entries {
		if !validWorkspaceSchemaBackupName(entry.Name()) {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect workspace schema backup %q: %w", entry.Name(), err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace schema backup %q is not a safe directory", entry.Name())
		}
		snapshot := filepath.Join(directory, "snapshot.sqlite")
		snapshotInfo, err := os.Lstat(snapshot)
		if err != nil || !snapshotInfo.Mode().IsRegular() || snapshotInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace schema backup %q has no safe snapshot", entry.Name())
		}
		backups = append(backups, workspaceSchemaBackup{
			directory: directory,
			modified:  snapshotInfo.ModTime().UnixNano(),
			current:   currentDirectory != "" && filepath.Clean(directory) == currentDirectory,
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].current != backups[j].current {
			return backups[i].current
		}
		if backups[i].modified != backups[j].modified {
			return backups[i].modified > backups[j].modified
		}
		return backups[i].directory > backups[j].directory
	})
	for _, backup := range backups[min(workspaceSchemaBackupRetention, len(backups)):] {
		relative, err := filepath.Rel(root, backup.directory)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return errors.New("workspace schema backup cleanup escaped its root")
		}
		if err := os.RemoveAll(backup.directory); err != nil {
			return fmt.Errorf("remove expired workspace schema backup %q: %w", relative, err)
		}
	}
	return nil
}

func validWorkspaceSchemaBackupName(name string) bool {
	const prefix = "backup-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, value := range name[len(prefix):] {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
	}
	return true
}

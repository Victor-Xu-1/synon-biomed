package v11

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/sqlitebackup"
)

var requiredSourceColumns = map[string][]string{
	"projects":          {"id", "created_at", "updated_at", "user_id"},
	"frames":            {"id", "agent_name", "status", "project_id", "conversation_type", "created_at", "updated_at"},
	"artifacts":         {"id", "project_id", "filename", "latest_version_id", "created_at"},
	"artifact_versions": {"id", "artifact_id", "version_number", "content_type", "checksum", "storage_path", "created_at"},
}

func Inspect(ctx context.Context, sourceDB string) (Inspection, error) {
	snapshot, cleanup, err := makeTemporarySnapshot(ctx, sourceDB)
	if err != nil {
		return Inspection{}, err
	}
	defer cleanup()
	return inspectSnapshot(ctx, snapshot, sourceDB)
}

func inspectSnapshot(ctx context.Context, snapshot, reportedSource string) (Inspection, error) {
	info, err := regularFile(snapshot)
	if err != nil {
		return Inspection{}, fmt.Errorf("inspect v1.1 database: %w", err)
	}
	db, err := sql.Open("sqlite", readOnlyDSN(snapshot))
	if err != nil {
		return Inspection{}, fmt.Errorf("open v1.1 database read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var quickCheck string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return Inspection{}, fmt.Errorf("quick-check v1.1 database: %w", err)
	}
	if quickCheck != "ok" {
		return Inspection{}, fmt.Errorf("v1.1 database quick-check failed: %s", quickCheck)
	}
	tables, err := sourceTables(ctx, db)
	if err != nil {
		return Inspection{}, err
	}
	for table, columns := range requiredSourceColumns {
		if _, ok := tables[table]; !ok {
			return Inspection{}, fmt.Errorf("v1.1 database is missing required table %q", table)
		}
		available, err := tableColumns(ctx, db, table)
		if err != nil {
			return Inspection{}, err
		}
		for _, column := range columns {
			if !available[column] {
				return Inspection{}, fmt.Errorf("v1.1 table %q is missing required column %q", table, column)
			}
		}
	}
	counts := make(map[string]int64, len(tables))
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var count int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(name)).Scan(&count); err != nil {
			return Inspection{}, fmt.Errorf("count v1.1 table %s: %w", name, err)
		}
		counts[name] = count
	}
	var migrationCount, latestMigration int64
	if tables["__drizzle_migrations"] {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(CAST(created_at AS INTEGER)), 0) FROM __drizzle_migrations`).Scan(&migrationCount, &latestMigration); err != nil {
			return Inspection{}, fmt.Errorf("inspect v1.1 migration journal: %w", err)
		}
	}
	digest, err := fileSHA256(snapshot)
	if err != nil {
		return Inspection{}, err
	}
	absolute, err := filepath.Abs(reportedSource)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{
		Schema: SchemaName, SourceDB: filepath.Clean(absolute), SourceSHA256: digest,
		SourceBytes: info.Size(), MigrationCount: migrationCount, LatestMigration: latestMigration,
		QuickCheckOK: true, TableCounts: counts,
	}, nil
}

func makeTemporarySnapshot(ctx context.Context, sourceDB string) (string, func(), error) {
	source, err := canonicalRegularFile(sourceDB)
	if err != nil {
		return "", func() {}, err
	}
	directory, err := os.MkdirTemp("", "synon-v11-inspect-")
	if err != nil {
		return "", func() {}, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	snapshot, err := snapshotSQLite(ctx, source, directory)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return snapshot, cleanup, nil
}

func snapshotSQLite(ctx context.Context, source, root string) (string, error) {
	snapshotSource := source
	cleanupSource := func() {}
	if sourceSetIsReadOnly(source) {
		var err error
		snapshotSource, cleanupSource, err = mirrorSQLiteSourceSet(source, root)
		if err != nil {
			return "", fmt.Errorf("mirror read-only v1.1 database: %w", err)
		}
	}
	defer cleanupSource()
	snapshot, err := onlineBackupSQLite(ctx, snapshotSource, root)
	if err != nil {
		return "", fmt.Errorf("create consistent v1.1 database snapshot: %w", err)
	}
	return snapshot, nil
}

func onlineBackupSQLite(ctx context.Context, source, root string) (string, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(source))
	if err != nil {
		return "", fmt.Errorf("open source database for snapshot: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	connection, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("open source database connection for snapshot: %w", err)
	}
	defer connection.Close()
	result, err := sqlitebackup.BackupFromConn(ctx, connection, root)
	if err != nil {
		return "", err
	}
	return result.Path, nil
}

func sourceSetIsReadOnly(source string) bool {
	for _, path := range []string{source, filepath.Dir(source)} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().Perm()&0o222 == 0 {
			return true
		}
	}
	return false
}

func mirrorSQLiteSourceSet(source, parent string) (string, func(), error) {
	directory, err := os.MkdirTemp(parent, ".v11-readonly-source-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	mirror := filepath.Join(directory, "source.db")
	for index, suffix := range []string{"", "-journal", "-shm", "-wal"} {
		if err := copySQLiteSourceFile(source+suffix, mirror+suffix, index == 0); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return mirror, cleanup, nil
}

func copySQLiteSourceFile(source, target string, required bool) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sqlite source file is not a regular non-symlink file: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	copied, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if copied != info.Size() {
		return fmt.Errorf("sqlite source file changed while mirroring: %s", source)
	}
	return nil
}

func readOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	return u.String()
}

func sourceTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list v1.1 tables: %w", err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[name] = true
	}
	return tables, rows.Err()
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect v1.1 table %s: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func canonicalRegularFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("source database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source database must be a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return "", errors.New("source database path must not contain symbolic links")
	}
	return absolute, nil
}

func regularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("path is not a regular file")
	}
	return info, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

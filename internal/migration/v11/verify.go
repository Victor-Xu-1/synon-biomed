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

	workspace "synon-go/internal/persistence/workspace"
)

func LoadReport(targetHome string) (Report, error) {
	target, err := canonicalDirectory(targetHome)
	if err != nil {
		return Report{}, err
	}
	path := filepath.Join(target, ManifestFilename)
	info, err := os.Lstat(path)
	if err != nil {
		return Report{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Report{}, errors.New("migration manifest is not a regular non-symlink file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return Report{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	if report.Schema != SchemaName || report.Status != StatusCompleted || strings.TrimSpace(report.MigrationID) == "" {
		return Report{}, errors.New("migration manifest is not a completed SynonBiomed v1.1 migration")
	}
	if filepath.Clean(report.TargetHome) != target {
		return Report{}, errors.New("migration manifest target does not match its directory")
	}
	return report, nil
}

func Verify(ctx context.Context, targetHome string) (Verification, error) {
	report, err := LoadReport(targetHome)
	if err != nil {
		return Verification{}, err
	}
	target := report.TargetHome
	if len(report.SourceSHA256) != 64 || report.SourceBytes <= 0 || report.SourceSnapshot != "" {
		return Verification{}, errors.New("migration source identity is incomplete or an unclean source snapshot was retained")
	}
	targetDB, err := secureTargetRelative(target, report.TargetDatabase)
	if err != nil {
		return Verification{}, fmt.Errorf("validate target database path: %w", err)
	}
	store, err := workspace.Open(targetDB)
	if err != nil {
		return Verification{}, fmt.Errorf("verify migrated artifact storage: %w", err)
	}
	if err := store.Close(); err != nil {
		return Verification{}, err
	}
	db, err := sql.Open("sqlite", readOnlyDSN(targetDB))
	if err != nil {
		return Verification{}, err
	}
	defer db.Close()
	var quickCheck string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return Verification{}, err
	}
	if quickCheck != "ok" {
		return Verification{}, fmt.Errorf("migrated workspace quick-check failed: %s", quickCheck)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return Verification{}, err
	}
	if rows.Next() {
		rows.Close()
		return Verification{}, errors.New("migrated workspace contains foreign-key violations")
	}
	if err := rows.Close(); err != nil {
		return Verification{}, err
	}
	transformedSourceTables := map[string]struct{}{
		"frame_messages": {}, "custom_agent_prompts": {}, "oauth_tokens": {}, "events": {},
		"capability_settings": {}, "artifact_dependencies": {}, "user_secrets": {}, "managed_endpoints": {},
	}
	for table, expected := range report.Imported {
		if _, transformed := transformedSourceTables[table]; transformed {
			continue
		}
		actual, err := countTable(ctx, db, table)
		if err != nil {
			return Verification{}, fmt.Errorf("verify imported table %s: %w", table, err)
		}
		if actual != expected {
			return Verification{}, fmt.Errorf("imported table %s count changed: got %d, expected %d", table, actual, expected)
		}
	}
	return Verification{
		Schema: SchemaName, MigrationID: report.MigrationID, TargetHome: target,
		SourceIdentityRecorded: true, WorkspaceQuickCheckOK: true, ForeignKeysOK: true,
	}, nil
}

func secureTargetRelative(root, relative string) (string, error) {
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("relative path escapes target home")
	}
	path := filepath.Join(root, relative)
	if !pathContains(root, path) {
		return "", errors.New("relative path escapes target home")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("target path is not a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !pathContains(root, resolved) {
		return "", errors.New("target path contains symbolic links")
	}
	return path, nil
}

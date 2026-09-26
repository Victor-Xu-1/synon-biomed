package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// The logical output path is a view of a receipt-owned immutable snapshot.
// Only missing/empty paths or exact superseded copies may be replaced. A user
// file, foreign link or changed directory stays in place without invalidating
// the snapshot or granting those bytes scientific-output authority.
func linkManagedExecutionSnapshot(ctx context.Context, workspaceRoot, finalRoot string, authority managedExecutionOutputAuthority) error {
	outputRoot := authority.Root
	backupRoot := outputRoot + ".synon-output-backup-" + managedExecutionSnapshotID(authority)
	backupMoved := false
	info, err := os.Lstat(outputRoot)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(outputRoot)
			if resolveErr == nil && filepath.Clean(resolved) == filepath.Clean(finalRoot) {
				return removeManagedExecutionSnapshotBackup(ctx, workspaceRoot, backupRoot, authority)
			}
			logManagedExecutionAliasConflict(authority)
			return nil
		}
		if !info.IsDir() {
			logManagedExecutionAliasConflict(authority)
			return nil
		}
		entries, readErr := os.ReadDir(outputRoot)
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			if err := os.Remove(outputRoot); err != nil {
				return err
			}
		} else {
			matches, matchErr := managedExecutionDirectoryMatchesReceipt(ctx, workspaceRoot, outputRoot, authority)
			if matchErr != nil {
				return matchErr
			}
			if !matches {
				logManagedExecutionAliasConflict(authority)
				return nil
			}
			if _, backupErr := os.Lstat(backupRoot); backupErr == nil {
				return errors.New("managed execution output recovery path is already occupied")
			} else if !errors.Is(backupErr, os.ErrNotExist) {
				return backupErr
			}
			if err := os.Rename(outputRoot, backupRoot); err != nil {
				return fmt.Errorf("reserve managed execution output path: %w", err)
			}
			backupMoved = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	relativeTarget, err := filepath.Rel(filepath.Dir(outputRoot), finalRoot)
	if err == nil {
		err = os.Symlink(relativeTarget, outputRoot)
	}
	if err != nil {
		if backupMoved {
			err = errors.Join(err, os.Rename(backupRoot, outputRoot))
		}
		return fmt.Errorf("publish managed execution output view: %w", err)
	}
	if _, managed, verifyErr := managedExecutionSnapshotRoot(workspaceRoot, outputRoot, authority.PackID, authority.ExecutionID); verifyErr != nil || !managed {
		rollbackErr := os.Remove(outputRoot)
		if backupMoved {
			rollbackErr = errors.Join(rollbackErr, os.Rename(backupRoot, outputRoot))
		}
		return errors.Join(errors.New("managed execution output view did not verify"), verifyErr, rollbackErr)
	}
	return removeManagedExecutionSnapshotBackup(ctx, workspaceRoot, backupRoot, authority)
}

func managedExecutionDirectoryMatchesReceipt(ctx context.Context, workspaceRoot, directory string, authority managedExecutionOutputAuthority) (bool, error) {
	seen := 0
	matches := true
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		logical, err := filepath.Rel(workspaceRoot, filepath.Join(authority.Root, relative))
		if err != nil {
			return err
		}
		expected := authority.Digests[filepath.ToSlash(logical)]
		if expected == "" || entry.Type()&os.ModeSymlink != 0 {
			matches = false
			return nil
		}
		digest, err := digestManagedExecutionOutputFile(ctx, directory, path)
		if err != nil {
			return err
		}
		matches = matches && digest == expected
		seen++
		return nil
	})
	return err == nil && matches && seen == len(authority.Digests) && seen > 0, err
}

func removeManagedExecutionSnapshotBackup(ctx context.Context, workspaceRoot, backupRoot string, authority managedExecutionOutputAuthority) error {
	if _, err := os.Lstat(backupRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	matches, err := managedExecutionDirectoryMatchesReceipt(ctx, workspaceRoot, backupRoot, authority)
	if err != nil {
		return err
	}
	if !matches {
		logManagedExecutionAliasConflict(authority)
		return nil
	}
	if err := os.RemoveAll(backupRoot); err != nil {
		return fmt.Errorf("remove verified superseded managed execution output: %w", err)
	}
	return nil
}

func logManagedExecutionAliasConflict(authority managedExecutionOutputAuthority) {
	log.Printf("managed_execution_output_alias_conflict pack=%q execution=%q action=preserve_conflict_use_verified_snapshot", authority.PackID, authority.ExecutionID)
}

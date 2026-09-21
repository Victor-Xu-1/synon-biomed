package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const managedExecutionSnapshotSchema = "synon.execution-pack-output-snapshot.v1"

type managedExecutionSnapshotManifest struct {
	Schema      string            `json:"schema"`
	PackID      string            `json:"execution_pack_id"`
	ExecutionID string            `json:"execution_id"`
	Digests     map[string]string `json:"digests"`
}

// publishManagedExecutionOutputSnapshot moves a verified output directory into
// the stable read-only materialization root and leaves its old task path as a
// compatibility symlink. The worker already has the stable root mounted, so
// later publications become visible without changing the kernel session spec.
func publishManagedExecutionOutputSnapshot(
	ctx context.Context,
	workspaceRoot string,
	authority managedExecutionOutputAuthority,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	workspaceRoot = filepath.Clean(strings.TrimSpace(workspaceRoot))
	outputRoot := filepath.Clean(strings.TrimSpace(authority.Root))
	if workspaceRoot == "" || outputRoot == "" || !managedExecutionPathWithinRoot(workspaceRoot, outputRoot) || outputRoot == workspaceRoot {
		return errors.New("managed execution snapshot workspace authority is invalid")
	}
	lock := agentWorkspaceEditLock("managed-execution-snapshot:\x00" + outputRoot)
	lock.Lock()
	defer lock.Unlock()
	stableRoot := filepath.Join(workspaceRoot, ".synon-artifacts", ".managed")
	if _, err := secureEnsureAgentWorkspaceDirectory(filepath.Dir(stableRoot), 0o700); err != nil {
		return fmt.Errorf("prepare managed execution snapshot root: %w", err)
	}
	if err := os.MkdirAll(stableRoot, 0o700); err != nil {
		return fmt.Errorf("create managed execution snapshot root: %w", err)
	}
	snapshotID := managedExecutionSnapshotID(authority)
	finalRoot := filepath.Join(stableRoot, snapshotID)
	backupRoot := outputRoot + ".synon-output-backup-" + snapshotID
	if info, err := os.Lstat(outputRoot); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if _, managed, resolveErr := managedExecutionSnapshotRoot(workspaceRoot, outputRoot, authority.PackID, authority.ExecutionID); resolveErr != nil {
			return resolveErr
		} else if managed {
			return removeManagedExecutionSnapshotBackup(backupRoot)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if valid, err := managedExecutionSnapshotManifestAt(finalRoot, authority.PackID, authority.ExecutionID); err != nil {
		return err
	} else if valid {
		return linkManagedExecutionSnapshot(workspaceRoot, outputRoot, finalRoot, backupRoot, authority.PackID, authority.ExecutionID)
	} else if _, err := os.Lstat(finalRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil {
		return errors.New("managed execution snapshot identity already exists")
	}
	stagingRoot := filepath.Join(stableRoot, ".staging-"+snapshotID)
	if valid, err := managedExecutionSnapshotManifestAt(stagingRoot, authority.PackID, authority.ExecutionID); err != nil {
		return err
	} else if valid {
		if err := makeManagedExecutionSnapshotReadOnly(stagingRoot); err != nil {
			return err
		}
		if err := os.Rename(stagingRoot, finalRoot); err != nil {
			return fmt.Errorf("recover managed execution snapshot: %w", err)
		}
		return linkManagedExecutionSnapshot(workspaceRoot, outputRoot, finalRoot, backupRoot, authority.PackID, authority.ExecutionID)
	} else if _, err := os.Lstat(stagingRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil {
		if err := os.RemoveAll(stagingRoot); err != nil {
			return fmt.Errorf("reset managed execution snapshot staging: %w", err)
		}
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return fmt.Errorf("create managed execution snapshot staging: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	paths := make([]string, 0, len(authority.Digests))
	for relative := range authority.Digests {
		paths = append(paths, filepath.FromSlash(relative))
	}
	sort.Strings(paths)
	for _, relative := range paths {
		logicalPath := filepath.Join(workspaceRoot, relative)
		if !managedExecutionPathWithinRoot(outputRoot, logicalPath) || logicalPath == outputRoot {
			return errors.New("managed execution snapshot contains an invalid relative path")
		}
		destinationRelative, err := filepath.Rel(outputRoot, logicalPath)
		if err != nil || destinationRelative == "." || destinationRelative == ".." || strings.HasPrefix(destinationRelative, ".."+string(filepath.Separator)) {
			return errors.New("managed execution snapshot contains an invalid relative path")
		}
		if err := copyManagedExecutionSnapshotFile(ctx, logicalPath, filepath.Join(stagingRoot, destinationRelative), authority.Digests[filepath.ToSlash(relative)]); err != nil {
			return err
		}
	}
	manifest := managedExecutionSnapshotManifest{
		Schema: managedExecutionSnapshotSchema, PackID: authority.PackID,
		ExecutionID: authority.ExecutionID, Digests: authority.Digests,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stagingRoot, ".synon-output-snapshot.json"), encoded, 0o444); err != nil {
		return fmt.Errorf("write managed execution snapshot manifest: %w", err)
	}
	if err := makeManagedExecutionSnapshotReadOnly(stagingRoot); err != nil {
		return err
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		return fmt.Errorf("publish managed execution snapshot: %w", err)
	}
	if err := linkManagedExecutionSnapshot(workspaceRoot, outputRoot, finalRoot, backupRoot, authority.PackID, authority.ExecutionID); err != nil {
		return err
	}
	committed = true
	return nil
}

func managedExecutionSnapshotManifestAt(root, packID, executionID string) (bool, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("managed execution snapshot identity is unsafe")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(root, ".synon-output-snapshot.json"))
	if err != nil {
		return false, err
	}
	var manifest managedExecutionSnapshotManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil ||
		manifest.Schema != managedExecutionSnapshotSchema || manifest.PackID != packID || manifest.ExecutionID != executionID {
		return false, errors.New("managed execution snapshot manifest conflicts with its receipt")
	}
	return true, nil
}

func linkManagedExecutionSnapshot(workspaceRoot, outputRoot, finalRoot, backupRoot, packID, executionID string) error {
	backupMoved := false
	info, err := os.Lstat(outputRoot)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			_, managed, resolveErr := managedExecutionSnapshotRoot(workspaceRoot, outputRoot, packID, executionID)
			if resolveErr != nil {
				return resolveErr
			}
			if managed {
				return removeManagedExecutionSnapshotBackup(backupRoot)
			}
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
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	relativeTarget, err := filepath.Rel(filepath.Dir(outputRoot), finalRoot)
	if err != nil {
		if backupMoved {
			_ = os.Rename(backupRoot, outputRoot)
		}
		return err
	}
	if err := os.Symlink(relativeTarget, outputRoot); err != nil {
		if backupMoved {
			_ = os.Rename(backupRoot, outputRoot)
		}
		return fmt.Errorf("publish managed execution output compatibility path: %w", err)
	}
	if _, managed, err := managedExecutionSnapshotRoot(workspaceRoot, outputRoot, packID, executionID); err != nil || !managed {
		_ = os.Remove(outputRoot)
		if backupMoved {
			_ = os.Rename(backupRoot, outputRoot)
		}
		if err != nil {
			return err
		}
		return errors.New("managed execution snapshot compatibility path did not verify")
	}
	if _, err := os.Lstat(backupRoot); err == nil {
		if err := os.RemoveAll(backupRoot); err != nil {
			return fmt.Errorf("remove superseded managed execution output: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeManagedExecutionSnapshotBackup(backupRoot string) error {
	if _, err := os.Lstat(backupRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if err := os.RemoveAll(backupRoot); err != nil {
		return fmt.Errorf("remove superseded managed execution output: %w", err)
	}
	return nil
}

func managedExecutionSnapshotID(authority managedExecutionOutputAuthority) string {
	digest := sha256.Sum256([]byte(authority.PackID + "\x00" + authority.ExecutionID + "\x00" + authority.Root))
	return hex.EncodeToString(digest[:16])
}

func managedExecutionSnapshotRoot(workspaceRoot, outputRoot, packID, executionID string) (string, bool, error) {
	info, err := os.Lstat(outputRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, errors.New("managed execution output directory is unavailable")
		}
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return outputRoot, false, nil
	}
	resolved, err := filepath.EvalSymlinks(outputRoot)
	if err != nil {
		return "", false, errors.New("managed execution output compatibility path is broken")
	}
	managedRoot := filepath.Join(workspaceRoot, ".synon-artifacts", ".managed")
	if !managedExecutionPathWithinRoot(managedRoot, resolved) || resolved == managedRoot {
		return "", false, errors.New("managed execution output compatibility path escaped the stable root")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(resolved, ".synon-output-snapshot.json"))
	if err != nil {
		return "", false, errors.New("managed execution output snapshot manifest is unavailable")
	}
	var manifest managedExecutionSnapshotManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || manifest.Schema != managedExecutionSnapshotSchema || manifest.PackID != packID || manifest.ExecutionID != executionID {
		return "", false, errors.New("managed execution output snapshot manifest conflicts with its receipt")
	}
	return resolved, true, nil
}

func copyManagedExecutionSnapshotFile(ctx context.Context, source, destination, expectedDigest string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed execution snapshot source is unavailable or unsafe")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".snapshot-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hasher), &contextKernelReader{ctx: ctx, reader: sourceFile}); err != nil {
		return err
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(strings.TrimSpace(expectedDigest)) {
		return errors.New("managed execution snapshot source digest changed")
	}
	if err := temporary.Chmod(0o444); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return nil
}

func makeManagedExecutionSnapshotReadOnly(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed execution snapshot contains a symlink")
		}
		return os.Chmod(path, 0o600)
	})
}

func managedExecutionAuthorityContainsPath(authority managedExecutionOutputAuthority, candidate string) bool {
	if managedExecutionPathWithinRoot(authority.Root, candidate) {
		return true
	}
	return authority.ResolvedRoot != "" && managedExecutionPathWithinRoot(authority.ResolvedRoot, candidate)
}

package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/tools/fileevents"
)

func captureWebFSFileSnapshot(
	ctx context.Context,
	workspaceRoot string,
	baselineRoot string,
) (map[string]webFSSnapshotFile, error) {
	files := make(map[string]webFSSnapshotFile)
	var count int
	var totalBytes int64
	err := filepath.WalkDir(workspaceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return errWebFSCanceled
		}
		if path == workspaceRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		count++
		if count > maxWebFSTreeEntries {
			return errWebFSTooMany
		}
		if info.Size() < 0 || info.Size() > maxWebFSCopyBytes-totalBytes {
			return errWebFSTooLarge
		}
		relative, err := filepath.Rel(workspaceRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		target := filepath.Join(baselineRoot, filepath.FromSlash(relative))
		hash, err := copyWebFSSnapshotBaselineFile(path, target, info.Size())
		if err != nil {
			return err
		}
		files[relative] = webFSSnapshotFile{Size: info.Size(), SHA256: hash}
		totalBytes += info.Size()
		return nil
	})
	return files, err
}

func compareWebFSFileSnapshot(ctx context.Context, snapshot *webFSSnapshot) (webFSSnapshotCompare, error) {
	current, err := scanWebFSFileSnapshot(ctx, snapshot.Workspace)
	if err != nil {
		return webFSSnapshotCompare{}, err
	}
	result := webFSSnapshotCompare{
		Staged: []webFSSnapshotChange{}, Unstaged: []webFSSnapshotChange{},
	}
	for relative, currentFile := range current {
		baseline, found := snapshot.Files[relative]
		operation := ""
		switch {
		case !found:
			operation = "create"
		case baseline.Size != currentFile.Size || baseline.SHA256 != currentFile.SHA256:
			operation = "modify"
		}
		if operation != "" {
			result.Unstaged = append(result.Unstaged, webFSSnapshotChange{
				FilePath:     filepath.Join(snapshot.Workspace, filepath.FromSlash(relative)),
				RelativePath: relative, Operation: operation,
			})
		}
	}
	for relative := range snapshot.Files {
		if _, found := current[relative]; found {
			continue
		}
		result.Unstaged = append(result.Unstaged, webFSSnapshotChange{
			FilePath:     filepath.Join(snapshot.Workspace, filepath.FromSlash(relative)),
			RelativePath: relative, Operation: "delete",
		})
	}
	sortWebFSSnapshotChanges(result.Unstaged)
	return result, nil
}

func scanWebFSFileSnapshot(ctx context.Context, workspaceRoot string) (map[string]webFSSnapshotFile, error) {
	files := make(map[string]webFSSnapshotFile)
	var count int
	var totalBytes int64
	err := filepath.WalkDir(workspaceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return errWebFSCanceled
		}
		if path == workspaceRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		count++
		if count > maxWebFSTreeEntries {
			return errWebFSTooMany
		}
		if info.Size() < 0 || info.Size() > maxWebFSCopyBytes-totalBytes {
			return errWebFSTooLarge
		}
		hash, err := hashWebFSSnapshotFile(path, info.Size())
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(workspaceRoot, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = webFSSnapshotFile{Size: info.Size(), SHA256: hash}
		totalBytes += info.Size()
		return nil
	})
	return files, err
}

func readWebFSFileBaseline(snapshot *webFSSnapshot, filePath string) (*string, error) {
	_, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return nil, err
	}
	if _, found := snapshot.Files[relative]; !found {
		return nil, nil
	}
	baseline, err := resolveWebFSTarget(snapshot.BaselineRoot, filepath.FromSlash(relative), true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := readWebFSFile(baseline, maxWebFSReadBytes)
	if err != nil {
		return nil, err
	}
	content := string(data)
	return &content, nil
}

func resetWebFSFileSnapshot(snapshot *webFSSnapshot, filePath string) error {
	target, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return err
	}
	baseline, found := snapshot.Files[relative]
	if !found {
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errWebFSUnsupported
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		fileevents.NotifyChanged(target)
		return nil
	}
	source, err := resolveWebFSTarget(snapshot.BaselineRoot, filepath.FromSlash(relative), true)
	if err != nil {
		return err
	}
	if err := restoreWebFSSnapshotFile(source, target, baseline.Size); err != nil {
		return err
	}
	fileevents.NotifyChanged(target)
	return nil
}

func copyWebFSSnapshotBaselineFile(source, target string, expected int64) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return zero, err
	}
	input, err := os.Open(source)
	if err != nil {
		return zero, err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return zero, err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(input, expected+1))
	closeErr := output.Close()
	if copyErr != nil {
		return zero, copyErr
	}
	if closeErr != nil {
		return zero, closeErr
	}
	if written != expected {
		return zero, errors.New("snapshot source changed while it was captured")
	}
	var hash [sha256.Size]byte
	copy(hash[:], hasher.Sum(nil))
	return hash, nil
}

func hashWebFSSnapshotFile(path string, expected int64) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, expected+1))
	if err != nil {
		return zero, err
	}
	if written != expected {
		return zero, errors.New("snapshot file changed while it was scanned")
	}
	var hash [sha256.Size]byte
	copy(hash[:], hasher.Sum(nil))
	return hash, nil
}

func restoreWebFSSnapshotFile(source, target string, expected int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".synon-restore-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	input, err := os.Open(source)
	if err != nil {
		_ = temp.Close()
		return err
	}
	written, copyErr := io.Copy(temp, io.LimitReader(input, expected+1))
	closeInputErr := input.Close()
	if copyErr == nil {
		copyErr = closeInputErr
	}
	if copyErr == nil && written != expected {
		copyErr = errors.New("snapshot baseline changed while it was restored")
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	closeOutputErr := temp.Close()
	if copyErr == nil {
		copyErr = closeOutputErr
	}
	if copyErr != nil {
		return copyErr
	}
	return os.Rename(tempPath, target)
}

func sortWebFSSnapshotChanges(changes []webFSSnapshotChange) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].RelativePath != changes[j].RelativePath {
			return changes[i].RelativePath < changes[j].RelativePath
		}
		return strings.Compare(changes[i].Operation, changes[j].Operation) < 0
	})
}

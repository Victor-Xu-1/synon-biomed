package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

type managedExecutionOutputAuthority struct {
	Root         string
	ResolvedRoot string
	PackID       string
	ExecutionID  string
	Digests      map[string]string
}

// managedExecutionOutputAuthorities reconstructs immutable output ownership
// exclusively from host-persisted successful execution receipts. A marker
// written in the model-writable workspace is never authority by itself.
func (s *Server) managedExecutionOutputAuthorities(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	records []workspace.ExecutionLogRecord,
	executionBindings map[string]string,
	excludedPackIDs ...string,
) ([]managedExecutionOutputAuthority, error) {
	if s == nil || s.scienceCapabilities == nil {
		return nil, nil
	}
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return nil, errors.New("managed execution output workspace authority is unavailable")
	}
	seenRoots := map[string]bool{}
	excluded := map[string]bool{}
	for _, packID := range excludedPackIDs {
		excluded[strings.TrimSpace(packID)] = true
	}
	authorities := make([]managedExecutionOutputAuthority, 0)
	for recordIndex := len(records) - 1; recordIndex >= 0; recordIndex-- {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		record := records[recordIndex]
		if !strings.EqualFold(strings.TrimSpace(record.ExitStatus), "ok") ||
			!agentSavedArtifactExecutionAuthorized(record, access, workspaceDir, executionBindings) {
			continue
		}
		for _, capability := range s.scienceCapabilities.Capabilities {
			for _, engine := range capability.AcceptedEngines {
				pack := engine.ExecutionPack
				if pack.Mode != "local" || excluded[pack.ID] ||
					!commandExecutesManagedExecutionPack(pack.Skill, pack, record.Source) {
					continue
				}
				writes, mapErr := managedExecutionReceiptWriteMap(workspaceRoot, record.FilesWritten)
				if mapErr != nil {
					return nil, mapErr
				}
				markerRoots := make([]string, 0)
				for path := range writes {
					if filepath.Base(path) == managedExecutionOutputOwnershipMarker {
						markerRoots = append(markerRoots, filepath.Dir(path))
					}
				}
				sort.Strings(markerRoots)
				for _, outputRoot := range markerRoots {
					if seenRoots[outputRoot] {
						continue
					}
					seenRoots[outputRoot] = true
					authority, verifyErr := s.verifyManagedExecutionOutputAuthority(
						ctx, workspaceRoot, outputRoot, pack.ID, record.ID, writes,
					)
					if verifyErr != nil {
						return nil, verifyErr
					}
					if err := publishManagedExecutionOutputSnapshot(ctx, workspaceRoot, authority); err != nil {
						return nil, err
					}
					if resolvedRoot, managed, resolveErr := managedExecutionSnapshotRoot(
						workspaceRoot, authority.Root, authority.PackID, authority.ExecutionID,
					); resolveErr != nil || !managed {
						return nil, errors.New("managed execution output snapshot did not become readable")
					} else {
						authority.ResolvedRoot = resolvedRoot
					}
					authorities = append(authorities, authority)
				}
			}
		}
	}
	sort.Slice(authorities, func(left, right int) bool { return authorities[left].Root < authorities[right].Root })
	return authorities, nil
}

func managedExecutionReceiptWriteMap(workspaceRoot string, raw any) (map[string]string, error) {
	writes := map[string]string{}
	for _, write := range agentSavedArtifactFileWrites(raw) {
		path := filepath.Clean(strings.TrimSpace(write.Path))
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspaceRoot, path)
		}
		path, err := filepath.Abs(path)
		if err != nil || !managedExecutionPathWithinRoot(workspaceRoot, path) {
			return nil, errors.New("managed execution receipt contains an out-of-scope file")
		}
		digest := strings.ToLower(strings.TrimSpace(write.SHA256))
		if len(digest) != sha256.Size*2 {
			return nil, errors.New("managed execution receipt contains an invalid file digest")
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, errors.New("managed execution receipt contains an invalid file digest")
		}
		if previous, duplicate := writes[path]; duplicate && previous != digest {
			return nil, errors.New("managed execution receipt contains conflicting file digests")
		}
		writes[path] = digest
	}
	return writes, nil
}

func (s *Server) verifyManagedExecutionOutputAuthority(
	ctx context.Context,
	workspaceRoot, outputRoot, packID, executionID string,
	writes map[string]string,
) (managedExecutionOutputAuthority, error) {
	if !managedExecutionPathWithinRoot(workspaceRoot, outputRoot) || outputRoot == workspaceRoot {
		return managedExecutionOutputAuthority{}, errors.New("managed execution output escaped the task workspace")
	}
	info, err := os.Lstat(outputRoot)
	recoveredSnapshot := false
	resolved := outputRoot
	if errors.Is(err, os.ErrNotExist) {
		candidate := filepath.Join(workspaceRoot, ".synon-artifacts", ".managed", managedExecutionSnapshotID(managedExecutionOutputAuthority{
			Root: outputRoot, PackID: packID, ExecutionID: executionID,
		}))
		valid, snapshotErr := managedExecutionSnapshotManifestAt(candidate, packID, executionID)
		if snapshotErr != nil || !valid {
			return managedExecutionOutputAuthority{}, errors.New("managed execution output directory is unavailable or unsafe")
		}
		resolved, recoveredSnapshot = candidate, true
	} else if err != nil || !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return managedExecutionOutputAuthority{}, errors.New("managed execution output directory is unavailable or unsafe")
	} else if info.Mode()&os.ModeSymlink != 0 {
		var managed bool
		resolved, managed, err = managedExecutionSnapshotRoot(workspaceRoot, outputRoot, packID, executionID)
		if err != nil || !managed {
			return managedExecutionOutputAuthority{}, errors.New("managed execution output directory changed through an untrusted symbolic link")
		}
	} else {
		resolved, err = filepath.EvalSymlinks(outputRoot)
		if err != nil || filepath.Clean(resolved) != filepath.Clean(outputRoot) {
			return managedExecutionOutputAuthority{}, errors.New("managed execution output directory changed through a symbolic link")
		}
	}
	ownershipRoot := outputRoot
	if recoveredSnapshot {
		ownershipRoot = resolved
	}
	markerPackID, ownership := inspectManagedExecutionOutputOwnership(ownershipRoot)
	if ownership != managedExecutionOutputOwnershipValid || markerPackID != packID {
		return managedExecutionOutputAuthority{}, errors.New("managed execution output ownership does not match its successful receipt")
	}
	_, engine, found := s.scienceCapabilities.FindExecutionPack(packID)
	if !found || engine.ExecutionPack.Mode != "local" {
		return managedExecutionOutputAuthority{}, errors.New("managed execution output references an unavailable execution pack")
	}
	pathSet := map[string]bool{}
	for path := range writes {
		path = filepath.Clean(path)
		if path != outputRoot && managedExecutionPathWithinRoot(outputRoot, path) {
			pathSet[path] = true
		}
	}
	markerPath := filepath.Join(outputRoot, managedExecutionOutputOwnershipMarker)
	if !pathSet[markerPath] {
		return managedExecutionOutputAuthority{}, errors.New("managed execution ownership marker is missing from its successful file-write receipt")
	}
	for _, output := range engine.ExecutionPack.Outputs {
		lookupRoot := outputRoot
		if recoveredSnapshot {
			lookupRoot = resolved
		}
		relative, found := managedExecutionBundleOutputPath(workspaceRoot, lookupRoot, output.Path)
		if !found {
			return managedExecutionOutputAuthority{}, fmt.Errorf("managed execution output %q is missing", output.Path)
		}
		path := filepath.Join(workspaceRoot, filepath.FromSlash(relative))
		if recoveredSnapshot {
			withinSnapshot, relativeErr := filepath.Rel(resolved, path)
			if relativeErr != nil || withinSnapshot == "." || withinSnapshot == ".." || strings.HasPrefix(withinSnapshot, ".."+string(filepath.Separator)) {
				return managedExecutionOutputAuthority{}, errors.New("managed execution output recovery path is invalid")
			}
			path = filepath.Join(outputRoot, withinSnapshot)
		}
		if !pathSet[path] {
			return managedExecutionOutputAuthority{}, errors.New("managed execution output is missing from its successful file-write receipt")
		}
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	digests := make(map[string]string, len(paths))
	for _, path := range paths {
		receiptDigest := writes[filepath.Clean(path)]
		if receiptDigest == "" {
			return managedExecutionOutputAuthority{}, errors.New("managed execution output is missing from its successful file-write receipt")
		}
		digestPath := path
		if recoveredSnapshot {
			relative, relativeErr := filepath.Rel(outputRoot, path)
			if relativeErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return managedExecutionOutputAuthority{}, errors.New("managed execution output recovery path is invalid")
			}
			digestPath = filepath.Join(resolved, relative)
		}
		currentDigest, err := digestManagedExecutionOutputFile(ctx, resolved, digestPath)
		if err != nil || !strings.EqualFold(currentDigest, receiptDigest) {
			return managedExecutionOutputAuthority{}, fmt.Errorf("managed execution output no longer matches its successful file-write receipt: %s want=%s got=%s err=%v", filepath.ToSlash(path), receiptDigest, currentDigest, err)
		}
		relative, _ := filepath.Rel(workspaceRoot, path)
		digests[filepath.ToSlash(relative)] = currentDigest
	}
	return managedExecutionOutputAuthority{
		Root: outputRoot, ResolvedRoot: resolved, PackID: packID, ExecutionID: executionID, Digests: digests,
	}, nil
}

func digestManagedExecutionOutputFile(ctx context.Context, outputRoot, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return "", errors.New("managed execution output file is unavailable or unsafe")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !managedExecutionPathWithinRoot(outputRoot, resolved) {
		return "", errors.New("managed execution output file changed through a symbolic link")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, &contextKernelReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Server) agentKernelWorkspaceImmutableMounts(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	excludePackID string,
) ([]kernelruntime.WorkerMount, error) {
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return nil, errors.New("kernel immutable workspace authority is unavailable")
	}
	artifactRoot, err := secureEnsureAgentWorkspaceDirectory(filepath.Join(workspaceRoot, ".synon-artifacts"), 0o700)
	if err != nil || filepath.Clean(artifactRoot) != filepath.Join(workspaceRoot, ".synon-artifacts") {
		return nil, errors.New("kernel immutable artifact directory is unsafe")
	}
	paths := []string{artifactRoot}
	if s.workspaceStore != nil {
		records, listErr := s.workspaceStore.ListExecutionLog(access.Frame.ID, "")
		if listErr != nil {
			return nil, errors.New("managed execution output receipts could not be loaded")
		}
		bindings, bindingErr := s.workspaceStore.KernelLocalExecutionBindings(ctx, access)
		if bindingErr != nil {
			return nil, errors.New("managed execution output operation bindings could not be loaded")
		}
		_, authorityErr := s.managedExecutionOutputAuthorities(
			ctx, access, workspaceRoot, records, bindings, excludePackID,
		)
		if authorityErr != nil {
			return nil, authorityErr
		}
	}
	sort.Slice(paths, func(left, right int) bool {
		if len(paths[left]) != len(paths[right]) {
			return len(paths[left]) < len(paths[right])
		}
		return paths[left] < paths[right]
	})
	mounts := make([]kernelruntime.WorkerMount, 0, len(paths))
	for _, path := range paths {
		covered := false
		for _, mount := range mounts {
			if managedExecutionPathWithinRoot(mount.Path, path) {
				covered = true
				break
			}
		}
		if !covered {
			mounts = append(mounts, kernelruntime.TrustedReadOnlyDirectoryMount(path))
		}
	}
	return mounts, nil
}

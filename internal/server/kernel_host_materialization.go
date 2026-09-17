package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/runtimecontrol"
)

var errKernelArtifactContentIntegrity = errors.New("immutable source content verification failed")

// materializeKernelLargeToolResult gives the analysis kernel the same
// immutable-reference boundary used for ordinary project artifacts. Internal
// large-result evidence remains hidden from project listings and is scoped to
// its owning project; only its exact ltr-* version can become a read-only file.
func (s *Server) materializeKernelLargeToolResult(
	ctx context.Context,
	workspaceDir string,
	access workspace.KernelFrameAccess,
	requestedID string,
) (string, error) {
	versionID := strings.TrimSpace(requestedID)
	if workspace.IsRunnerLargeToolResultArtifactID(versionID) {
		record, found, err := s.workspaceStore.GetRunnerLargeToolResult(ctx, versionID, access.UserID)
		if err != nil {
			return "", classifyKernelHostError(err)
		}
		if !found || record.ProjectID != access.Frame.ProjectID {
			return "", kernelruntime.NewHostCallError("not_found", "host.artifact_path: artifact version not found")
		}
		versionID = record.VersionID
	}
	record, reader, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(ctx, versionID, access.UserID)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if !found || record.ProjectID != access.Frame.ProjectID {
		if reader != nil {
			_ = reader.Close()
		}
		return "", kernelruntime.NewHostCallError("not_found", "host.artifact_path: artifact version not found")
	}
	defer reader.Close()
	receiptRoot, err := s.kernelMaterializationReceiptRoot()
	if err != nil {
		return "", err
	}
	return materializeKernelImmutableContent(
		ctx, workspaceDir, receiptRoot, record.VersionID,
		runnerLargeToolResultFilename(record.ToolName, record.ToolCallID),
		record.SizeBytes, record.ContentSHA256, reader,
	)
}

func materializeKernelImmutableContent(
	ctx context.Context,
	workspaceDir, receiptRoot, versionID, filename string,
	sizeBytes int64,
	contentSHA256 string,
	reader workspace.ArtifactContentReader,
) (string, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if ctx == nil || workspaceDir == "" || !filepath.IsAbs(workspaceDir) || sizeBytes < 0 ||
		versionID == "" || versionID != strings.TrimSpace(versionID) || versionID == "." || versionID == ".." || strings.ContainsAny(versionID, "/\\\x00") {
		return "", kernelruntime.NewHostCallError("unavailable", "host.artifact_path: kernel workspace is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	if _, err := secureEnsureAgentWorkspaceDirectory(workspaceDir, 0o700); err != nil {
		return "", classifyKernelHostError(fmt.Errorf("create kernel artifact directory: %w", err))
	}
	name := filepath.Base(strings.ReplaceAll(strings.ReplaceAll(filename, "\x00", ""), "\\", "/"))
	if name == "." || name == "" {
		name = "artifact"
	}
	relative := filepath.Join(".synon-artifacts", versionID+"-"+name)
	destination := filepath.Join(workspaceDir, relative)
	receiptPath := kernelMaterializationReceiptPath(receiptRoot, workspaceDir, versionID)
	lock := agentWorkspaceEditLock(workspaceDir + "\x00" + relative)
	lock.Lock()
	defer lock.Unlock()
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	// Reuse the workspace's descriptor-relative, no-symlink/no-hardlink file
	// authority. A pathname check followed by os.Open/os.Rename races with
	// changes to the cache directory and is not a containment boundary.
	authority, err := openAgentWorkspaceEditAuthority(workspaceDir, relative, true)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	defer authority.close()
	current, currentMode, found, err := authority.openCurrent()
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if found {
		defer current.Close()
		info, err := current.Stat()
		if err != nil {
			return "", classifyKernelHostError(err)
		}
		matches := contentSHA256 != "" && kernelMaterializationReceiptMatches(receiptPath, destination, versionID, name, sizeBytes, contentSHA256, info)
		if !matches {
			matches, err = kernelMaterializedContentMatches(ctx, current, sizeBytes, contentSHA256)
		}
		if err != nil {
			return "", classifyKernelHostError(err)
		}
		if matches {
			if currentMode.Perm() != 0o400 {
				if err := current.Chmod(0o400); err != nil {
					return "", classifyKernelHostError(err)
				}
			}
			if err := writeKernelMaterializationReceipt(receiptPath, destination, versionID, name, sizeBytes, contentSHA256); err != nil {
				return "", classifyKernelHostError(err)
			}
			return verifiedKernelMaterializedPath(authority, destination)
		}
	}
	if available := runtimecontrol.AvailableBytes(filepath.Dir(destination)); available != nil {
		required := uint64(sizeBytes)
		if required > *available || *available-required < 64<<20 {
			return "", kernelruntime.NewHostCallError("storage_error", "host.artifact_path: insufficient workspace capacity for lazy materialization")
		}
	}
	staging, err := authority.createStaging(0o600)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	defer staging.discard(authority)
	hasher := sha256.New()
	source := &contextKernelReader{ctx: ctx, reader: reader}
	written, err := io.Copy(io.MultiWriter(staging.file, hasher), io.LimitReader(source, sizeBytes))
	if err != nil {
		return "", classifyKernelHostError(fmt.Errorf("materialize artifact: %w", err))
	}
	var extra [1]byte
	count, endErr := source.Read(extra[:])
	if written != sizeBytes || count != 0 || !errors.Is(endErr, io.EOF) ||
		contentSHA256 != "" && hex.EncodeToString(hasher.Sum(nil)) != contentSHA256 {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		return "", errors.Join(errKernelArtifactContentIntegrity, kernelruntime.NewHostCallError("storage_error", "host.artifact_path: artifact content verification failed"))
	}
	if err := staging.file.Chmod(0o400); err != nil {
		return "", classifyKernelHostError(err)
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	// Only this managed, reproducible copy is replaced. The original artifact
	// and all user-authored files remain untouched if verification fails.
	if err := authority.commit(staging); err != nil {
		return "", classifyKernelHostError(err)
	}
	if err := writeKernelMaterializationReceipt(receiptPath, destination, versionID, name, sizeBytes, contentSHA256); err != nil {
		return "", classifyKernelHostError(err)
	}
	path, err := verifiedKernelMaterializedPath(authority, destination)
	if err == nil && found {
		workspaceDigest := sha256.Sum256([]byte(workspaceDir))
		log.Printf("kernel_artifact_cache_rebuilt version_id=%q workspace_fingerprint=%s", versionID, hex.EncodeToString(workspaceDigest[:8]))
	}
	return path, err
}

func kernelMaterializedContentMatches(ctx context.Context, file *os.File, sizeBytes int64, contentSHA256 string) (bool, error) {
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != sizeBytes {
		return false, nil
	}
	if contentSHA256 == "" {
		// Size alone cannot verify a cached copy. Rebuild from the source when
		// an older artifact has no recorded digest.
		return false, nil
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, &contextKernelReader{ctx: ctx, reader: file}); err != nil {
		return false, err
	}
	return hex.EncodeToString(hasher.Sum(nil)) == contentSHA256, nil
}

func verifiedKernelMaterializedPath(authority *agentWorkspaceEditAuthority, expected string) (string, error) {
	actual, err := authority.actualPath()
	if err != nil || filepath.Clean(actual) != filepath.Clean(expected) {
		return "", kernelruntime.NewHostCallError("storage_error", "host.artifact_path: kernel artifact directory changed")
	}
	return actual, nil
}

type contextKernelReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextKernelReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

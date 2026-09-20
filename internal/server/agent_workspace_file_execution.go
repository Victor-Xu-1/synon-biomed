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
	"strings"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/fileevents"
)

func (s *Server) executeAgentWorkspaceFileTool(
	ctx context.Context,
	identity *agentKernelContext,
	name string,
	input map[string]any,
) (any, error) {
	if err := validateAgentWorkspaceFileToolInput(name, input); err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errors.New("workspace file authority is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		if contextErr := context.Cause(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, errors.New("workspace file authority could not be verified")
	}
	switch name {
	case "read_file":
		return s.executeAgentWorkspaceReadFile(ctx, identity, access.UserID, access.Frame.ProjectID, input)
	case "edit_file":
		if preflight := agentWorkspaceArtifactSelfReferencePreflight(input); preflight != nil {
			return preflight, nil
		}
		return s.executeAgentWorkspaceEditFile(ctx, identity, access, input)
	default:
		return nil, errors.New("workspace file tool is unavailable")
	}
}

func (s *Server) executeAgentWorkspaceReadFile(
	ctx context.Context,
	identity *agentKernelContext,
	userID string,
	projectID string,
	input map[string]any,
) (any, error) {
	if conditionID := strings.TrimSpace(stringValue(input["recovery_condition_id"])); conditionID != "" {
		return s.readRunnerCorrectionCondition(ctx, identity, userID, projectID, conditionID, input)
	}
	if filePath := strings.TrimSpace(stringValue(input["file_path"])); filePath != "" {
		target, err := s.resolveAgentWorkspaceFileTarget(identity, userID, filePath, false)
		if err != nil {
			return nil, err
		}
		file, err := openAgentWorkspaceRegularFile(target.root, target.relativePath)
		if err != nil {
			// Distinguish a missing task-scoped path, which the model can repair,
			// from hard-link and other security failures, which remain fail-closed.
			if _, statErr := os.Lstat(target.path); os.IsNotExist(statErr) {
				return nil, fmt.Errorf("read_file file does not exist in task workspace: %s", target.relativePath)
			}
			return nil, errors.New("read_file could not open the requested file")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, errors.New("read_file could not inspect the requested file")
		}
		return readAgentWorkspaceFileWithLocation(ctx, file, filepath.Base(target.path), "", info.Size(), input,
			map[string]any{"file_path": target.path, "file_path_scope": "original_source"})
	}
	versionID := strings.TrimSpace(stringValue(input["version_id"]))
	version, found, err := s.openAgentWorkspaceReadableVersion(ctx, userID, projectID, versionID)
	if err != nil {
		return nil, errors.New("read_file could not open the requested artifact version")
	}
	if !found {
		var recovery map[string]any
		version, recovery, err = s.resolveAgentWorkspaceArtifactHandle(ctx, userID, projectID, versionID)
		if err != nil {
			return nil, err
		}
		if recovery != nil {
			return recovery, nil
		}
		input = copyMapAny(input)
		input["version_id"] = version.versionID
	}
	defer version.reader.Close()
	return s.readAgentWorkspaceVersionWithLocation(ctx, identity, version, input)
}

func (s *Server) openAgentWorkspaceReadableVersion(
	ctx context.Context,
	userID string,
	projectID string,
	versionID string,
) (agentWorkspaceReadableVersion, bool, error) {
	if workspace.IsRunnerLargeToolResultArtifactID(versionID) {
		record, found, err := s.workspaceStore.GetRunnerLargeToolResult(ctx, versionID, userID)
		if err != nil || !found {
			return agentWorkspaceReadableVersion{}, found, err
		}
		if record.ProjectID != projectID {
			return agentWorkspaceReadableVersion{}, false, nil
		}
		record, reader, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(ctx, record.VersionID, userID)
		if err != nil || !found {
			return agentWorkspaceReadableVersion{}, found, err
		}
		return agentWorkspaceReadableVersion{
			reader: reader, filename: runnerLargeToolResultFilename(record.ToolName, record.ToolCallID),
			contentType: record.ContentType, sizeBytes: record.SizeBytes,
			versionID: record.VersionID, sha256: record.ContentSHA256,
		}, true, nil
	}
	if workspace.IsRunnerLargeToolResultVersionID(versionID) {
		record, reader, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(ctx, versionID, userID)
		if err != nil || !found {
			return agentWorkspaceReadableVersion{}, found, err
		}
		if record.ProjectID != projectID {
			_ = reader.Close()
			return agentWorkspaceReadableVersion{}, false, nil
		}
		return agentWorkspaceReadableVersion{
			reader: reader, filename: runnerLargeToolResultFilename(record.ToolName, record.ToolCallID),
			contentType: record.ContentType, sizeBytes: record.SizeBytes,
			versionID: record.VersionID, sha256: record.ContentSHA256,
		}, true, nil
	}

	artifact, version, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
	if err != nil || !found {
		return agentWorkspaceReadableVersion{}, found, err
	}
	if artifact.ProjectID != projectID {
		_ = reader.Close()
		return agentWorkspaceReadableVersion{}, false, nil
	}
	return agentWorkspaceReadableVersion{
		reader: reader, filename: artifact.Name, contentType: artifact.Kind, sizeBytes: version.SizeBytes,
		versionID: version.ID, sha256: version.ContentSHA256,
	}, true, nil
}

func (s *Server) executeAgentWorkspaceEditFile(
	ctx context.Context,
	identity *agentKernelContext,
	access workspace.KernelFrameAccess,
	input map[string]any,
) (result any, returnErr error) {
	target, err := s.resolveAgentWorkspaceFileTarget(identity, access.UserID, strings.TrimSpace(stringValue(input["file_path"])), true)
	if err != nil {
		return nil, err
	}
	lock := agentWorkspaceEditLock(target.root + "\x00" + target.relativePath)
	lock.Lock()
	defer lock.Unlock()

	authority, err := openAgentWorkspaceEditAuthority(target.root, target.relativePath, target.createParents)
	if err != nil {
		return nil, errors.New("edit_file could not authorize the requested file")
	}
	defer authority.close()
	// Include the resulting file view for both fresh and replayed successes.
	// The write lock and path authority remain held until the view is captured.
	defer func() {
		if returnErr == nil {
			if receipt, ok := result.(map[string]any); ok {
				attachAgentWorkspaceEditView(ctx, authority, receipt)
			}
		}
	}()

	oldString := input["old_string"].(string)
	newString := input["new_string"].(string)
	requestFingerprint := agentWorkspaceEditRequestFingerprint(access, target, oldString, newString)
	if replay, found, replayErr := s.replayAgentWorkspaceEditExecution(ctx, authority, access, target, requestFingerprint); replayErr != nil {
		return nil, replayErr
	} else if found {
		return replay, nil
	}
	executionID, tracked := agentWorkspaceEditExecutionID(ctx, access)
	var persistedReceipt *workspace.AgentFileEditReceipt
	if tracked && s != nil && s.workspaceStore != nil {
		receipt, found, receiptErr := s.workspaceStore.GetAgentFileEditReceipt(
			ctx, access.UserID, executionID, requestFingerprint,
		)
		if receiptErr != nil {
			return nil, errors.New("edit_file could not verify prior mutation state")
		}
		if found {
			currentDigest, currentFound, digestErr := agentWorkspaceCurrentDigestState(ctx, authority)
			if digestErr != nil {
				return nil, errors.New("edit_file could not verify prior mutation state")
			}
			if currentFound && currentDigest == receipt.FinalSHA256 {
				completion, buildErr := s.agentWorkspaceEditReceiptInput(
					identity, access, target, requestFingerprint, receipt,
				)
				if buildErr != nil {
					return nil, errors.New("edit_file could not rebuild pending provenance")
				}
				if _, _, completeErr := retryAgentWorkspaceEditReceiptCompletion(ctx, func() (workspace.ExecutionLogRecord, workspace.AgentFileEditReceipt, error) {
					return s.workspaceStore.CompleteAgentFileEditReceipt(ctx, completion)
				}); completeErr != nil {
					return nil, errors.New("edit_file result is durable but provenance remains pending; retry the same call")
				}
				if actualPath, pathErr := authority.actualPath(); pathErr == nil {
					fileevents.NotifyChanged(actualPath)
				}
				return agentWorkspaceEditResult(receipt), nil
			}
			matchesOriginal := receipt.OriginalSHA256 == "absent" && !currentFound
			matchesOriginal = matchesOriginal || currentFound && currentDigest == receipt.OriginalSHA256
			if !matchesOriginal || receipt.State != "prepared" {
				return nil, errors.New("edit_file prior mutation state conflicts with the workspace file")
			}
			persistedReceipt = &receipt
		}
	}
	current, mode, found, err := authority.openCurrent()
	if err != nil {
		return nil, errors.New("edit_file could not inspect the requested file")
	}
	created := !found
	if oldString != "" && !found {
		return nil, errors.New("edit_file old_string was not found")
	}
	if oldString != "" {
		oldString, err = normalizeAgentWorkspaceQuotedOldString(ctx, current, oldString)
		if err != nil {
			_ = current.Close()
			return nil, errors.New("edit_file could not inspect the requested file")
		}
	}
	if mode == 0 {
		mode = 0o600
	}
	staging, err := authority.createStaging(mode)
	if err != nil {
		if current != nil {
			_ = current.Close()
		}
		return nil, errors.New("edit_file could not prepare the requested file")
	}
	defer staging.discard(authority)

	var bytesWritten int64
	originalSHA256 := "absent"
	var originalDigest [sha256.Size]byte
	finalHasher := sha256.New()
	stagingWriter := io.MultiWriter(staging.file, finalHasher)
	if oldString == "" {
		if current != nil {
			originalSHA256, err = digestAgentWorkspaceReader(ctx, current)
			closeErr := current.Close()
			if err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if err == nil {
			bytesWritten, err = writeAgentWorkspaceBytes(ctx, stagingWriter, []byte(newString))
		}
	} else {
		bytesWritten, originalDigest, err = streamAgentWorkspaceReplacement(
			ctx, current, stagingWriter, []byte(oldString), []byte(newString),
		)
		closeErr := current.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return nil, err
	}
	if oldString != "" {
		originalSHA256 = hex.EncodeToString(originalDigest[:])
	}
	finalDigest := hex.EncodeToString(finalHasher.Sum(nil))
	if err := validateAgentFileContentType(target.displayPath, staging.file); err != nil {
		return nil, err
	}
	if err := validateAgentSavedArtifactStructure(target.displayPath, staging.file); err != nil {
		return nil, fmt.Errorf("%w: %v", errAgentFileStructureInvalid, err)
	}
	receipt := workspace.AgentFileEditReceipt{
		ExecutionID: executionID, FrameID: access.Frame.ID, DisplayPath: target.displayPath,
		OriginalSHA256: originalSHA256, FinalSHA256: finalDigest,
		Created: created, BytesWritten: bytesWritten,
	}
	if tracked && s != nil && s.workspaceStore != nil {
		if persistedReceipt != nil && !sameAgentWorkspaceEditReceipt(*persistedReceipt, receipt) {
			return nil, errors.New("edit_file pending mutation conflicts with the requested edit")
		}
		prepareInput, buildErr := s.agentWorkspaceEditReceiptInput(identity, access, target, requestFingerprint, receipt)
		if buildErr != nil {
			return nil, errors.New("edit_file could not prepare durable provenance")
		}
		prepared, _, prepareErr := retryAgentWorkspaceEditReceiptPreparation(ctx, func() (workspace.AgentFileEditReceipt, bool, error) {
			return s.workspaceStore.PrepareAgentFileEditReceipt(ctx, prepareInput)
		})
		if prepareErr != nil || !sameAgentWorkspaceEditReceipt(prepared, receipt) {
			return nil, errors.New("edit_file could not prepare durable provenance")
		}
		receipt = prepared
	}
	if oldString != "" {
		unchanged, verifyErr := verifyAgentWorkspaceCurrentDigest(ctx, authority, originalDigest)
		if verifyErr != nil {
			return nil, errors.New("edit_file could not verify the requested file")
		}
		if !unchanged {
			return nil, errors.New("edit_file target changed during the edit")
		}
	} else {
		currentDigest, currentFound, verifyErr := agentWorkspaceCurrentDigestState(ctx, authority)
		if verifyErr != nil {
			return nil, errors.New("edit_file could not verify the requested file")
		}
		matchesOriginal := originalSHA256 == "absent" && !currentFound
		matchesOriginal = matchesOriginal || currentFound && currentDigest == originalSHA256
		if !matchesOriginal {
			return nil, errors.New("edit_file target changed during the edit")
		}
	}
	if err := authority.commit(staging); err != nil {
		return nil, errors.New("edit_file could not persist the requested file")
	}
	actualPath, err := authority.actualPath()
	if err != nil {
		return nil, errors.New("edit_file persisted the file but could not resolve its final path")
	}
	fileevents.NotifyChanged(actualPath)
	durableDigest, digestErr := digestAgentWorkspaceCurrent(ctx, authority)
	if digestErr != nil {
		return nil, errors.New("edit_file persisted the file but could not verify its final digest")
	}
	if durableDigest != finalDigest {
		return nil, errors.New("edit_file persisted file digest does not match the prepared mutation")
	}
	if tracked && s != nil && s.workspaceStore != nil {
		completion, buildErr := s.agentWorkspaceEditReceiptInput(identity, access, target, requestFingerprint, receipt)
		if buildErr != nil {
			return nil, errors.New("edit_file result is durable but provenance remains pending; retry the same call")
		}
		if _, _, completeErr := retryAgentWorkspaceEditReceiptCompletion(ctx, func() (workspace.ExecutionLogRecord, workspace.AgentFileEditReceipt, error) {
			return s.workspaceStore.CompleteAgentFileEditReceipt(ctx, completion)
		}); completeErr != nil {
			return nil, errors.New("edit_file result is durable but provenance remains pending; retry the same call")
		}
	}
	return agentWorkspaceEditResult(receipt), nil
}

func (s *Server) resolveAgentWorkspaceFileTarget(
	identity *agentKernelContext,
	userID string,
	requested string,
	write bool,
) (agentWorkspaceFileTarget, error) {
	workspaceRoot, err := s.canonicalAgentWorkspaceRoot(identity)
	if err != nil {
		return agentWorkspaceFileTarget{}, err
	}
	if write {
		workspaceRoot, err = s.ensureAgentWorkspaceRoot(identity)
		if err != nil {
			return agentWorkspaceFileTarget{}, err
		}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return agentWorkspaceFileTarget{}, errors.New("workspace file path is required")
	}
	absolute := filepath.IsAbs(requested)
	requestedPath := requested
	if !absolute {
		requestedPath, err = filepath.Abs(filepath.Join(workspaceRoot, requested))
	} else {
		requestedPath, err = filepath.Abs(requested)
	}
	if err != nil {
		return agentWorkspaceFileTarget{}, errors.New("workspace file path is invalid")
	}

	target := agentWorkspaceFileTarget{root: workspaceRoot, createParents: !absolute || hostPathWithin(workspaceRoot, requestedPath), requestedAbsolute: absolute}
	if !hostPathWithin(workspaceRoot, requestedPath) {
		if s == nil || s.settingsStore == nil {
			return agentWorkspaceFileTarget{}, errors.New("workspace file path is outside the authorized workspace")
		}
		grants, loadErr := s.loadHostGrants(userID)
		if loadErr != nil {
			return agentWorkspaceFileTarget{}, errors.New("workspace file authorization could not be verified")
		}
		target = agentWorkspaceFileTarget{requestedAbsolute: true}
		bestRoot := ""
		for _, grant := range grants {
			if write && grant.Mode != "read_write" {
				continue
			}
			if !write && grant.Mode != "read" && grant.Mode != "read_write" {
				continue
			}
			root, resolveErr := canonicalHostDirectory(grant.Path)
			if resolveErr != nil {
				return agentWorkspaceFileTarget{}, errors.New("workspace file authorization could not be verified")
			}
			if hostPathWithin(root, requestedPath) && len(root) > len(bestRoot) {
				bestRoot = root
			}
		}
		if bestRoot == "" {
			return agentWorkspaceFileTarget{}, errors.New("workspace file path is outside the authorized workspace")
		}
		target.root = bestRoot
	}
	relative, relErr := filepath.Rel(target.root, requestedPath)
	if relErr != nil {
		return agentWorkspaceFileTarget{}, errors.New("workspace file path is invalid")
	}
	parts, partErr := secureAgentWorkspacePathParts(relative)
	if partErr != nil {
		return agentWorkspaceFileTarget{}, errors.New("workspace file path is invalid")
	}
	target.relativePath = filepath.Join(parts...)
	target.path = filepath.Join(target.root, target.relativePath)
	if write && protectedAgentWorkspaceEditPath(target.path) {
		return agentWorkspaceFileTarget{}, errors.New("edit_file cannot modify protected configuration")
	}
	if target.requestedAbsolute {
		target.displayPath = filepath.ToSlash(target.path)
	} else {
		relative, relErr := filepath.Rel(workspaceRoot, target.path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			return agentWorkspaceFileTarget{}, errors.New("workspace file path is invalid")
		}
		target.displayPath = filepath.ToSlash(relative)
	}
	return target, nil
}

func agentWorkspaceAbsolutePathParts(path string) []string {
	parts := strings.FieldsFunc(filepath.ToSlash(filepath.Clean(path)), func(r rune) bool { return r == '/' })
	if volume := filepath.VolumeName(path); volume != "" && len(parts) > 0 && strings.EqualFold(parts[0], strings.TrimSuffix(volume, string(filepath.Separator))) {
		parts = parts[1:]
	}
	return parts
}

func (s *Server) canonicalAgentWorkspaceRoot(identity *agentKernelContext) (string, error) {
	if identity == nil || strings.TrimSpace(identity.workspaceDir) == "" || !filepath.IsAbs(identity.workspaceDir) {
		return "", errors.New("workspace file authority is unavailable")
	}
	target, err := canonicalAgentWorkspaceDirectoryTarget(identity.workspaceDir)
	if err != nil {
		return "", errors.New("workspace file authority is unavailable")
	}
	protected, err := s.agentKernelProtectedPaths()
	if err != nil {
		return "", errors.New("workspace file protected paths could not be verified")
	}
	for _, path := range protected {
		if hostPathWithin(path, target) || hostPathWithin(target, path) {
			return "", errors.New("workspace file path overlaps protected application data")
		}
	}
	return target, nil
}

func (s *Server) ensureAgentWorkspaceRoot(identity *agentKernelContext) (string, error) {
	target, err := s.canonicalAgentWorkspaceRoot(identity)
	if err != nil {
		return "", err
	}
	root, err := secureEnsureAgentWorkspaceDirectory(target, 0o700)
	if err != nil || filepath.Clean(root) != filepath.Clean(target) {
		return "", errors.New("workspace file authority is unavailable")
	}
	return root, nil
}

func canonicalOrCreateAgentWorkspaceDirectory(path string, mode os.FileMode) (string, error) {
	target, err := canonicalAgentWorkspaceDirectoryTarget(path)
	if err != nil {
		return "", err
	}
	return secureEnsureAgentWorkspaceDirectory(target, mode)
}

func canonicalAgentWorkspaceDirectoryTarget(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(absolute) {
		return "", errors.New("workspace directory path is invalid")
	}
	probe := filepath.Clean(absolute)
	suffix := []string{}
	for {
		if _, statErr := os.Stat(probe); statErr == nil {
			ancestor, resolveErr := canonicalHostDirectory(probe)
			if resolveErr != nil {
				return "", resolveErr
			}
			parts := append([]string{ancestor}, suffix...)
			return filepath.Join(parts...), nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", errors.New("workspace directory has no existing ancestor")
		}
		suffix = append([]string{filepath.Base(probe)}, suffix...)
		probe = parent
	}
}

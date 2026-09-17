package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func retryAgentWorkspaceEditReceiptPreparation(
	ctx context.Context,
	prepare func() (workspace.AgentFileEditReceipt, bool, error),
) (workspace.AgentFileEditReceipt, bool, error) {
	var receipt workspace.AgentFileEditReceipt
	var created bool
	err := retryTransientStoreContention(ctx, "prepare_agent_file_edit_provenance", func() error {
		var err error
		receipt, created, err = prepare()
		return err
	})
	return receipt, created, err
}

func retryAgentWorkspaceEditReceiptCompletion(
	ctx context.Context,
	complete func() (workspace.ExecutionLogRecord, workspace.AgentFileEditReceipt, error),
) (workspace.ExecutionLogRecord, workspace.AgentFileEditReceipt, error) {
	var record workspace.ExecutionLogRecord
	var receipt workspace.AgentFileEditReceipt
	err := retryTransientStoreContention(ctx, "complete_agent_file_edit_provenance", func() error {
		var err error
		record, receipt, err = complete()
		return err
	})
	return record, receipt, err
}

func firstAgentWorkspaceError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func agentWorkspaceEditRequestFingerprint(
	access workspace.KernelFrameAccess,
	target agentWorkspaceFileTarget,
	oldString string,
	newString string,
) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"agent-workspace-edit-v1",
		access.UserID,
		access.Frame.ProjectID,
		access.Frame.ID,
		access.Frame.IncarnationID,
		access.RootFrameIncarnationID,
		target.root,
		target.relativePath,
		oldString,
		newString,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func agentWorkspaceEditExecutionID(ctx context.Context, access workspace.KernelFrameAccess) (string, bool) {
	run, ok := transcriptArtifactRunFromContext(ctx)
	if !ok || strings.TrimSpace(run.ToolCallID) == "" || run.Authority == nil {
		return "", false
	}
	digest := strings.Join([]string{
		"agent-workspace-edit-execution-v1",
		run.Authority.Stream.UID,
		strconv.FormatInt(run.SourceEventID, 10),
		strings.TrimSpace(run.ToolCallID),
		access.Frame.ID,
		access.Frame.IncarnationID,
	}, "\x00")
	return "edit-file-" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(digest)).String(), true
}

func (s *Server) replayAgentWorkspaceEditExecution(
	ctx context.Context,
	authority *agentWorkspaceEditAuthority,
	access workspace.KernelFrameAccess,
	target agentWorkspaceFileTarget,
	requestFingerprint string,
) (map[string]any, bool, error) {
	executionID, tracked := agentWorkspaceEditExecutionID(ctx, access)
	if !tracked || s == nil || s.workspaceStore == nil {
		return nil, false, nil
	}
	record, found, err := s.workspaceStore.GetExecutionLog(access.Frame.ID, executionID)
	if err != nil {
		return nil, false, errors.New("edit_file could not verify prior execution state")
	}
	if !found {
		return nil, false, nil
	}
	detection, ok := record.Detection.(map[string]any)
	if !ok || stringValue(detection["request_sha256"]) != requestFingerprint ||
		stringValue(detection["display_path"]) != target.displayPath {
		return nil, false, errors.New("edit_file idempotency record conflicts with the requested edit")
	}
	expectedDigest, ok := agentWorkspaceExecutionFileDigest(record.FilesWritten, target.displayPath)
	if !ok {
		return nil, false, errors.New("edit_file prior execution is missing its file digest")
	}
	currentDigest, err := digestAgentWorkspaceCurrent(ctx, authority)
	if err != nil || currentDigest != expectedDigest {
		return nil, false, errors.New("edit_file prior result no longer matches the workspace file")
	}
	created, ok := detection["created"].(bool)
	if !ok {
		return nil, false, errors.New("edit_file prior execution metadata is invalid")
	}
	bytesWritten := int(numberValue(detection["bytes_written"]))
	if bytesWritten < 0 {
		return nil, false, errors.New("edit_file prior execution metadata is invalid")
	}
	return map[string]any{
		"success": true, "created": created, "file_path": target.displayPath, "bytes_written": bytesWritten,
	}, true, nil
}

func agentWorkspaceExecutionFileDigest(value any, path string) (string, bool) {
	entries, ok := value.([]any)
	if !ok || len(entries) != 1 {
		return "", false
	}
	entry, ok := entries[0].(map[string]any)
	if !ok || stringValue(entry["path"]) != path {
		return "", false
	}
	digest := strings.ToLower(strings.TrimSpace(stringValue(entry["sha256"])))
	if len(digest) != sha256.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", false
	}
	return digest, true
}

func (s *Server) agentWorkspaceEditReceiptInput(
	identity *agentKernelContext,
	access workspace.KernelFrameAccess,
	target agentWorkspaceFileTarget,
	requestFingerprint string,
	receipt workspace.AgentFileEditReceipt,
) (workspace.AgentFileEditReceiptInput, error) {
	workspaceRoot, err := s.canonicalAgentWorkspaceRoot(identity)
	if err != nil {
		return workspace.AgentFileEditReceiptInput{}, err
	}
	kernelID, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: "host_tool", Language: "diff", Environment: "workspace", WorkspaceDir: workspaceRoot,
	})
	if err != nil {
		return workspace.AgentFileEditReceiptInput{}, err
	}
	return workspace.AgentFileEditReceiptInput{
		OwnerID: access.UserID, IdempotencyKey: receipt.ExecutionID, RequestSHA256: requestFingerprint,
		Receipt: receipt,
		ExecutionLogInput: workspace.SaveExecutionLogInput{
			Record: workspace.ExecutionLogRecord{
				ID: receipt.ExecutionID, FrameID: access.Frame.ID, KernelID: kernelID,
				KernelKind: "host_tool", CondaEnv: "workspace", Language: "diff",
				Source:     "edit_file " + target.displayPath + " request_sha256=" + requestFingerprint,
				ExitStatus: "ok", Origin: "agent",
				FilesWritten: []kernelruntime.FileWrite{{Path: target.displayPath, SHA256: receipt.FinalSHA256}},
				Detection: map[string]any{
					"request_sha256": requestFingerprint,
					"display_path":   target.displayPath,
					"created":        receipt.Created,
					"bytes_written":  receipt.BytesWritten,
				},
			},
			ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
			ExpectedFrameIncarnationID:     access.Frame.IncarnationID,
			ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
		},
	}, nil
}

func sameAgentWorkspaceEditReceipt(left, right workspace.AgentFileEditReceipt) bool {
	return left.ExecutionID == right.ExecutionID && left.FrameID == right.FrameID &&
		left.DisplayPath == right.DisplayPath && left.OriginalSHA256 == right.OriginalSHA256 &&
		left.FinalSHA256 == right.FinalSHA256 && left.Created == right.Created &&
		left.BytesWritten == right.BytesWritten
}

func agentWorkspaceEditResult(receipt workspace.AgentFileEditReceipt) map[string]any {
	return map[string]any{
		"success": true, "created": receipt.Created, "file_path": receipt.DisplayPath,
		"bytes_written": int(receipt.BytesWritten),
		"changed":       receipt.Created || !strings.EqualFold(receipt.OriginalSHA256, receipt.FinalSHA256),
	}
}

func agentWorkspaceEditLock(path string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(filepath.Clean(path)))
	return &agentWorkspaceEditLocks[int(hash.Sum32())%len(agentWorkspaceEditLocks)]
}

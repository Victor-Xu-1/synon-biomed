package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// reuseCompletedAgentPublicScientificFileDownload makes a logically repeated
// download idempotent across runner segments, process restarts, and new tool
// call IDs. The completed transcript receipt, immutable artifact version, and
// workspace checksum must all agree; otherwise the normal download/conflict
// path remains authoritative. Reuse still appends a consumed association for
// the current source event so causal lineage is never borrowed silently.
func (s *Server) reuseCompletedAgentPublicScientificFileDownload(
	ctx context.Context,
	workspaceDir string,
	stream transcriptstore.Stream,
	claim transcriptstore.RunnerClaim,
	sourceEventID int64,
	toolCallID string,
	artifactID string,
	request *agentPublicScientificFileRequest,
) (map[string]any, bool, error) {
	state, err := s.agentPublicScientificDownloadRecoveryState(
		ctx, stream.UID, stream.OwnerID, agentPublicScientificRecoveryCandidateLimit,
	)
	if err != nil {
		return nil, false, err
	}
	var completed agentPublicScientificCompletedDownload
	found := false
	for index := len(state.Completed) - 1; index >= 0; index-- {
		candidate := state.Completed[index]
		if candidate.URL == request.SourceURL && candidate.Filename == request.Filename {
			completed, found = candidate, true
			break
		}
	}
	if !found {
		return nil, false, nil
	}
	if completed.ArtifactID != artifactID || completed.VersionID == "" || completed.SHA256 == "" ||
		completed.SizeBytes <= 0 || (request.ExpectedSHA256 != "" && request.ExpectedSHA256 != completed.SHA256) {
		return nil, true, errAgentPublicScientificFileConflict
	}
	artifact, priorVersion, metadataFound, err := s.workspaceStore.GetArtifactVersionMetadata(completed.VersionID)
	if err != nil || !metadataFound {
		return nil, true, errAgentPublicScientificFileAuthority
	}
	if artifact.ID != artifactID || artifact.ProjectID != stream.ProjectID || artifact.Name != request.Filename ||
		priorVersion.ArtifactID != artifact.ID || priorVersion.SizeBytes != completed.SizeBytes ||
		priorVersion.ContentSHA256 != completed.SHA256 {
		return nil, true, errAgentPublicScientificFileConflict
	}
	_, _, content, contentFound, err := s.workspaceStore.OpenArtifactVersionContentForOwner(completed.VersionID, stream.OwnerID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, workspace.ErrArtifactContentPruned) {
			return nil, true, errAgentPublicScientificFileAuthority
		}
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		request.ExpectedSHA256 = completed.SHA256
		request.RecoveryParentVersionID = priorVersion.ID
		content, err = recoverAgentScientificWorkspaceContent(ctx, workspaceDir, request.Filename, priorVersion, request.maximumBytes())
		if errors.Is(err, os.ErrNotExist) {
			// This is a newly authorized download call, not a blind replay of the
			// historical mutation. The normal transfer still enforces source
			// policy and the original completed digest before creating a version.
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
	} else if !contentFound {
		return nil, true, errAgentPublicScientificFileAuthority
	}
	defer content.Close()
	if err := ensureAgentWorkspaceDownloadTarget(
		ctx, workspaceDir, request.Filename, priorVersion.SizeBytes, priorVersion.ContentSHA256,
		request.maximumBytes(),
	); err != nil {
		return nil, true, agentPublicScientificWorkspaceDownloadError(err)
	}
	mutationDigest := sha256.Sum256([]byte(fmt.Sprintf(
		"reuse-public-scientific-file-v1:%s:%d:%s:%s:%s",
		stream.UID, sourceEventID, toolCallID, request.Filename, request.SourceURL,
	)))
	reusedArtifact, reusedVersion, err := s.workspaceStore.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(ctx, "reuse-public-scientific-file-"+hex.EncodeToString(mutationDigest[:])),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: request.Filename,
			ContentType: artifact.Kind, Content: content, MaxBytes: request.maximumBytes(),
			ParentVersionID: request.RecoveryParentVersionID,
			CreatedBy:       claim.RunnerID, RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: sourceEventID, Relation: "consumed",
				ReuseCurrentVersionIfUnchanged: true,
			},
			Language: agentPublicScientificArtifactLanguage, IsIntermediate: true,
		},
		stream.OwnerID,
	)
	if err != nil {
		return nil, true, err
	}
	replayArtifact, replayVersion, replayContent, replayFound, err := s.workspaceStore.OpenArtifactVersionContent(reusedVersion.ID)
	if err != nil || !replayFound || replayArtifact.ID != reusedArtifact.ID || replayVersion.ID != reusedVersion.ID {
		return nil, true, errAgentPublicScientificFileAuthority
	}
	defer replayContent.Close()
	if err := publishAgentWorkspaceDownloadFile(
		ctx, workspaceDir, request.Filename, replayContent, reusedVersion.SizeBytes, reusedVersion.ContentSHA256,
		request.maximumBytes(),
	); err != nil {
		return nil, true, agentPublicScientificWorkspaceDownloadError(err)
	}
	return s.agentPublicScientificFileResult(stream, reusedArtifact, reusedVersion, *request), true, nil
}

// Reacquire only an exact, bounded local copy under the task root. Staging
// freezes bytes before the new artifact write; a conflicting workspace file
// remains untouched and is not replaced by another network representation.
func recoverAgentScientificWorkspaceContent(ctx context.Context, workspaceDir, filename string, version workspace.ArtifactVersion, maxBytes int64) (workspace.ArtifactContentReader, error) {
	root, err := os.OpenRoot(workspaceDir)
	if err != nil {
		return nil, errAgentPublicScientificFileAuthority
	}
	source, err := openRegularWorkspaceRootFile(root, filename)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, errAgentPublicScientificFileConflict
	}
	stage, err := stageWorkspaceFile(ctx, root, source, min(maxBytes, version.SizeBytes))
	closeErr := source.Close()
	if err != nil || closeErr != nil {
		if stage != nil {
			_ = stage.close()
		}
		_ = root.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errAgentPublicScientificFileConflict
	}
	if stage.size != version.SizeBytes || stage.digest != version.ContentSHA256 {
		_ = stage.close()
		_ = root.Close()
		return nil, errAgentPublicScientificFileConflict
	}
	file, err := openRegularWorkspaceRootFile(root, stage.name)
	if err != nil {
		_ = stage.close()
		_ = root.Close()
		return nil, errAgentPublicScientificFileAuthority
	}
	return &recoveredScientificContent{File: file, stage: stage, root: root}, nil
}

type recoveredScientificContent struct {
	*os.File
	stage *stagedWorkspaceFile
	root  *os.Root
}

func (content *recoveredScientificContent) Close() error {
	return errors.Join(content.File.Close(), content.stage.close(), content.root.Close())
}

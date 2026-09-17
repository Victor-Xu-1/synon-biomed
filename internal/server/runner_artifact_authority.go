package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

type transcriptArtifactRunContextKey struct{}
type transcriptRunnerChatRunContextKey struct{}

type transcriptArtifactRun struct {
	Authority     *transcriptRunnerAuthority
	SourceEventID int64
	ToolCallID    string
}

func withTranscriptArtifactRun(ctx context.Context, authority *transcriptRunnerAuthority, sourceEventID int64) context.Context {
	return context.WithValue(ctx, transcriptArtifactRunContextKey{}, transcriptArtifactRun{
		Authority: authority, SourceEventID: sourceEventID,
	})
}

func transcriptArtifactRunFromContext(ctx context.Context) (transcriptArtifactRun, bool) {
	value, ok := ctx.Value(transcriptArtifactRunContextKey{}).(transcriptArtifactRun)
	return value, ok && value.Authority != nil && value.SourceEventID > 0
}

func withTranscriptRunnerChatRun(ctx context.Context, run *sessionRunnerChatRun) context.Context {
	if run == nil {
		return ctx
	}
	return context.WithValue(ctx, transcriptRunnerChatRunContextKey{}, run)
}

func transcriptRunnerChatRunFromContext(ctx context.Context) (*sessionRunnerChatRun, bool) {
	run, ok := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	return run, ok && run != nil && run.Transcript != nil
}

func (s *Server) withTranscriptArtifactToolSource(ctx context.Context, toolCallID string) context.Context {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if run == nil || run.Transcript == nil {
		return ctx
	}
	toolCallID = strings.TrimSpace(toolCallID)
	sourceEventID := run.ToolSourceEventIDs[toolCallID]
	if sourceEventID <= 0 && s != nil && s.workspaceStore != nil {
		batchID := strings.TrimSpace(run.ToolBatchIDs[toolCallID])
		ordinal, hasOrdinal := run.ToolBatchOrdinals[toolCallID]
		if batchID != "" && hasOrdinal {
			items, err := s.workspaceStore.ListToolCallBatchItems(
				context.WithoutCancel(ctx), run.Transcript.Stream.OwnerID, batchID,
			)
			if err == nil {
				for _, item := range items {
					if item.Ordinal == ordinal && item.ToolCallID == toolCallID && item.StartedEventID > 0 {
						sourceEventID = item.StartedEventID
						if run.ToolSourceEventIDs == nil {
							run.ToolSourceEventIDs = map[string]int64{}
						}
						run.ToolSourceEventIDs[toolCallID] = sourceEventID
						break
					}
				}
			}
		}
	}
	if sourceEventID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, transcriptArtifactRunContextKey{}, transcriptArtifactRun{
		Authority: run.Transcript, SourceEventID: sourceEventID, ToolCallID: strings.TrimSpace(toolCallID),
	})
}

func (s *Server) registerTranscriptArtifact(
	ctx context.Context,
	run transcriptArtifactRun,
	input map[string]any,
) (map[string]any, error) {
	if s.workspaceStore == nil || s.transcriptStore == nil || run.Authority == nil {
		return nil, errors.New("transcript artifact authority is unavailable")
	}
	stream := run.Authority.Stream
	claim := run.Authority.Claim
	if sessionID := strings.TrimSpace(stringValue(input["sessionId"])); sessionID != "" && sessionID != stream.SessionID {
		return nil, errors.New("artifact session does not match the active transcript")
	}
	if runnerID := strings.TrimSpace(stringValue(input["runId"])); runnerID != "" && runnerID != claim.RunnerID {
		return nil, errors.New("artifact runner does not match the active transcript")
	}
	resolved, relativePath, err := resolveArtifactPath(s.fileRoot, stringValue(input["path"]))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("artifact path is a directory: %s", relativePath)
	}
	if info.Size() > v11ArtifactBinaryLimit {
		return nil, fmt.Errorf("artifact content exceeds %d byte limit", v11ArtifactBinaryLimit)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if err := validateAgentSavedArtifactJSONBytes(relativePath, data); err != nil {
		return nil, err
	}
	if err := validateAgentSavedArtifactDelimitedBytes(relativePath, data); err != nil {
		return nil, err
	}
	artifactID := transcriptArtifactIDFor(stream.OwnerID, stream.ProjectID, stream.UID, relativePath)
	contentType := detectArtifactMIME(data)
	mutationDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", stream.UID, claim.Attempt, run.SourceEventID)))
	mutationID := "transcript-artifact-" + hex.EncodeToString(mutationDigest[:16])
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(ctx, mutationID),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: filepath.Base(relativePath),
			ContentType: contentType, Content: bytes.NewReader(data), MaxBytes: v11ArtifactBinaryLimit,
			CreatedBy: claim.RunnerID, RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: run.SourceEventID, Relation: "produced",
				ReuseCurrentVersionIfUnchanged: true,
			},
		},
		stream.OwnerID,
	)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringValue(input["title"]))
	if title == "" {
		title = artifact.Name
	}
	kind := strings.TrimSpace(stringValue(input["kind"]))
	if kind == "" {
		kind = "file"
	}
	projection := map[string]any{
		"artifactId": artifact.ID, "versionId": version.ID, "kind": kind, "title": title,
		"description":  strings.TrimSpace(stringValue(input["description"])),
		"relativePath": relativePath, "fileName": artifact.Name, "mimeType": contentType,
		"sizeBytes": version.SizeBytes, "sha256": version.ContentSHA256,
		"sessionId": stream.SessionID, "runId": claim.RunnerID, "createdAt": version.CreatedAt.Format(time.RFC3339Nano),
	}
	if metadata, ok := input["metadata"]; ok && metadata != nil {
		projection["metadata"] = metadata
	}
	if metadataErr := carryForwardRCSBArtifactRuntimeMetadata(s.runtimeStore, artifact.ID, projection); metadataErr != nil {
		log.Printf("register_artifact compatibility metadata carry-forward failed for artifact %s: %v", artifact.ID, metadataErr)
	}
	entry, err := s.runtimeStore.Set(artifactRuntimeNamespace, artifact.ID, projection)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": entry.Value, "stored": true}, nil
}

func transcriptArtifactIDFor(_, _, streamUID, relativePath string) string {
	return streamArtifactIDFor(streamUID, relativePath)
}

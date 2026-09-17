package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const runnerTaskMemoryPolicyVersion = "synon-memory-v2"

func (s *Server) runnerTaskMemoryContext(
	ctx context.Context,
	session sessionstore.Session,
	messages []chatCompletionMessage,
	run *sessionRunnerChatRun,
	taskIntentID string,
	taskIntentRevision int64,
) (string, error) {
	if run == nil || run.Transcript == nil {
		workspaceContext, err := s.workspaceMemoryContext(ctx, session, messages)
		return workspaceContext, err
	}
	taskIntentID = strings.TrimSpace(taskIntentID)
	if taskIntentID == "" || taskIntentRevision <= 0 {
		return "", errors.New("canonical task intent identity is required for runner memory")
	}
	claim := run.Transcript.Claim
	if claim.ResumeSource == transcriptstore.ResumeSourceCheckpoint {
		snapshot, found, err := s.transcriptStore.GetRunnerTaskMemorySnapshotForTask(
			ctx, claim.StreamUID, claim.OwnerID, claim.Attempt, taskIntentID, taskIntentRevision,
		)
		if err != nil {
			return "", fmt.Errorf("load runner task memory snapshot: %w", err)
		}
		copySnapshot := false
		if !found {
			snapshot, found, err = s.transcriptStore.GetRunnerTaskMemorySnapshotForCheckpointTask(
				ctx, claim.StreamUID, claim.OwnerID, claim.ResumeCheckpoint, taskIntentID, taskIntentRevision,
			)
			if err != nil {
				return "", fmt.Errorf("load checkpoint task memory snapshot: %w", err)
			}
			copySnapshot = found
		}
		if !runnerTaskMemoryMatchesTask(snapshot, taskIntentID, taskIntentRevision, claim.ClaimedInputRevision) {
			// A resumed attempt can reach its checkpoint before any snapshot
			// was sealed on the attempt (for example, it paused for approval,
			// or a previous fresh attempt failed before its first model call).
			// Carry the newest durable snapshot forward when it still
			// describes this task; otherwise recompute memory from the
			// authoritative transcript instead of killing the task.
			latest, latestFound, latestErr := s.transcriptStore.LatestRunnerTaskMemorySnapshotForTask(
				ctx, claim.StreamUID, claim.OwnerID, taskIntentID, taskIntentRevision,
			)
			if latestErr != nil {
				return "", fmt.Errorf("load latest runner task memory snapshot: %w", latestErr)
			}
			if latestFound && runnerTaskMemoryMatchesTask(latest, taskIntentID, taskIntentRevision, claim.ClaimedInputRevision) {
				snapshot = latest
				copySnapshot = true
			} else {
				return s.sealRunnerTaskMemoryForTask(ctx, session, messages, run, taskIntentID, taskIntentRevision)
			}
		}
		if copySnapshot {
			payload, err := runnerTaskMemorySnapshotPayload(snapshot)
			if err != nil {
				return "", err
			}
			if _, err := s.checkpointTranscriptRunnerEventWithDestinations(
				ctx, run.Transcript, transcriptstore.RunnerPhasePlanning,
				"runner-task-memory-snapshot-v1", payload, false, nil,
			); err != nil {
				return "", fmt.Errorf("copy runner task memory snapshot: %w", err)
			}
		}
		return snapshot.WorkspaceMemory, nil
	}
	if claim.ResumeSource != transcriptstore.ResumeSourceFresh && claim.ResumeSource != transcriptstore.ResumeSourceUserInput {
		return "", errors.New("runner task memory requires a fresh task or checkpoint continuation")
	}
	return s.sealRunnerTaskMemoryForTask(ctx, session, messages, run, taskIntentID, taskIntentRevision)
}

func runnerTaskMemoryMatchesTask(
	snapshot transcriptstore.RunnerTaskMemorySnapshot,
	taskIntentID string,
	taskIntentRevision, claimedInputRevision int64,
) bool {
	return snapshot.TaskIntentID == taskIntentID &&
		snapshot.TaskIntentRevision == taskIntentRevision &&
		snapshot.InitialInputRevision <= claimedInputRevision &&
		snapshot.PolicyVersion == runnerTaskMemoryPolicyVersion
}

func (s *Server) sealRunnerTaskMemoryForTask(
	ctx context.Context,
	session sessionstore.Session,
	messages []chatCompletionMessage,
	run *sessionRunnerChatRun,
	taskIntentID string,
	taskIntentRevision int64,
) (string, error) {
	claim := run.Transcript.Claim
	workspaceContext, err := s.workspaceMemoryContext(ctx, session, messages)
	if err != nil {
		return "", err
	}
	snapshot, err := transcriptstore.SealRunnerTaskMemorySnapshot(transcriptstore.RunnerTaskMemorySnapshot{
		TaskIntentID: taskIntentID, TaskIntentRevision: taskIntentRevision,
		InitialInputRevision: claim.ClaimedInputRevision,
		SessionMemory:        "", WorkspaceMemory: workspaceContext,
		PolicyVersion: runnerTaskMemoryPolicyVersion,
	})
	if err != nil {
		return "", fmt.Errorf("seal runner task memory snapshot: %w", err)
	}
	payload, err := runnerTaskMemorySnapshotPayload(snapshot)
	if err != nil {
		return "", err
	}
	clientMessageID := runnerTaskMemorySnapshotClientMessageID(snapshot)
	if _, err := s.checkpointTranscriptRunnerEventWithDestinations(
		ctx, run.Transcript, transcriptstore.RunnerPhasePlanning,
		clientMessageID, payload, true, nil,
	); err != nil {
		return "", fmt.Errorf("persist runner task memory snapshot: %w", err)
	}
	return workspaceContext, nil
}

func runnerTaskMemorySnapshotClientMessageID(snapshot transcriptstore.RunnerTaskMemorySnapshot) string {
	sha := strings.ToLower(strings.TrimSpace(snapshot.SHA256))
	if len(sha) >= 16 {
		return "runner-task-memory-snapshot-v1-" + sha[:16]
	}
	return "runner-task-memory-snapshot-v1"
}

func runnerTaskMemorySnapshotPayload(snapshot transcriptstore.RunnerTaskMemorySnapshot) (map[string]any, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

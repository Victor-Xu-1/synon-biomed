package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// save_artifacts is intentionally item-granular: one miss in a requested
// batch does not roll back the files that were durably registered. Completion
// publication must follow the same contract. Treat each returned artifact as
// a candidate draft and let the immutable commit intersection below decide
// what this exact run is allowed to publish.
func sessionRunnerSavedArtifactsFromToolContent(content string) []map[string]any {
	var result any
	if strings.TrimSpace(content) == "" || json.Unmarshal([]byte(content), &result) != nil ||
		agentruntime.IsNonExecutingPreflight(result) {
		return nil
	}
	return sessionRunnerSavedArtifactResults(result)
}

// publishSessionRunnerCompletionArtifacts is the only draft-to-user-visible
// transition. It runs after every completion validator has accepted the final
// candidate and before the terminal assistant event is committed. The durable
// commit ledger and stored version state determine publication; compacted or
// externalized provider history cannot hide a successfully saved draft.
func (s *Server) publishSessionRunnerCompletionArtifacts(
	ctx context.Context,
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
) error {
	if len(commits) == 0 {
		return nil
	}
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil {
		return errors.New("runner artifact publication authority is unavailable")
	}
	projectID := strings.TrimSpace(sessionRunnerProjectID(session))
	ownerID := strings.TrimSpace(run.Transcript.Stream.OwnerID)
	if projectID == "" || ownerID == "" {
		return errors.New("runner artifact publication scope is unavailable")
	}
	for _, commit := range latestSessionRunnerArtifactReferences(commits) {
		if commit.Relation != transcriptstore.ArtifactRelationProduced {
			continue
		}
		versionID := strings.TrimSpace(commit.VersionID)
		retention, found, err := s.workspaceStore.ArtifactRetentionMode(commit.ArtifactID)
		if err != nil {
			return err
		}
		if !found || retention != "snapshot" {
			continue
		}
		intermediate, found, err := s.workspaceStore.ArtifactVersionIntermediate(versionID)
		if err != nil {
			return err
		}
		if !found || !intermediate {
			continue
		}
		if err := s.workspaceStore.PublishArtifactVersion(
			ctx, versionID, strings.TrimSpace(commit.ArtifactID), projectID, ownerID,
		); err != nil {
			return err
		}
	}
	return nil
}

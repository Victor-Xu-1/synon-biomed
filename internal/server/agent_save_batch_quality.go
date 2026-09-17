package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	maxAgentSaveBatchQualityArtifacts = 32
	maxAgentSaveBatchQualityBytes     = int64(32 << 20)
)

// agentSavedArtifactBatchQualityAdvisory evaluates only immutable versions
// that the current save call has already published. It gives the same model
// loop a repair opportunity without making quality diagnostics a prerequisite
// for preserving valid output bytes.
func (s *Server) agentSavedArtifactBatchQualityAdvisory(ctx context.Context, projectID string, artifacts []any) map[string]any {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" ||
		len(artifacts) < 2 || len(artifacts) > maxAgentSaveBatchQualityArtifacts {
		return nil
	}
	commits := make([]transcriptstore.ArtifactReferenceInput, 0, len(artifacts))
	var totalBytes int64
	for _, raw := range artifacts {
		artifact := mapValue(raw)
		artifactID, versionID := strings.TrimSpace(stringValue(artifact["artifact_id"])), strings.TrimSpace(stringValue(artifact["version_id"]))
		size := int64(numberValue(artifact["size_bytes"]))
		if artifactID == "" || versionID == "" || size < 0 {
			return nil
		}
		totalBytes += size
		if totalBytes > maxAgentSaveBatchQualityBytes {
			return nil
		}
		commits = append(commits, transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifactID, VersionID: versionID, Relation: transcriptstore.ArtifactRelationProduced,
		})
	}
	failures, err := s.validateSessionRunnerCrossArtifactConsistency(ctx, projectID, commits)
	if err != nil {
		return map[string]any{
			"code": "artifact_consistency_validation_unavailable", "severity": "warning",
			"blocking": false, "quality_advisory": true, "completion_check_required": true,
			"message": "Artifacts were saved, but the current-batch consistency check was unavailable. Terminal validation must retry the check before publication.",
		}
	}
	failures = uniqueSortedFolded(failures)
	sort.Strings(failures)
	if len(failures) == 0 {
		return nil
	}
	completionBlocking := false
	for _, failure := range failures {
		if !sessionRunnerCompletionAdvisoryCrossArtifactFailure(failure) &&
			!sessionRunnerEvidenceGapCrossArtifactFailure(failure) {
			completionBlocking = true
			break
		}
	}
	digest := sha256.Sum256([]byte("synon.artifact-consistency-advisory.v1\n" + strings.Join(failures, "\n")))
	visible := failures
	if len(visible) > 32 {
		visible = append([]string(nil), visible[:32]...)
	}
	message := "Artifacts were saved. The current immutable versions contain specific consistency findings that can be reviewed before final use."
	if completionBlocking {
		message = "Artifacts were saved, but at least one structural consistency finding must be resolved before terminal publication."
	}
	advisory := map[string]any{
		"code": "artifact_consistency_advisory", "severity": "warning",
		"blocking": false, "quality_advisory": true, "completion_blocking": completionBlocking,
		"quality_snapshot_id": hex.EncodeToString(digest[:]), "findings": visible,
		"finding_count": len(failures), "findings_truncated": len(visible) < len(failures),
		"message": message,
	}
	if requirements := runnerArtifactRepairRequirements(strings.Join(failures, "; ")); len(requirements) > 0 {
		advisory["repair_requirements"] = requirements
	}
	return advisory
}

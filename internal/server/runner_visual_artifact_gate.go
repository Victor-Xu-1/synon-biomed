package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const sessionRunnerVisualArtifactValidationReasonCode = "visual_artifact_validation_required"

type sessionRunnerVisualArtifactValidationRequired struct {
	Artifacts              []string
	Targets                []transcriptstore.RunnerVisualConditionArtifact
	StructureSceneFailures []string
}

func (err *sessionRunnerVisualArtifactValidationRequired) Error() string {
	if err == nil {
		return "generated visual artifacts require immutable visual validation"
	}
	names := append([]string(nil), err.Artifacts...)
	names = append(names, err.StructureSceneFailures...)
	names = uniqueSortedStrings(names)
	if len(names) == 0 {
		return "generated visual artifacts require immutable visual validation"
	}
	sort.Strings(names)
	if len(names) > 8 {
		names = append(names[:8], fmt.Sprintf("and %d more", len(names)-8))
	}
	if len(err.StructureSceneFailures) > 0 && len(err.Artifacts) == 0 {
		return "multi-structure delivery does not satisfy the immutable structure-scene contract: " + strings.Join(names, ", ")
	}
	return "generated visual artifacts or structure scenes are not bound to the required immutable visual evidence: " + strings.Join(names, ", ")
}

func (err *sessionRunnerVisualArtifactValidationRequired) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	var artifacts []string
	if err != nil {
		artifacts = append([]string(nil), err.Artifacts...)
		artifacts = append(artifacts, err.StructureSceneFailures...)
	}
	artifacts = uniqueSortedStrings(artifacts)
	visual := transcriptstore.RunnerVisualCondition{UnboundNames: artifacts}
	if err != nil && len(err.Targets) > 0 {
		visual.Artifacts = append([]transcriptstore.RunnerVisualConditionArtifact(nil), err.Targets...)
		visual.UnboundNames = append([]string(nil), err.StructureSceneFailures...)
	}
	return newRunnerCorrection(sessionRunnerVisualArtifactValidationReasonCode,
		err.Error()+". For every generated visual artifact, bind the renderer output to its immutable hash and pass VisualReview. For any delivery containing multiple structures, also save one synon.structure-scene.v1 manifest that names the exact mother and derived versions, includes visible layers and a preview image, then validate that preview with VisualReview before answering. Repair every overlap, clipping, hash, canvas, scene, or manifest blocker; a semantic VisualReview pass is valid only after its model-visible challenge succeeds.",
		transcriptstore.RunnerCorrectionCondition{Visual: &visual},
	)
}

// verifySessionRunnerVisualArtifactEvidence is the deterministic image gate for
// the user-controlled completion-verification pipeline. Optional independent
// review may add broader judgment, but verifier_mode=off must not start or keep
// a review workflow alive after the main task has completed.
func (s *Server) verifySessionRunnerVisualArtifactEvidence(session sessionstore.Session) error {
	// Not every runner entry point is backed by a workspace frame. IM,
	// delegation, and direct TaskRun sessions can legitimately complete without
	// one, and therefore cannot have workspace artifact versions to validate.
	// Workspace-backed sessions remain strict: once a frame exists, every image
	// artifact must be bound to an immutable passing VisualReview record.
	if s == nil || s.workspaceStore == nil {
		return nil
	}
	frame, found, err := s.workspaceStore.GetFrame(session.ID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = strings.TrimSpace(frame.ID)
	}
	workspaceEvidence, err := s.sessionReviewerWorkspaceEvidence(session, rootFrameID)
	if err != nil {
		return err
	}
	if sessionRunnerVerificationEnabled(session) {
		if err := s.verifyVisualArtifactEvidence(workspaceEvidence.Artifacts); err != nil {
			return err
		}
	}
	return s.verifyStructureSceneEvidence(workspaceEvidence.Artifacts)
}

func (s *Server) verifyVisualArtifactEvidence(artifacts []sessionReviewerArtifactEvidence) error {
	visualArtifacts := make([]sessionReviewerArtifactEvidence, 0)
	for _, artifact := range artifacts {
		if visualArtifactName(artifact.Name) {
			visualArtifacts = append(visualArtifacts, artifact)
		}
	}
	if len(visualArtifacts) == 0 {
		return nil
	}
	if s == nil || s.runtimeStore == nil {
		return errors.New("visual review runtime store is unavailable")
	}
	validatedHashes, err := s.validatedVisualReviewImageHashes()
	if err != nil {
		return err
	}
	missing := []string{}
	targets := []transcriptstore.RunnerVisualConditionArtifact{}
	for _, artifact := range visualArtifacts {
		digest := strings.ToLower(strings.TrimSpace(artifact.ContentSHA256))
		if len(digest) != sha256.Size*2 {
			missing = append(missing, artifact.Name+" (invalid artifact digest)")
			targets = append(targets, transcriptstore.RunnerVisualConditionArtifact{Name: artifact.Name, VersionID: artifact.VersionID, SHA256: artifact.ContentSHA256, Failure: "invalid_digest"})
			continue
		}
		if _, found := validatedHashes[digest]; !found {
			missing = append(missing, artifact.Name)
			targets = append(targets, transcriptstore.RunnerVisualConditionArtifact{Name: artifact.Name, VersionID: artifact.VersionID, SHA256: digest, Failure: "missing_validation"})
		}
	}
	if len(missing) > 0 {
		return &sessionRunnerVisualArtifactValidationRequired{Artifacts: missing, Targets: targets}
	}
	return nil
}

func (s *Server) validatedVisualReviewImageHashes() (map[string]struct{}, error) {
	if s == nil || s.runtimeStore == nil {
		return nil, errors.New("visual review runtime store is unavailable")
	}
	records, err := s.runtimeStore.List(visualReviewRuntimeNamespace)
	if err != nil {
		return nil, fmt.Errorf("list visual review records: %w", err)
	}
	validatedHashes := map[string]struct{}{}
	for _, entry := range records {
		record := mapValue(entry.Value)
		if !visualReviewRecordPassed(record) {
			continue
		}
		for _, rawEvidence := range anySliceValue(record["evidence"]) {
			evidence := mapValue(rawEvidence)
			if stringValue(evidence["source"]) != "image_path" || stringValue(evidence["status"]) != "attached" {
				continue
			}
			digest := strings.ToLower(strings.TrimSpace(stringValue(evidence["sha256"])))
			if len(digest) == sha256.Size*2 {
				validatedHashes[digest] = struct{}{}
			}
		}
	}
	return validatedHashes, nil
}

func visualArtifactName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func visualReviewRecordPassed(record map[string]any) bool {
	if !boolValue(record["visual_verified"], false) || strings.ToLower(strings.TrimSpace(stringValue(record["verdict"]))) != "pass" {
		return false
	}
	switch strings.TrimSpace(stringValue(record["action"])) {
	case "validate_layout":
		validation := mapValue(record["layout_validation"])
		return boolValue(record["layout_verified"], false) &&
			strings.TrimSpace(stringValue(record["evidence_level"])) == "machine_verified_layout" &&
			numberValue(validation["overlap_count"]) == 0 &&
			numberValue(validation["clipped_count"]) == 0 &&
			numberValue(validation["invalid_box_count"]) == 0
	case "record_assessment":
		assessment := mapValue(record["assessment"])
		response := strings.ToUpper(strings.TrimSpace(stringValue(assessment["visual_challenge_response"])))
		expectedHash := strings.TrimSpace(stringValue(record[visualReviewChallengeHashField]))
		actualHash := sha256.Sum256([]byte(response))
		return response != "" && expectedHash != "" && strings.EqualFold(expectedHash, hex.EncodeToString(actualHash[:]))
	default:
		return false
	}
}

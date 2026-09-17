package server

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) sessionRunnerVerificationRoot(sessionID string) (string, error) {
	if s == nil || s.workspaceStore == nil {
		return "", errors.New("workspace store is required for completion verification")
	}
	frame, found, err := s.workspaceStore.GetFrame(sessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("verification frame %s not found", sessionID)
	}
	return strings.TrimSpace(frame.RootFrameID), nil
}

func (s *Server) persistSessionRunnerReview(rootFrameID, sessionID, reviewerFrameID, reviewerModel, answer string, review sessionRunnerReview, reviewIndex int, sourceRef map[string]any) ([]string, error) {
	if review.Verdict == "pass" {
		ids := make([]string, 0, max(1, len(review.Issues)))
		for _, issue := range review.Issues {
			verdict := normalizeReviewerIssueVerdict(issue.Verdict)
			if verdict == "fail" {
				return nil, errors.New("pass review contains a blocking issue")
			}
			if verdict != "pass" && verdict != "warn" {
				continue
			}
			claim := firstNonEmpty(issue.Claim, "Reviewer traced the completion")
			evidence := firstNonEmpty(issue.Evidence, review.Summary, "Independent completion review passed")
			status := "resolved"
			if verdict == "warn" {
				status = "unaddressed"
			}
			check := workspace.VerificationCheck{
				ID: runnerReviewCheckID(sessionID, reviewIndex, claim, sourceRef), Claim: &claim,
				Verdict: verdict, Status: status, Evidence: &evidence,
				ArtifactVersionID: issue.ArtifactVersionID,
				ReviewerIndex:     &reviewIndex, ReviewerModel: optionalStringPointer(reviewerModel),
				ReviewerFrameID: optionalStringPointer(reviewerFrameID),
				SourceRef:       sourceRef,
				CreatedAt:       time.Now().UTC(),
			}
			if severity := strings.TrimSpace(issue.Severity); severity != "" {
				check.Severity = optionalStringPointer(normalizeReviewerSeverity(severity))
			}
			if err := s.workspaceStore.AppendVerificationCheck(rootFrameID, check); err != nil {
				return nil, err
			}
			ids = append(ids, check.ID)
		}
		if len(ids) > 0 {
			return ids, nil
		}
		claim := "Agent completion satisfies the independent reviewer acceptance criteria"
		evidence := firstNonEmpty(review.Summary, "Independent completion review passed")
		check := workspace.VerificationCheck{
			ID: runnerReviewCheckID(sessionID, reviewIndex, claim, sourceRef), Claim: &claim,
			Verdict: "pass", Status: "resolved", Evidence: &evidence,
			ArtifactVersionID: sessionRunnerReviewArtifactVersionID(review.EvidenceRefs, sourceRef),
			ReviewerIndex:     &reviewIndex, ReviewerModel: optionalStringPointer(reviewerModel),
			ReviewerFrameID: optionalStringPointer(reviewerFrameID),
			SourceRef:       sourceRef,
			CreatedAt:       time.Now().UTC(),
		}
		if err := s.workspaceStore.AppendVerificationCheck(rootFrameID, check); err != nil {
			return nil, err
		}
		return []string{check.ID}, nil
	}
	issues := review.Issues
	if len(issues) == 0 {
		issues = []sessionRunnerReviewIssue{{Claim: firstNonEmpty(review.Summary, "Completion requires revision"), Severity: "high", Evidence: review.Feedback}}
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		claim := firstNonEmpty(issue.Claim, review.Summary, "Completion requires revision")
		evidence := firstNonEmpty(issue.Evidence, review.Feedback, review.Summary)
		severity := normalizeReviewerSeverity(issue.Severity)
		verdict := normalizeReviewerIssueVerdict(issue.Verdict)
		if verdict == "" {
			verdict = "warn"
			if severity == "critical" || severity == "high" {
				verdict = "fail"
			}
		}
		status := "unaddressed"
		if verdict == "pass" {
			status = "resolved"
		}
		check := workspace.VerificationCheck{
			ID: runnerReviewCheckID(sessionID, reviewIndex, claim, sourceRef), Claim: &claim,
			Verdict: verdict, Status: status, Evidence: &evidence,
			ArtifactVersionID: issue.ArtifactVersionID,
			ReviewerIndex:     &reviewIndex, ReviewerModel: optionalStringPointer(reviewerModel),
			ReviewerFrameID: optionalStringPointer(reviewerFrameID),
			SourceRef:       sourceRef,
			CreatedAt:       time.Now().UTC(),
		}
		if strings.TrimSpace(issue.Severity) != "" {
			check.Severity = &severity
		}
		if err := s.workspaceStore.AppendVerificationCheck(rootFrameID, check); err != nil {
			return nil, err
		}
		ids = append(ids, check.ID)
	}
	return ids, nil
}

// sessionRunnerReviewArtifactVersionID projects a reviewer finding onto the
// artifact verification surface only when its server-verified evidence
// receipts resolve to one exact immutable version. Cross-artifact findings
// remain session-level checks instead of being attributed to an arbitrary
// artifact.
func sessionRunnerReviewArtifactVersionID(evidenceRefs []string, sourceRef map[string]any) *string {
	if len(evidenceRefs) == 0 || sourceRef == nil {
		return nil
	}
	receiptVersions := map[string]string{}
	appendReceipt := func(receiptID, versionID string) {
		receiptID = strings.TrimSpace(receiptID)
		versionID = strings.TrimSpace(versionID)
		if receiptID == "" || versionID == "" {
			return
		}
		if previous, found := receiptVersions[receiptID]; found && previous != versionID {
			receiptVersions[receiptID] = ""
			return
		}
		receiptVersions[receiptID] = versionID
	}
	switch receipts := sourceRef["read_receipts"].(type) {
	case []sessionReviewerEvidenceReceipt:
		for _, receipt := range receipts {
			appendReceipt(receipt.ReceiptID, receipt.VersionID)
		}
	case []any:
		for _, raw := range receipts {
			switch receipt := raw.(type) {
			case sessionReviewerEvidenceReceipt:
				appendReceipt(receipt.ReceiptID, receipt.VersionID)
			case map[string]any:
				appendReceipt(
					firstNonEmpty(stringValue(receipt["receiptId"]), stringValue(receipt["receipt_id"])),
					firstNonEmpty(stringValue(receipt["versionId"]), stringValue(receipt["version_id"])),
				)
			}
		}
	case []map[string]any:
		for _, receipt := range receipts {
			appendReceipt(
				firstNonEmpty(stringValue(receipt["receiptId"]), stringValue(receipt["receipt_id"])),
				firstNonEmpty(stringValue(receipt["versionId"]), stringValue(receipt["version_id"])),
			)
		}
	}
	versionID := ""
	for _, ref := range evidenceRefs {
		candidate := receiptVersions[strings.TrimSpace(ref)]
		if candidate == "" {
			continue
		}
		if versionID == "" {
			versionID = candidate
			continue
		}
		if candidate != versionID {
			return nil
		}
	}
	return optionalStringPointer(versionID)
}

func sessionRunnerVerificationSourceRef(
	rootFrameID string,
	sessionID string,
	reviewIndex int,
	answer string,
	run *sessionRunnerChatRun,
	workspaceEvidence sessionReviewerWorkspaceEvidence,
) (map[string]any, error) {
	rootFrameID = strings.TrimSpace(rootFrameID)
	sessionID = strings.TrimSpace(sessionID)
	answer = strings.TrimSpace(answer)
	if rootFrameID == "" || sessionID == "" || reviewIndex < 0 || answer == "" {
		return nil, errors.New("completion review identity and candidate are required")
	}
	if run == nil || run.Attempt <= 0 || run.Transcript == nil ||
		run.Transcript.Claim.StreamUID == "" || run.Transcript.Claim.ClaimedInputRevision <= 0 ||
		int64(run.Attempt) != run.Transcript.Claim.Attempt {
		return nil, errors.New("completion review requires an exact transcript runner authority")
	}
	if !workspaceEvidence.InventoryComplete {
		return nil, errors.New("completion review artifact inventory is incomplete")
	}
	candidateHash := sha256.New()
	writeSessionReviewDigestString(candidateHash, "synon.runner-review.candidate.v1")
	writeSessionReviewDigestString(candidateHash, answer)
	artifacts := append([]sessionReviewerArtifactEvidence(nil), workspaceEvidence.Artifacts...)
	artifactIDs := map[string]struct{}{}
	versionIDs := map[string]struct{}{}
	names := map[string]struct{}{}
	for index := range artifacts {
		artifact := &artifacts[index]
		artifact.ArtifactID = strings.TrimSpace(artifact.ArtifactID)
		artifact.Name = strings.TrimSpace(artifact.Name)
		artifact.Kind = strings.TrimSpace(artifact.Kind)
		artifact.VersionID = strings.TrimSpace(artifact.VersionID)
		artifact.ContentSHA256 = strings.TrimSpace(artifact.ContentSHA256)
		decodedSHA, decodeErr := hex.DecodeString(artifact.ContentSHA256)
		if artifact.ArtifactID == "" || artifact.Name == "" || artifact.Kind == "" || artifact.VersionID == "" ||
			len(artifact.ContentSHA256) != 64 || strings.ToLower(artifact.ContentSHA256) != artifact.ContentSHA256 ||
			decodeErr != nil || len(decodedSHA) != sha256.Size {
			return nil, fmt.Errorf("completion review artifact %d has invalid immutable identity", index)
		}
		if _, duplicate := artifactIDs[artifact.ArtifactID]; duplicate {
			return nil, errors.New("completion review artifact inventory contains a duplicate artifact id")
		}
		if _, duplicate := versionIDs[artifact.VersionID]; duplicate {
			return nil, errors.New("completion review artifact inventory contains a duplicate version id")
		}
		if _, duplicate := names[artifact.Name]; duplicate {
			return nil, errors.New("completion review artifact inventory contains a duplicate name")
		}
		artifactIDs[artifact.ArtifactID] = struct{}{}
		versionIDs[artifact.VersionID] = struct{}{}
		names[artifact.Name] = struct{}{}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Name != artifacts[j].Name {
			return artifacts[i].Name < artifacts[j].Name
		}
		if artifacts[i].ArtifactID != artifacts[j].ArtifactID {
			return artifacts[i].ArtifactID < artifacts[j].ArtifactID
		}
		if artifacts[i].VersionID != artifacts[j].VersionID {
			return artifacts[i].VersionID < artifacts[j].VersionID
		}
		if artifacts[i].Kind != artifacts[j].Kind {
			return artifacts[i].Kind < artifacts[j].Kind
		}
		return artifacts[i].ContentSHA256 < artifacts[j].ContentSHA256
	})
	artifactHash := sha256.New()
	writeSessionReviewDigestString(artifactHash, "synon.runner-review.artifact-inventory.v1")
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(artifacts)))
	_, _ = artifactHash.Write(count[:])
	for _, artifact := range artifacts {
		writeSessionReviewDigestString(artifactHash, artifact.ArtifactID)
		writeSessionReviewDigestString(artifactHash, artifact.Name)
		writeSessionReviewDigestString(artifactHash, artifact.Kind)
		writeSessionReviewDigestString(artifactHash, artifact.VersionID)
		writeSessionReviewDigestString(artifactHash, artifact.ContentSHA256)
	}
	result := map[string]any{
		"schema":                    sessionReviewerEvidenceSchema,
		"session_id":                sessionID,
		"root_frame_id":             rootFrameID,
		"kind":                      "runner_completion_review",
		"review_index":              reviewIndex,
		"candidate_sha256":          hex.EncodeToString(candidateHash.Sum(nil)),
		"artifact_inventory_sha256": hex.EncodeToString(artifactHash.Sum(nil)),
		"artifact_count":            len(artifacts),
		"inventory_complete":        true,
		"artifact_refs":             artifacts,
		"runner_attempt":            run.Attempt,
		"stream_uid":                run.Transcript.Claim.StreamUID,
		"claimed_input_revision":    run.Transcript.Claim.ClaimedInputRevision,
	}
	return result, nil
}

func writeSessionReviewDigestString(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len([]byte(value))))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func (s *Server) sessionRunnerOpenVerificationCheckIDs(rootFrameID, sessionID string, run *sessionRunnerChatRun) ([]string, error) {
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil ||
		run.Transcript.Claim.StreamUID == "" || run.Transcript.Claim.ClaimedInputRevision <= 0 {
		return []string{}, nil
	}
	checks, err := s.workspaceStore.ListVerificationChecks(rootFrameID, "open")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(checks))
	for _, check := range checks {
		sourceRef, ok := check.SourceRef.(map[string]any)
		if !ok || stringValue(sourceRef["kind"]) != "runner_completion_review" ||
			stringValue(sourceRef["session_id"]) != sessionID ||
			stringValue(sourceRef["stream_uid"]) != run.Transcript.Claim.StreamUID ||
			numberValue(sourceRef["claimed_input_revision"]) != run.Transcript.Claim.ClaimedInputRevision {
			continue
		}
		result = append(result, check.ID)
	}
	return result, nil
}

func runnerReviewCheckID(sessionID string, reviewIndex int, claim string, sourceRef map[string]any) string {
	digest := sha256.New()
	writeSessionReviewDigestString(digest, "synon.runner-review.check-id.v1")
	for _, value := range []string{
		sessionID,
		fmt.Sprint(reviewIndex),
		stringValue(sourceRef["stream_uid"]),
		fmt.Sprint(sourceRef["runner_attempt"]),
		fmt.Sprint(sourceRef["claimed_input_revision"]),
		stringValue(sourceRef["kind"]),
		stringValue(sourceRef["reviewer_profile"]),
		stringValue(sourceRef["candidate_sha256"]),
		stringValue(sourceRef["artifact_inventory_sha256"]),
		stringValue(sourceRef["read_receipts_sha256"]),
		stringValue(sourceRef["scientific_decision_sha256"]),
		claim,
	} {
		writeSessionReviewDigestString(digest, value)
	}
	return "runner-review-" + hex.EncodeToString(digest.Sum(nil)[:12])
}

func optionalStringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func sessionRunnerAttempt(run *sessionRunnerChatRun) int {
	if run == nil {
		return 0
	}
	return run.Attempt
}

func (s *Server) checkpointSessionReview(options SessionRunnerChatOptions, run *sessionRunnerChatRun, reviewIndex int, status, message string, details map[string]any) error {
	if run == nil {
		return nil
	}
	payload := map[string]any{"reviewIndex": reviewIndex, "reviewerModel": options.Model}
	for key, value := range details {
		payload[key] = value
	}
	return s.checkpointChatTool(options, run, status, message, fmt.Sprintf("completion-review-%d-%d", run.Attempt, reviewIndex), "verification", payload)
}

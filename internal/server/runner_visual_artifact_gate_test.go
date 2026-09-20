package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestVisualArtifactCompletionGateSkipsSessionsWithoutWorkspaceFrame(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if err := srv.verifySessionRunnerVisualArtifactEvidence(sessionstore.Session{ID: "im:wechat:standalone"}); err != nil {
		t.Fatalf("non-workspace session unexpectedly required workspace visual evidence: %v", err)
	}
}

func TestVisualArtifactCompletionGateHonorsDisabledVerifierMode(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-visual-policy", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-visual-policy", ProjectID: "project-visual-policy", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	imageBytes := writeVisualReviewSizedPNG(t, filepath.Join(root, "ranking.png"), 240, 140)
	if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-ranking-policy", ProjectID: "project-visual-policy", Name: "ranking.png",
		ContentType: "image/png", Content: bytes.NewReader(imageBytes), CreatedBy: "runner",
		RootFrameID: "frame-visual-policy", FrameID: "frame-visual-policy",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	disabled := sessionstore.Session{ID: "frame-visual-policy", WorkDir: root, Orchestration: map[string]any{
		"sessionConfig": map[string]any{"verifier_mode": "off"},
	}}
	if err := srv.verifySessionRunnerVisualArtifactEvidence(disabled); err != nil {
		t.Fatalf("disabled verifier unexpectedly required visual evidence: %v", err)
	}
	enabled := disabled
	enabled.Orchestration = map[string]any{"sessionConfig": map[string]any{"verifier_mode": "on"}}
	if err := srv.verifySessionRunnerVisualArtifactEvidence(enabled); err == nil {
		t.Fatal("enabled verifier accepted an unreviewed image artifact")
	} else {
		var required *sessionRunnerVisualArtifactValidationRequired
		if !errors.As(err, &required) || len(required.Artifacts) != 1 || required.Artifacts[0] != "ranking.png" {
			t.Fatalf("enabled verifier error=%#v", err)
		}
	}
}

func TestVisualArtifactCompletionGateRequiresExactPassedImageHash(t *testing.T) {
	root := t.TempDir()
	imageBytes := writeVisualReviewSizedPNG(t, filepath.Join(root, "ranking.png"), 240, 140)
	imageDigest := sha256.Sum256(imageBytes)
	digest := hex.EncodeToString(imageDigest[:])
	artifact := sessionReviewerArtifactEvidence{
		ArtifactID: "artifact-ranking", VersionID: "version-ranking", Name: "ranking.png",
		Kind: "image", ContentSHA256: digest,
	}
	srv := New(Options{FileRoot: root})
	if err := srv.verifyVisualArtifactEvidence([]sessionReviewerArtifactEvidence{artifact}); err == nil {
		t.Fatal("unreviewed image artifact passed completion gate")
	} else {
		var required *sessionRunnerVisualArtifactValidationRequired
		if !errors.As(err, &required) || len(required.Artifacts) != 1 || required.Artifacts[0] != "ranking.png" {
			t.Fatalf("unreviewed image error=%#v", err)
		}
		condition := required.runnerCorrection().Condition
		if condition == nil || condition.Visual == nil || len(condition.Visual.Artifacts) != 1 || condition.Visual.Artifacts[0].VersionID != artifact.VersionID || condition.Visual.Artifacts[0].SHA256 != digest {
			t.Fatal("visual correction lost immutable version/digest")
		}
	}

	manifest, err := json.Marshal(map[string]any{
		"schema": visualReviewLayoutSchema, "image_path": "ranking.png", "image_sha256": digest,
		"canvas": map[string]any{"width": 240, "height": 140},
		"text_boxes": []any{
			map[string]any{"id": "title", "text": "Ranking", "x0": 70, "y0": 8, "x1": 170, "y1": 28},
			map[string]any{"id": "label-a", "text": "A", "x0": 30, "y0": 90, "x1": 45, "y1": 110},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ranking.layout.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := srv.executeVisualReviewTool(withVisualReviewWorkspaceRoot(context.Background(), root), map[string]any{
		"action": "validate_layout", "objective": "validate ranking chart",
		"image_path": "ranking.png", "render_manifest_path": "ranking.layout.json",
	})
	if err != nil || mapValue(result)["status"] != "passed" {
		t.Fatalf("layout validation result=%#v err=%v", result, err)
	}
	if err := srv.verifyVisualArtifactEvidence([]sessionReviewerArtifactEvidence{artifact}); err != nil {
		t.Fatalf("hash-bound reviewed image did not pass completion gate: %v", err)
	}

	tampered := artifact
	tampered.ContentSHA256 = hex.EncodeToString(sha256.New().Sum(nil))
	if err := srv.verifyVisualArtifactEvidence([]sessionReviewerArtifactEvidence{tampered}); err == nil {
		t.Fatal("different artifact bytes reused an unrelated visual validation")
	}
	if err := srv.verifyVisualArtifactEvidence([]sessionReviewerArtifactEvidence{{Name: "table.csv"}}); err != nil {
		t.Fatalf("non-visual artifact unexpectedly required visual validation: %v", err)
	}
}

func TestVisualReviewRecordPassedRejectsMetadataOnlySemanticAssessment(t *testing.T) {
	responseHash := sha256.Sum256([]byte("2345ABCD"))
	base := map[string]any{
		"action": "record_assessment", "verdict": "pass", "visual_verified": true,
		visualReviewChallengeHashField: hex.EncodeToString(responseHash[:]),
		"assessment":                   map[string]any{"visual_challenge_response": "2345ABCD"},
	}
	if !visualReviewRecordPassed(base) {
		t.Fatal("valid model-visible challenge record was rejected")
	}
	delete(base, visualReviewChallengeHashField)
	if visualReviewRecordPassed(base) {
		t.Fatal("metadata-only semantic assessment passed without the visual challenge")
	}
}

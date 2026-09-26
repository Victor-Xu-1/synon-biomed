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
	"strings"
	"testing"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestStructureSceneManifestRequiresVisibleMotherAndDerivedLayers(t *testing.T) {
	structures := []sessionReviewerArtifactEvidence{
		{ArtifactID: "mother-artifact", VersionID: "mother-version", Name: "mother.pdb", ContentSHA256: strings.Repeat("1", 64)},
		{ArtifactID: "derived-artifact", VersionID: "derived-version", Name: "derived.pdb", ContentSHA256: strings.Repeat("2", 64)},
	}
	images := []sessionReviewerArtifactEvidence{{
		ArtifactID: "preview-artifact", VersionID: "preview-version", Name: "scene.png", ContentSHA256: strings.Repeat("3", 64),
	}}
	manifest := structureSceneManifest{
		Schema:  structureSceneManifestSchema,
		SceneID: "scene-1",
		MotherStructure: structureSceneArtifactRef{
			Name: "mother.pdb", VersionID: "mother-version",
		},
		DerivedStructures: []structureSceneArtifactRef{{Name: "derived.pdb", VersionID: "derived-version"}},
		Layers: []structureSceneLayer{
			{VersionID: "mother-version", Role: "mother", Representation: "cartoon", Visible: true},
			{VersionID: "derived-version", Role: "derived", Representation: "ball-and-stick", Visible: true},
		},
		PreviewImage: structureScenePreviewImage{
			Name: "scene.png", VersionID: "preview-version", SHA256: strings.Repeat("3", 64),
		},
	}
	if failures := validateStructureSceneManifest(manifest, structures, images); len(failures) != 0 {
		t.Fatalf("valid scene manifest rejected: %v", failures)
	}
	manifest.Layers[1].Visible = false
	failures := validateStructureSceneManifest(manifest, structures, images)
	if len(failures) == 0 || !strings.Contains(strings.Join(failures, " "), "must be visible") {
		t.Fatalf("invisible derived layer was accepted: %v", failures)
	}
}

func TestStructureSceneCompletionGateRequiresManifestForMultipleSnapshotStructures(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-scene-missing", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-scene-missing", ProjectID: "project-scene-missing", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	writeSceneTestArtifact(t, store, "mother-artifact", "project-scene-missing", "mother.pdb", "ATOM mother", "frame-scene-missing")
	writeSceneTestArtifact(t, store, "derived-artifact", "project-scene-missing", "derived.pdb", "ATOM derived", "frame-scene-missing")
	srv := New(Options{FileRoot: root, Workspace: store})
	err = srv.verifySessionRunnerVisualArtifactEvidence(sessionForSceneTest("frame-scene-missing", root))
	if err == nil {
		t.Fatal("multiple snapshot structures passed without a structure-scene manifest")
	}
	var required *sessionRunnerVisualArtifactValidationRequired
	if !errors.As(err, &required) || len(required.StructureSceneFailures) == 0 ||
		!strings.Contains(strings.Join(required.StructureSceneFailures, " "), structureSceneManifestSchema) {
		t.Fatalf("unexpected scene contract error: %#v", err)
	}
}

func TestStructureSceneCompletionGateBindsPreviewToVisualReview(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-scene-valid", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-scene-valid", ProjectID: "project-scene-valid", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	mother := writeSceneTestArtifact(t, store, "mother-artifact", "project-scene-valid", "mother.pdb", "ATOM mother", "frame-scene-valid")
	derived := writeSceneTestArtifact(t, store, "derived-artifact", "project-scene-valid", "derived.pdb", "ATOM derived", "frame-scene-valid")
	imageBytes := writeVisualReviewSizedPNG(t, filepath.Join(root, "scene.png"), 240, 140)
	imageDigest := sha256.Sum256(imageBytes)
	imageHash := hex.EncodeToString(imageDigest[:])
	imageArtifact := writeSceneTestArtifactBytes(t, store, "preview-artifact", "project-scene-valid", "scene.png", imageBytes, "image/png", "frame-scene-valid")
	manifestBytes, err := json.Marshal(structureSceneManifest{
		Schema:            structureSceneManifestSchema,
		SceneID:           "scene-1",
		MotherStructure:   structureSceneArtifactRef{Name: "mother.pdb", VersionID: mother.ID},
		DerivedStructures: []structureSceneArtifactRef{{Name: "derived.pdb", VersionID: derived.ID}},
		Layers: []structureSceneLayer{
			{VersionID: mother.ID, Role: "mother", Representation: "cartoon", Visible: true},
			{VersionID: derived.ID, Role: "derived", Representation: "ball-and-stick", Visible: true},
		},
		PreviewImage: structureScenePreviewImage{Name: "scene.png", VersionID: imageArtifact.ID, SHA256: imageHash},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeSceneTestArtifactBytes(t, store, "scene-manifest-artifact", "project-scene-valid", "scene.json", manifestBytes, "application/json", "frame-scene-valid")
	layoutBytes, err := json.Marshal(map[string]any{
		"schema": visualReviewLayoutSchema, "image_path": "scene.png", "image_sha256": imageHash,
		"canvas":     map[string]any{"width": 240, "height": 140},
		"text_boxes": []any{map[string]any{"id": "scene-label", "text": "Structure scene", "x0": 20, "y0": 20, "x1": 180, "y1": 40}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scene.layout.json"), layoutBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})
	result, err := srv.executeVisualReviewTool(withVisualReviewWorkspaceRoot(context.Background(), root), map[string]any{
		"action": "validate_layout", "objective": "validate structure overlay scene",
		"image_path": "scene.png", "render_manifest_path": "scene.layout.json",
	})
	if err != nil || mapValue(result)["status"] != "passed" {
		t.Fatalf("visual review did not pass: result=%#v err=%v", result, err)
	}
	if err := srv.verifySessionRunnerVisualArtifactEvidence(sessionForSceneTest("frame-scene-valid", root)); err != nil {
		t.Fatalf("valid structure scene was rejected: %v", err)
	}
}

func sessionForSceneTest(frameID, root string) sessionstore.Session {
	return sessionstore.Session{ID: frameID, WorkDir: root, Orchestration: map[string]any{
		"sessionConfig": map[string]any{"verifier_mode": "off"},
	}}
}

func writeSceneTestArtifact(t *testing.T, store *workspace.Store, artifactID, projectID, name, content, frameID string) workspace.ArtifactVersion {
	return writeSceneTestArtifactBytes(t, store, artifactID, projectID, name, []byte(content), "text/plain", frameID)
}

func writeSceneTestArtifactBytes(t *testing.T, store *workspace.Store, artifactID, projectID, name string, content []byte, contentType, frameID string) workspace.ArtifactVersion {
	t.Helper()
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: projectID, Name: name, ContentType: contentType,
		Content: bytes.NewReader(content), CreatedBy: "runner", RootFrameID: frameID, FrameID: frameID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

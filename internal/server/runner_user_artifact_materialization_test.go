package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestRunnerVerifiesOriginalAttachmentsWithoutEagerWorkspaceCopies(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	artifact, version := writeKernelInspectionArtifact(
		t, store, identity.access, "artifact-attached", "multi-ligands.cdx",
		"chemical/x-cdx", "VjCD0100\x04\x03\x02\x01", "runner-attached-input",
	)
	entry := eventjournal.Entry{SessionID: identity.access.Frame.ID, EventID: 1, Message: eventjournal.Message{
		"type": "user_message", "role": "user", "text": "analyze the attached structure",
		"artifactRefs": []any{map[string]any{
			"artifact_id": artifact.ID, "version_id": version.ID, "filename": artifact.Name,
			"content_type": "chemical/x-cdx", "size_bytes": version.SizeBytes,
			"checksum": version.ContentSHA256,
		}},
	}}
	projected, err := app.validateRunnerUserArtifactsForFrame(
		context.Background(), identity.access, []eventjournal.Entry{entry},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 || projected[0].Message == nil {
		t.Fatalf("projected entries=%#v", projected)
	}
	if _, err := os.Stat(filepath.Join(identity.workspaceDir, ".synon-artifacts")); !os.IsNotExist(err) {
		t.Fatalf("provider preparation created an eager attachment copy: %v", err)
	}
	modelText := runnerModelMessageText(projected[0].Message)
	if strings.Contains(modelText, `"workspace_path":`) ||
		!strings.Contains(modelText, `"read_only":true`) ||
		!strings.Contains(modelText, `"materialization":"lazy_via_version_id"`) ||
		!strings.Contains(modelText, `"sha256":"`+version.ContentSHA256+`"`) ||
		!strings.Contains(modelText, `"reader_contract":{"family":"scientific_data","format_id":"scientific_binary"`) ||
		!strings.Contains(modelText, `"status":"reader_required"`) {
		t.Fatalf("provider attachment context=%q", modelText)
	}

	corrupt := entry
	corrupt.Message = make(eventjournal.Message, len(entry.Message))
	for key, value := range entry.Message {
		corrupt.Message[key] = value
	}
	corrupt.Message["artifactRefs"] = []any{map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "filename": artifact.Name,
		"content_type": "chemical/x-cdx", "size_bytes": version.SizeBytes,
		"checksum": strings.Repeat("0", 64),
	}}
	if _, err := app.validateRunnerUserArtifactsForFrame(context.Background(), identity.access, []eventjournal.Entry{corrupt}); err == nil {
		t.Fatal("mismatched immutable attachment metadata was accepted")
	}
}

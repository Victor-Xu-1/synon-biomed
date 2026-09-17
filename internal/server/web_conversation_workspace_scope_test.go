package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestP5WebConversationWorkspaceListsOnlyCurrentConversationArtifacts(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: "project-shared", UserID: "local", Name: "project-shared",
	}); err != nil {
		t.Fatal(err)
	}
	for _, frameID := range []string{"conversation-a", "conversation-b", "conversation-empty"} {
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: "project-shared", AgentName: "OPERON", Status: "completed",
			ConversationType: "agent", Name: frameID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, frameID := range []string{"conversation-a", "conversation-b"} {
		if _, _, err := store.WriteArtifactVersionRealtime(
			workspace.WithMutationIdempotencyKey(t.Context(), "conversation-workspace-scope-"+frameID),
			workspace.WriteArtifactVersionInput{
				ArtifactID: "artifact-" + frameID, ProjectID: "project-shared", Name: frameID + ".csv",
				ContentType: "text/csv", Content: bytes.NewReader([]byte("frame," + frameID + "\n")), MaxBytes: 1 << 20,
				RootFrameID: frameID, FrameID: frameID, IsUserUpload: true,
			},
			"local",
		); err != nil {
			t.Fatal(err)
		}
	}

	app := New(Options{Workspace: store, FileRoot: filepath.Join(root, "runtime")})
	for _, test := range []struct {
		conversationID string
		wantArtifactID string
	}{
		{conversationID: "conversation-a", wantArtifactID: "artifact-conversation-a"},
		{conversationID: "conversation-b", wantArtifactID: "artifact-conversation-b"},
		{conversationID: "conversation-empty"},
	} {
		response := p5WorkspaceRequest(t, app, http.MethodGet,
			"/api/conversations/"+test.conversationID+"/workspace?path=project-files&limit=200", "")
		if response.Code != http.StatusOK {
			t.Fatalf("conversation=%s status=%d body=%s", test.conversationID, response.Code, response.Body.String())
		}
		var page struct {
			Items []struct {
				ArtifactID string `json:"artifact_id"`
				Name       string `json:"name"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("conversation=%s payload=%s err=%v", test.conversationID, response.Body.String(), err)
		}
		if len(page.Items) != boolToInt(test.wantArtifactID != "") || page.Total != len(page.Items) {
			t.Fatalf("conversation=%s page=%#v", test.conversationID, page)
		}
		if test.wantArtifactID != "" && (page.Items[0].ArtifactID != test.wantArtifactID || page.Items[0].Name != test.conversationID+".csv") {
			t.Fatalf("conversation=%s item=%#v", test.conversationID, page.Items[0])
		}
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

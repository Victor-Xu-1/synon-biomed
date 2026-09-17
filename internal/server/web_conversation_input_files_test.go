package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationMessageMaterializesAbsoluteFilesIntoManagedTaskWorkspace(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-input-files", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-input-files", ProjectID: project.ID, AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Input files",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "measurements.csv")
	want := []byte("sample,value\nA,1.25\n")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}

	response := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+frame.ID+"/messages", map[string]any{
		"content": "Analyze the measurements.\n[[SYNON_AI_FILES]]\n" + source, "files": []string{source},
	}, "local")
	if response.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
	session, found, err := app.sessionStore.Get(frame.ID)
	if err != nil || !found {
		t.Fatalf("session found=%v err=%v", found, err)
	}
	inputData, _ := session.Orchestration["inputData"].(map[string]any)
	files, _ := inputData["files"].([]any)
	if len(files) != 1 || files[0] != "inputs/measurements.csv" {
		t.Fatalf("materialized files=%#v input=%#v", files, inputData)
	}
	request, _ := inputData["request"].(string)
	if strings.Contains(request, source) || !strings.Contains(request, "inputs/measurements.csv") {
		t.Fatalf("materialized request=%q", request)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), frame.ID)
	if err != nil || !found {
		t.Fatalf("frame access found=%v err=%v", found, err)
	}
	taskRoot, err := app.defaultAgentKernelTaskWorkspace(
		access.Frame.ProjectID, access.Frame.RootFrameID, access.RootFrameIncarnationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(taskRoot, "inputs", "measurements.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("materialized bytes=%q want=%q", got, want)
	}
	if original, err := os.ReadFile(source); err != nil || string(original) != string(want) {
		t.Fatalf("source changed bytes=%q err=%v", original, err)
	}
}

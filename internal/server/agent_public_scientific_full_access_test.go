package server

import (
	"context"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestPublicScientificRedirectPolicyFollowsConversationFullAccess(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	frameID := "frame-scientific-full-access"
	seedTranscriptWebFrame(t, store, "local", "project-scientific-full-access", frameID)
	seedPermissionTestAgent(t, store, "local")
	setMode := func(mode string) {
		t.Helper()
		if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
			FrameID: frameID,
			ContextData: map[string]any{"web_assistant": map[string]any{
				"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": mode},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	setMode("allow")
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	if !server.agentPublicScientificAllowsPublicRedirects(frameID) {
		t.Fatal("full-access conversation did not enable public scientific redirects")
	}
	request := agentPublicScientificFileRequest{
		SourceHost: "github.com", AcceptedTypes: []string{"application/octet-stream"},
		AllowPublicRedirects: true,
	}
	if policy := server.agentPublicScientificTransferPolicy(request); !policy.AllowPublicRedirects {
		t.Fatal("full-access redirect authority did not reach secure fetch policy")
	}
	if !agentPublicScientificResponseHostAllowed(request, "release-assets.githubusercontent.com") {
		t.Fatal("public release asset host was rejected in full-access mode")
	}
	for _, host := range []string{"localhost", "service.local", "127.0.0.1"} {
		if agentPublicScientificResponseHostAllowed(request, host) {
			t.Fatalf("protected redirect host %q was allowed", host)
		}
	}

	setMode("smart")
	if server.agentPublicScientificAllowsPublicRedirects(frameID) {
		t.Fatal("smart permission mode inherited full-access redirect authority")
	}
}

package server

import (
	"context"
	"slices"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestAgentKernelEgressPolicyUsesOneConfiguredProxyAndExcludesRepl(t *testing.T) {
	server := New(Options{
		FileRoot:             t.TempDir(),
		ConfigAllowedDomains: []string{"api.example.com"},
		ConfigDeniedDomains:  []string{"blocked.example.com"},
		ConfigNetworkProxy:   "http://proxy.example:8080",
		MCPX509Posture: func() mcpstdio.TLSPosture {
			return mcpstdio.TLSPosture{Strict: false, CABundle: "/operator/company.pem"}
		},
	})
	allowed, denied, bundle, proxy, err := server.agentKernelEgressPolicy("python", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(allowed, "api.example.com") || !slices.Contains(denied, "blocked.example.com") ||
		bundle != "/operator/company.pem" || proxy != "http://proxy.example:8080" {
		t.Fatalf("kernel egress policy = allowed:%#v denied:%#v bundle:%q proxy:%q", allowed, denied, bundle, proxy)
	}
	allowed, denied, bundle, proxy, err = server.agentKernelEgressPolicy("repl", "")
	if err != nil || len(allowed) != 0 || len(denied) != 0 || bundle != "" || proxy != "" {
		t.Fatalf("repl egress policy = allowed:%#v denied:%#v bundle:%q proxy:%q err:%v", allowed, denied, bundle, proxy, err)
	}
}

func TestAgentKernelEgressPolicyFullAccessAllowsPublicHTTPSOnly(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	frameID := "frame-full-access-egress"
	seedTranscriptWebFrame(t, store, "local", "project-full-access-egress", frameID)
	seedPermissionTestAgent(t, store, "local")
	if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
		FrameID: frameID,
		ContextData: map[string]any{"web_assistant": map[string]any{
			"id": "synonbiomed:operon", "conversation_overrides": map[string]any{"permission": "allow"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	allowed, _, _, _, err := server.agentKernelEgressPolicy("python", frameID)
	if err != nil || !slices.Contains(allowed, "*") {
		t.Fatalf("full-access public egress=%#v err=%v", allowed, err)
	}
	if repl, _, _, _, err := server.agentKernelEgressPolicy("repl", frameID); err != nil || len(repl) != 0 {
		t.Fatalf("repl public egress=%#v err=%v", repl, err)
	}
}

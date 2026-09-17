package server

import (
	"context"
	"testing"
)

func TestNormalizeWebAssistantPermissionModeUsesOneCanonicalWireMapping(t *testing.T) {
	tests := []struct {
		name string
		wire string
		want string
	}{
		{name: "default", wire: "default", want: "default"},
		{name: "smart ui label", wire: "smart", want: "smart"},
		{name: "legacy ask", wire: "ask", want: "default"},
		{name: "legacy confirm", wire: "confirm", want: "smart"},
		{name: "Claude compatible accept edits", wire: "acceptEdits", want: "smart"},
		{name: "full ui label", wire: "bypassPermissions", want: "allow"},
		{name: "expert full access alias", wire: "full-access", want: "allow"},
		{name: "no sandbox full access alias", wire: "yoloNoSandbox", want: "allow"},
		{name: "legacy allow", wire: "allow", want: "allow"},
		{name: "deny", wire: "deny", want: "deny"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeWebAssistantPermissionMode(test.wire)
			if !ok || got != test.want {
				t.Fatalf("normalizeWebAssistantPermissionMode(%q) = %q, %t; want %q, true", test.wire, got, ok, test.want)
			}
		})
	}
}

func TestNormalizeWebAssistantPermissionModeRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"", "unknown", "unsafe-mystery-mode"} {
		if got, ok := normalizeWebAssistantPermissionMode(value); ok || got != "" {
			t.Fatalf("normalizeWebAssistantPermissionMode(%q) = %q, %t; want empty, false", value, got, ok)
		}
	}
}

func TestWebConversationPermissionOptionValueMapsCanonicalModesToStableUIValues(t *testing.T) {
	tests := map[string]string{
		"":        "default",
		"default": "default",
		"confirm": "smart",
		"smart":   "smart",
		"allow":   "bypassPermissions",
		"deny":    "deny",
	}
	for canonical, want := range tests {
		if got := webConversationPermissionOptionValue(canonical); got != want {
			t.Fatalf("webConversationPermissionOptionValue(%q) = %q; want %q", canonical, got, want)
		}
	}
}

func TestWebConversationPermissionRuntimeModeTreatsDefaultAsApprovalRequired(t *testing.T) {
	tests := map[string]string{
		"default": "ask",
		"ask":     "ask",
		"confirm": "smart",
		"smart":   "smart",
		"allow":   "allow",
		"deny":    "deny",
	}
	for selection, want := range tests {
		if got := webConversationPermissionRuntimeMode(selection); got != want {
			t.Fatalf("webConversationPermissionRuntimeMode(%q) = %q; want %q", selection, got, want)
		}
	}
}

func TestWebPermissionDecisionDefinesDistinctThreeTierSemantics(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		risky bool
		want  string
	}{
		{name: "request approval routine", mode: "ask", want: "ask"},
		{name: "request approval risky", mode: "ask", risky: true, want: "ask"},
		{name: "smart routine", mode: "smart", want: "allow"},
		{name: "smart risky", mode: "smart", risky: true, want: "ask"},
		{name: "full routine", mode: "allow", want: "allow"},
		{name: "full risky", mode: "allow", risky: true, want: "allow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := webPermissionDecision(test.mode, test.risky)
			if err != nil || got != test.want {
				t.Fatalf("decision=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
}

func TestWebPendingPermissionRiskMatchesSmartModeContract(t *testing.T) {
	tests := []struct {
		name         string
		confirmation map[string]any
		wantRisky    bool
	}{
		{name: "sandboxed local execution", confirmation: map[string]any{"kind": "local_exec"}},
		{name: "task local edit", confirmation: map[string]any{"kind": agentToolApprovalKind, "tool": "edit_file"}},
		{name: "network expansion", confirmation: map[string]any{"kind": "network"}, wantRisky: true},
		{name: "capability install", confirmation: map[string]any{"kind": "capability_install"}, wantRisky: true},
		{name: "artifact deletion", confirmation: map[string]any{"kind": "artifact_delete"}, wantRisky: true},
		{name: "unknown approval fails safe", confirmation: map[string]any{"kind": "future_permission"}, wantRisky: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := webPendingPermissionRisky(test.confirmation); got != test.wantRisky {
				t.Fatalf("risk=%t want=%t", got, test.wantRisky)
			}
		})
	}
}

func TestAgentRuntimeSmartApprovalRiskFailsSafeForUnclassifiedGovernedTools(t *testing.T) {
	for _, toolName := range []string{
		"edit_file", "file_write", "Write", "Edit", "Patch", "file_patch", "file_replace", "json_patch",
		"NotebookEdit", "file_copy", "file_mkdir", "shell_exec", "bash", "Shell", "powershell", "python", "r", "repl",
	} {
		if agentRuntimeSmartApprovalRisk(toolName, nil) {
			t.Fatalf("routine sandboxed tool %q was classified as risky", toolName)
		}
	}
	if !agentRuntimeSmartApprovalRisk("future_sensitive_tool", nil) {
		t.Fatal("an unclassified governed tool must fail safe as risky")
	}
}

func TestCompatibilityApprovalResolutionAuthorityDistinguishesPolicyFromManualInput(t *testing.T) {
	manual := compatibilityApprovalAuthorityFromContext(context.Background(), "owner")
	if manual.Source != "user" || manual.ActorID != "owner" {
		t.Fatalf("manual authority=%#v", manual)
	}
	policyContext := withCompatibilityApprovalResolutionAuthority(context.Background(), compatibilityApprovalResolutionAuthority{
		Source: "policy", ActorID: "system",
	})
	policy := compatibilityApprovalAuthorityFromContext(policyContext, "owner")
	if policy.Source != "policy" || policy.ActorID != "system" {
		t.Fatalf("policy authority=%#v", policy)
	}
}

func TestWebConfirmationRequestPendingMatchesDurableRequestIdentity(t *testing.T) {
	confirmations := []map[string]any{{"id": "approval-a"}, {"id": "approval-b"}}
	if !webConfirmationRequestPending(confirmations, "approval-b") {
		t.Fatal("existing approval was not found")
	}
	if webConfirmationRequestPending(confirmations, "approval-c") {
		t.Fatal("missing approval was reported as pending")
	}
}

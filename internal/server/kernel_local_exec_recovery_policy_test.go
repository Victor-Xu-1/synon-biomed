package server

import (
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestRememberedKernelApprovalOnlyReleasesExplicitAllowPolicy(t *testing.T) {
	resolution := workspace.KernelLocalExecApprovalResolutionInput{Tool: "python", Scope: "once"}
	approved, ok := rememberedKernelLocalExecApproval(resolution, workspace.ApprovalPolicyDecision{
		Found: true, Tier: "allow", Scope: "project", ScopeTargetID: "project-a",
	})
	if !ok || !approved.Approved || approved.Scope != "project" || approved.RequireStaleRunner {
		t.Fatalf("remembered approval=%#v ok=%t", approved, ok)
	}
	for _, policy := range []workspace.ApprovalPolicyDecision{
		{},
		{Found: true, Tier: "ask", Scope: "project"},
		{Found: true, Tier: "deny", Scope: "always"},
	} {
		if got, allowed := rememberedKernelLocalExecApproval(resolution, policy); allowed {
			t.Fatalf("non-allow policy released approval: %#v -> %#v", policy, got)
		}
	}
	software := resolution
	software.Tool = softwareRuntimeToolName
	if got, allowed := rememberedKernelLocalExecApproval(software, workspace.ApprovalPolicyDecision{
		Found: true, Tier: "allow", Scope: "project",
	}); allowed {
		t.Fatalf("software runtime persistent policy bypassed once-only gate: %#v", got)
	}
}

func TestRememberedKernelOperationScopePreservesOnceOnlySoftwareBoundary(t *testing.T) {
	operation := workspace.KernelLocalOperation{Tool: "python"}
	if scope, ok := rememberedKernelLocalOperationScope(operation, workspace.ApprovalPolicyDecision{
		Found: true, Tier: "allow", Scope: "always",
	}); !ok || scope != "always" {
		t.Fatalf("remembered operation scope=%q ok=%t", scope, ok)
	}
	operation.Tool = softwareRuntimeToolName
	if scope, ok := rememberedKernelLocalOperationScope(operation, workspace.ApprovalPolicyDecision{
		Found: true, Tier: "allow", Scope: "project",
	}); ok {
		t.Fatalf("software runtime persistent scope=%q bypassed once-only gate", scope)
	}
}

func TestKernelLocalExecRecoveryWakesOnlyActiveDispatches(t *testing.T) {
	for _, status := range []string{"registered", "claimed", "blocked", " REGISTERED "} {
		if !kernelLocalExecRecoveryDispatchActive(status) {
			t.Fatalf("active recovery dispatch status %q was not recognized", status)
		}
	}
	for _, status := range []string{"", "completed", "failed", "cancelled"} {
		if kernelLocalExecRecoveryDispatchActive(status) {
			t.Fatalf("terminal recovery dispatch status %q remained active", status)
		}
	}
}

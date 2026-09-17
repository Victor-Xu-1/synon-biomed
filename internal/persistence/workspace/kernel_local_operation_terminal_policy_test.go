package workspace

import "testing"

func TestDetachedKernelReceiptAllowsServerJSONPolicySettlement(t *testing.T) {
	files := []map[string]any{{
		"path": "probe.json", "policy_code": "json_workspace_file_forbidden", "policy_action": "removed",
	}}
	if !detachedKernelReceiptTerminalStateCompatible(
		KernelExecutionResultCompleted, KernelLocalOperationStateFailed, "ok", files,
	) {
		t.Fatal("server policy failure should settle a physically completed detached receipt")
	}
	if detachedKernelReceiptTerminalStateCompatible(
		KernelExecutionResultCompleted, KernelLocalOperationStateFailed, "ok", []map[string]any{{"path": "probe.json"}},
	) {
		t.Fatal("ordinary completed receipt must not be remapped to failed")
	}
}

func TestDetachedKernelReceiptAllowsReceiptBackedSemanticFailureSettlement(t *testing.T) {
	if !detachedKernelReceiptTerminalStateCompatible(
		KernelExecutionResultCompleted, KernelLocalOperationStateFailed, "error", nil,
	) {
		t.Fatal("a physically completed wrapper must allow its receipt-backed non-ok execution log to settle failed")
	}
	if detachedKernelReceiptTerminalStateCompatible(
		KernelExecutionResultCompleted, KernelLocalOperationStateCancelled, "error", nil,
	) {
		t.Fatal("a completed receipt cannot be remapped to cancellation")
	}
	if detachedKernelReceiptTerminalStateCompatible(
		KernelExecutionResultFailed, KernelLocalOperationStateCompleted, "ok", nil,
	) {
		t.Fatal("a failed receipt cannot be remapped to completion")
	}
}

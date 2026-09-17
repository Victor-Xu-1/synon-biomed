package failurecontract

import "testing"

func TestKindForDetailCodeIsClosedAndDeterministic(t *testing.T) {
	for code, want := range map[string]Kind{
		"software_dependency_unavailable":   NotFound,
		"software_install_failed":           ImageBuildFailed,
		"software_api_contract_mismatch":    InvalidRequest,
		"software_output_validation_failed": ResultRejected,
		"software_install_timeout":          Transient,
		"software_network_denied":           NetworkDenied,
		"permission_denied":                 Unauthorized,
		"unknown_provider_detail":           ResultRejected,
	} {
		if got := KindForDetailCode(code); got != want || !Valid(got) {
			t.Fatalf("KindForDetailCode(%q)=%q want=%q", code, got, want)
		}
	}
}

func TestApplyTerminalJobFailureClosesCurrentExecutionUnit(t *testing.T) {
	value := map[string]any{"ok": false, "code": "nonzero_exit", "retryable": true}
	if kind := ApplyTerminalJobFailure(value, "nonzero_exit"); kind != ResultRejected ||
		value["terminal"] != true || value["retryable"] != false ||
		value["next_action"] != "inspect_contract_then_start_new_execution" {
		t.Fatalf("terminal envelope=%#v kind=%q", value, kind)
	}
}

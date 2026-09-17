package server

import (
	"strings"
	"testing"
)

func TestAgentWorkspaceArtifactSelfReferencePreflightClosesVersionChase(t *testing.T) {
	preflight := agentWorkspaceArtifactSelfReferencePreflight(map[string]any{
		"file_path":  "reports/result.md",
		"old_string": "1. [result.md]({{artifact:210824cb-85c5-4b3b-a4e1-baf253000ebd}}) - report",
		"new_string": "1. [result.md]({{artifact:9126a810-3fec-4cde-a81c-2abd131b1e13}}) - report",
	})
	if preflight == nil || preflight["ok"] != true || preflight["executed"] != false ||
		preflight["status"] != "no_change_required" {
		t.Fatalf("expected successful no-op self-reference preflight, got %#v", preflight)
	}
}

func TestAgentWorkspaceArtifactSelfReferencePreflightAllowsSubstantiveEdits(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"other artifact": {
			"file_path":  "reports/result.md",
			"old_string": "[data.csv]({{artifact:210824cb-85c5-4b3b-a4e1-baf253000ebd}})",
			"new_string": "[data.csv]({{artifact:9126a810-3fec-4cde-a81c-2abd131b1e13}})",
		},
		"content change": {
			"file_path":  "reports/result.md",
			"old_string": "[result.md]({{artifact:210824cb-85c5-4b3b-a4e1-baf253000ebd}}) old",
			"new_string": "[result.md]({{artifact:9126a810-3fec-4cde-a81c-2abd131b1e13}}) corrected",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if preflight := agentWorkspaceArtifactSelfReferencePreflight(input); preflight != nil {
				t.Fatalf("substantive edit was rejected: %#v", preflight)
			}
		})
	}
}

func TestAgentRuntimeWorkspaceArtifactReferencePreflightRejectsInventedMarkersBeforeApproval(t *testing.T) {
	preflight := agentRuntimeWorkspaceArtifactReferencePreflight("edit_file", map[string]any{
		"file_path":  "clinical_report.md",
		"old_string": "![PK](pk_curve.png)",
		"new_string": "![PK]({{artifact:art_8dee9863-b92d-4d25-b552-e14438342fe7}})",
	})
	if preflight == nil || preflight["executed"] != false ||
		preflight["status"] != "artifact_reference_preflight_required" {
		t.Fatalf("invented artifact marker preflight=%#v", preflight)
	}
	if allowed := agentRuntimeWorkspaceArtifactReferencePreflight("edit_file", map[string]any{
		"file_path":  "clinical_report.md",
		"old_string": "![PK](old.png)",
		"new_string": "![PK](pk_concentration_time_curve.png)",
	}); allowed != nil {
		t.Fatalf("relative companion path was rejected: %#v", allowed)
	}
}

func TestArtifactToolDescriptionsForbidSelfReferenceVersionChasing(t *testing.T) {
	for name, description := range map[string]string{
		"edit_file":      agentWorkspaceEditFileToolSchema().Description,
		"save_artifacts": agentSaveArtifactsToolSchema().Description,
	} {
		for _, required := range []string{"final answer", "own", "reference"} {
			if !strings.Contains(strings.ToLower(description), required) {
				t.Fatalf("%s description is missing %q: %s", name, required, description)
			}
		}
	}
	if !strings.Contains(agentWorkspaceEditFileToolSchema().Description, "old_string MUST be empty") {
		t.Fatal("edit_file does not define complete rewrite semantics")
	}
}

package server

import "testing"

func TestToolDoctorAcceptsComputeCompatibilityScope(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	result, err := srv.executeToolDoctorTool(map[string]any{"scope": "compute"})
	if err != nil {
		t.Fatalf("ToolDoctor compute scope error = %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok || payload["ok"] != true || payload["scope"] != "compute" {
		t.Fatalf("ToolDoctor compute payload = %#v", result)
	}
	checks, ok := payload["checks"].([]map[string]any)
	if !ok || len(checks) != 1 || checks[0]["name"] != "durable-kernel-runner" {
		t.Fatalf("ToolDoctor compute checks = %#v", payload["checks"])
	}
}

func TestToolDoctorCanonicalizesScientificComputeAliases(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	for _, alias := range []string{"kernel-compute", "molecular-docking"} {
		result, err := srv.executeToolDoctorTool(map[string]any{"scope": alias})
		if err != nil {
			t.Fatalf("ToolDoctor alias %q error = %v", alias, err)
		}
		payload, ok := result.(map[string]any)
		if !ok || payload["ok"] != true || payload["scope"] != "compute" {
			t.Fatalf("ToolDoctor alias %q payload = %#v", alias, result)
		}
		checks, ok := payload["checks"].([]map[string]any)
		if !ok || len(checks) != 1 || checks[0]["scope"] != "compute" || checks[0]["name"] != "durable-kernel-runner" {
			t.Fatalf("ToolDoctor alias %q checks = %#v", alias, payload["checks"])
		}
	}
}

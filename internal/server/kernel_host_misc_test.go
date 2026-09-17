package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	compute "synon-go/internal/compute"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostMiscCapabilitiesCredentialsFindingsStatsAndStructuredOutput(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if _, err := app.secretStore.Create(secretstore.Secret{
		ID: "openalex", UserID: identity.access.UserID, Provider: "openalex", Name: "openalex",
		Credentials: map[string]string{"api_key": "fixture-openalex-key"},
	}); err != nil {
		t.Fatal(err)
	}
	claim := "The saved report needs one correction."
	if err := store.AppendVerificationCheck(identity.access.Frame.RootFrameID, workspace.VerificationCheck{
		ID: "finding-1", Claim: &claim, Verdict: "warn", Status: "open",
	}); err != nil {
		t.Fatal(err)
	}
	result := runKernelHostCell(t, app, identity, `
import json
caps = host.capabilities()
listed = host.credentials.list()
credential = host.credentials.get("openalex")
requested = host.credentials.request("openalex")
findings = host.findings()
addressed = host.findings.mark_addressed(["finding-1"], note="Corrected the saved report")
stats = host.get_local_compute_stats()
schema = host.query.schema()
rows = host.query("SELECT id FROM projects")
submitted = host.submit_output({"answer": 42}, completion_bullets=["Recorded the result"])
print(json.dumps({
    "cap": caps.get("host.compute.create"),
    "listed": listed[0]["provider"],
    "credential": credential["api_key"],
    "requested": requested,
    "finding": findings[0]["id"],
    "addressed": addressed["status"],
    "cores": stats["machine"]["cores"],
    "query_table": "projects" in schema,
    "query_project": rows["rows"][0][0],
    "submitted": submitted["status"],
}, sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("misc host result=%#v", result)
	}
	stdout := stringValue(result["stdout"])
	for _, expected := range []string{
		`"cap": true`, `"listed": "openalex"`, `"credential": "fixture-openalex-key"`,
		`"requested": "fixture-openalex-key"`, `"finding": "finding-1"`,
		`"addressed": "pending_review"`, `"submitted": "success"`,
		`"query_table": true`, `"query_project": "project-routine"`,
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("misc host stdout missing %q: %s", expected, stdout)
		}
	}
	checks, err := store.ListVerificationChecks(identity.access.Frame.RootFrameID, "resolved")
	if err != nil || len(checks) != 1 || checks[0].ID != "finding-1" {
		t.Fatalf("resolved findings=%#v err=%v", checks, err)
	}
}

func TestKernelHostManagedEndpointFreePortAndIdempotentRegistration(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if _, err := store.SetComputeBioNeMoSettings(workspace.ComputeBioNeMoSettings{
		Enabled: true, Mode: "local", HostedHost: "https://health.api.nvidia.com",
	}); err != nil {
		t.Fatal(err)
	}
	registration := compute.ManagedEndpointRegistration{
		Name: "fixture-model", URL: "http://127.0.0.1:24567", Port: 24567,
		CredentialName: "NVIDIA_API_KEY", SkillName: "fixture-model-skill",
		StartScript: "docker start fixture-model", StopScript: "docker stop fixture-model", LivePath: "/health/ready",
	}
	if err := store.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: registration.Name, URL: registration.URL, Port: registration.Port, State: "stopped", Location: "local",
		SkillName: registration.SkillName, CredentialName: &registration.CredentialName, LivePath: registration.LivePath,
		StartScript: registration.StartScript, StopScript: registration.StopScript,
		ApprovedScriptHash: compute.ApprovedManagedEndpointHash(registration), ServiceDir: t.TempDir(), RegisteredBy: identity.access.UserID,
	}); err != nil {
		t.Fatal(err)
	}
	portResult, err := app.handleKernelMiscHostCall(context.Background(), identity.access, "host.model_endpoints.free_port", nil, nil)
	if err != nil || numberValue(mapValue(portResult)["port"]) < 20000 || numberValue(mapValue(portResult)["port"]) > 29999 {
		t.Fatalf("free_port=%#v err=%v", portResult, err)
	}
	registered, err := app.handleKernelMiscHostCall(context.Background(), identity.access, "host.model_endpoints.register", []any{map[string]any{
		"name": registration.Name, "url": registration.URL, "skill": registration.SkillName,
		"credential": registration.CredentialName, "start": registration.StartScript,
		"stop": registration.StopScript, "live": registration.LivePath,
	}}, nil)
	if err != nil || mapValue(registered)["registered"] != true || mapValue(registered)["changed"] != false {
		t.Fatalf("idempotent registration=%#v err=%v", registered, err)
	}
}

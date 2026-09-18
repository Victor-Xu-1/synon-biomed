package server

import (
	"context"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
)

func TestManagedPackagePreflightUsesSourceRuntimeLanguage(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	for _, mode := range []string{"preflight", "install"} {
		authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{Name: "native", Language: "r", Status: "ready"}}
		_, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity,
			agentruntime.ToolCall{ID: "runtime-language-" + mode}, managePackagesToolName,
			map[string]any{"mode": mode, "environment": "native", "packages": []any{"r-jsonlite"}, "resource_requirements": managedEnvironmentTestResources(), "human_description": "Prepare runtime"}, authority)
		if err != nil || authority.listQuery.Language != "r" {
			t.Fatalf("%s considered the wrong runtime: query=%#v error=%v", mode, authority.listQuery, err)
		}
	}
}

func TestManagedEnvironmentMixedPhaseInterpreterContract(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	for _, packages := range [][]any{{"r-base"}, {"r-base", "pip::bridge==1.0"}} {
		authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{Name: "mixed", Language: "r", Status: "ready"}}
		input := map[string]any{"mode": "create", "name": "mixed", "language": "r", "python_version": "3.12", "packages": packages, "human_description": "Prepare mixed runtime"}
		if len(packages) == 1 {
			input["pip_phases"] = []any{[]any{"bridge==1.0"}}
		}
		_, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity,
			agentruntime.ToolCall{ID: "mixed-phase"}, manageEnvironmentsToolName, input, authority)
		if err != nil || authority.createInput.Language != "r" || authority.createInput.PythonVersion != "3.12" {
			t.Fatalf("phase interpreter contract rejected/lost: input=%#v error=%v", authority.createInput, err)
		}
	}
}

package server

import (
	"context"
	"reflect"
	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"testing"
)

func TestEnvironmentInventoryIgnoresNonExecutingNetworkOption(t *testing.T) {
	for _, network := range []string{"", "egress", "none"} {
		t.Run("network="+network, func(t *testing.T) {
			server, identity := managedEnvironmentToolFixture(t)
			authority := &recordingManagedEnvironmentAuthority{listResult: []kernelruntime.ManagedEnvironment{{Name: "existing", Language: "python", Status: "ready"}}}
			result, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: "inventory"}, manageEnvironmentsToolName, map[string]any{
				"mode": "list", "provider": "local-conda", "language": "python", "network": network, "human_description": "Inspect available environments",
			}, authority)
			if err != nil {
				t.Fatalf("read-only inventory rejected optional network metadata: %v", err)
			}
			if got := mapValue(result)["count"]; got != 1 {
				t.Fatalf("inventory count=%v", got)
			}
			if authority.listQuery.Language != "python" {
				t.Fatal("language query was lost")
			}
			if authority.createInput.Name != "" || !reflect.DeepEqual(authority.installInput, kernelruntime.MutateManagedPackagesInput{}) || authority.deleteName != "" {
				t.Fatal("inventory performed an environment mutation")
			}
		})
	}
}

func TestEnvironmentInventoryDoesNotRelaxProviderOrMutationValidation(t *testing.T) {
	for _, tc := range []struct{ name, mode, provider, image, network string }{
		{"image belongs to another provider", "list", "local-conda", "example/engine:v1", ""},
		{"unknown provider", "list", "unknown-provider", "", "egress"},
		{"creation cannot promise network isolation", "create", "local-conda", "", "none"},
		{"preflight cannot change provider through image", "preflight", "local-conda", "example/engine:v1", "egress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, identity := managedEnvironmentToolFixture(t)
			authority := &recordingManagedEnvironmentAuthority{}
			_, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: "invalid-request"}, manageEnvironmentsToolName, map[string]any{
				"mode": tc.mode, "provider": tc.provider, "image": tc.image, "network": tc.network, "name": "example", "packages": []any{"python"}, "human_description": "Inspect environment request",
			}, authority)
			if err == nil {
				t.Fatal("invalid provider or mutating network contract was accepted")
			}
			if authority.createInput.Name != "" || !reflect.DeepEqual(authority.installInput, kernelruntime.MutateManagedPackagesInput{}) {
				t.Fatal("rejected request mutated an environment")
			}
		})
	}
}

func TestEnvironmentPreflightAcceptsNonExecutingNetworkOption(t *testing.T) {
	for _, network := range []string{"", "egress", "none"} {
		t.Run(network, func(t *testing.T) {
			server, identity := managedEnvironmentToolFixture(t)
			authority := &recordingManagedEnvironmentAuthority{}
			result, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: "preflight"}, manageEnvironmentsToolName, map[string]any{
				"mode": "preflight", "provider": "local-conda", "network": network,
				"name": "candidate", "implementation": "engine:v1", "packages": []any{"example-library"},
				"resource_requirements": managedEnvironmentTestResources(), "human_description": "Inspect candidate resources",
			}, authority)
			if err != nil || mapValue(result)["mode"] != "preflight" || mapValue(result)["implementation"] != "engine:v1" {
				t.Fatalf("read-only preflight rejected or lost identity: %#v, %v", result, err)
			}
			if authority.createInput.Name != "" || !reflect.DeepEqual(authority.installInput, kernelruntime.MutateManagedPackagesInput{}) || authority.deleteName != "" {
				t.Fatal("preflight mutated an environment")
			}
		})
	}
}

func TestEnvironmentDefaultEgressReachesSelectionAuthority(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	run := &sessionRunnerChatRun{RequiredScientificCapabilities: []string{"scientific-generation"}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(ctx, identity, agentruntime.ToolCall{ID: "create"}, manageEnvironmentsToolName, map[string]any{
		"mode": "create", "provider": "local-conda", "network": "egress", "name": "candidate",
		"implementation": "engine:v1", "packages": []any{"example-library"}, "human_description": "Prepare selected environment",
	}, authority)
	if err != nil || mapValue(result)["status"] != "implementation_selection_required" {
		t.Fatalf("default egress must reach unchanged selection authority: %#v, %v", result, err)
	}
	if authority.createInput.Name != "" {
		t.Fatal("network normalization bypassed the user selection")
	}
}

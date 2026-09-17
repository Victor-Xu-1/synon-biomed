package realtime

import (
	"reflect"
	"testing"

	"synon-go/internal/compat/contracts"
)

func TestRealtimeCatalogExactlyCoversRecoveredBaseline(t *testing.T) {
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
	if len(EventSpecs()) != 47 || len(QueryContracts()) != 60 {
		t.Fatalf("catalog counts events=%d queries=%d", len(EventSpecs()), len(QueryContracts()))
	}
	for _, event := range contracts.EventTypes {
		spec, found := LookupEvent(event.Name)
		if !found || string(spec.Kind) != event.Kind {
			t.Fatalf("event %s spec=%#v found=%v", event.Name, spec, found)
		}
	}
}

func TestSynonWebRealtimeExtensionsRemainSeparateFromRecoveredBaseline(t *testing.T) {
	extensions := ExtensionEventSpecs()
	wantExtensions := []string{
		"confirmation.add", "confirmation.remove", "confirmation.update", "conversation.historyRebased",
		"conversation.listChanged", "message.stream", "message.userCreated", "runtime.statusChanged", "turn.completed",
	}
	if len(extensions) != len(wantExtensions) {
		t.Fatalf("web realtime extension count=%d want=%d: %#v", len(extensions), len(wantExtensions), extensions)
	}
	for index, name := range wantExtensions {
		if extensions[index].Name != name {
			t.Fatalf("web realtime extension[%d]=%#v want %q", index, extensions[index], name)
		}
		if name == "conversation.historyRebased" {
			if extensions[index].Owner != "transcript-history" || extensions[index].Kind != DeliveryInvalidate {
				t.Fatalf("history rebase extension=%#v", extensions[index])
			}
			continue
		}
		if extensions[index].Owner != "synon-web" || extensions[index].Kind != DeliveryFanout {
			t.Fatalf("web realtime extension[%d]=%#v want fanout synon-web %q", index, extensions[index], name)
		}
	}
	if spec, found := LookupEvent("conversation.listChanged"); !found || spec.Kind != DeliveryFanout {
		t.Fatalf("conversation.listChanged spec=%#v found=%v", spec, found)
	}
}

func TestEveryRecoveredQueryKeyResolves(t *testing.T) {
	args := map[string][]any{
		"trace": {"root", "focus"}, "branchMessages": {"root", "branch", 2},
		"artifactLineage": {"version", true}, "executionLog": {"frame", "version"},
		"annotations": {"av:version", "project"}, "benchNameSearch": {"project", "query"},
		"benchNameSearchBatch": {[]string{"project"}, "query"}, "bench": {"project", "frame"},
		"catalogSkillContent": {"skill", nil},
	}
	for _, query := range contracts.QueryKeys {
		queryArgs := args[query.Name]
		if queryArgs == nil && query.Expression != "" && query.Expression[0] != '[' && query.Expression[0] != '(' {
			queryArgs = []any{"id"}
		}
		key, err := ResolveQueryKey(query.Name, queryArgs...)
		if err != nil || len(key) == 0 {
			t.Fatalf("resolve %s (%s): key=%#v err=%v", query.Name, query.Expression, key, err)
		}
	}
}

func TestRecoveredMCPQueryKeysMatchV11Exactly(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want []any
	}{
		{"customMCPServers", nil, []any{"custom-mcp-servers"}},
		{"mcpAttachmentCounts", nil, []any{"mcp-attachment-counts"}},
		{"customMCPServer", []any{"mcp-1"}, []any{"custom-mcp-server", "mcp-1"}},
		{"agentCustomMCPServers", []any{"research"}, []any{"agent-custom-mcp-servers", "research"}},
		{"agentExcludedTools", []any{"research"}, []any{"agent-excluded-tools", "research"}},
		{"mcpConnectors", nil, []any{"mcp-connectors"}},
		{"mcpDirectoryHealth", nil, []any{"mcp-directory-health"}},
		{"mcpToolGrants", []any{"mcp-1"}, []any{"mcp-tool-grants", "mcp-1"}},
		{"mcpToolPermissions", []any{"mcp-1"}, []any{"mcp-tool-permissions", "mcp-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveQueryKey(test.name, test.args...)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveQueryKey(%s)=%#v err=%v, want %#v", test.name, got, err, test.want)
			}
		})
	}
}

func TestRecoveredSystemQueryKeysMatchV11Exactly(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want []any
	}{
		{"me", nil, []any{"me"}},
		{"firstRunOnboarding", nil, []any{"first-run-onboarding"}},
		{"remoteImageAllowlist", nil, []any{"remote-image-allowlist"}},
		{"agents", nil, []any{"agents"}},
		{"agentDetail", []any{"research"}, []any{"agent-detail", "research"}},
		{"models", []any{"provider-1"}, []any{"models", "provider-1"}},
		{"hostGrants", nil, []any{"host-grants"}},
		{"environmentStatus", nil, []any{"environments", "status"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveQueryKey(test.name, test.args...)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveQueryKey(%s)=%#v err=%v, want %#v", test.name, got, err, test.want)
			}
		})
	}
}

func TestRecoveredRuntimeDataQueryKeysMatchV11Exactly(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want []any
	}{
		{"annotations", []any{"av:version-1", "project-ignored"}, []any{"annotations", "av:version-1"}},
		{"skillCatalog", nil, []any{"skill-catalog"}},
		{"catalogSkillContent", []any{"patent-search"}, []any{"catalog-skill-content", "patent-search", "SKILL.md"}},
		{"catalogSkillFiles", []any{"patent-search"}, []any{"catalog-skill-files", "patent-search"}},
		{"customAgentPrompt", []any{"research"}, []any{"custom-agent-prompt", "research"}},
		{"cloudCredentials", nil, []any{"cloud-credentials"}},
		{"cloudCredential", []any{"credential-1"}, []any{"cloud-credential", "credential-1"}},
		{"secrets", nil, []any{"secrets"}},
		{"hostDirectory", []any{"/workspace"}, []any{"host-directory", "/workspace"}},
		{"processingCounts", nil, []any{"processing-counts"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveQueryKey(test.name, test.args...)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveQueryKey(%s)=%#v err=%v, want %#v", test.name, got, err, test.want)
			}
		})
	}
	remoteAnnotations, err := ResolveQueryKey("annotations", "remote:https://example.com", "project-1")
	if err != nil || !reflect.DeepEqual(remoteAnnotations, []any{"annotations", "remote:https://example.com", "project-1"}) {
		t.Fatalf("remote annotations key=%#v err=%v", remoteAnnotations, err)
	}
}

func TestRecoveredComputeQueryKeysMatchV11Exactly(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want []any
	}{
		{"computeJobs", []any{"project-1"}, []any{"compute-jobs", "project-1"}},
		{"computeProviders", nil, []any{"operon", "compute", "providers"}},
		{"managedEndpoints", nil, []any{"operon", "compute", "managed-endpoints"}},
		{"sessionKernels", []any{"root-1"}, []any{"session-kernels", "root-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveQueryKey(test.name, test.args...)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveQueryKey(%s)=%#v err=%v, want %#v", test.name, got, err, test.want)
			}
		})
	}
}

func TestRecoveredInvalidationSemantics(t *testing.T) {
	lineage := Invalidations("lineage_ready", map[string]any{"version_ids": []any{"v1", "v2"}})
	if len(lineage) != 2 || !reflect.DeepEqual(lineage[0].Key, []any{"artifact-lineage", "v1"}) {
		t.Fatalf("lineage invalidations=%#v", lineage)
	}
	verification := Invalidations("verification_update", map[string]any{"root_frame_id": "root", "version_ids": []string{"v1"}})
	if len(verification) != 2 || verification[0].Policy != "debounced" || verification[1].Query != "artifactVerification" {
		t.Fatalf("verification invalidations=%#v", verification)
	}
	project := Invalidations("project_deleted", map[string]any{"project_id": "project"})
	if len(project) < 9 {
		t.Fatalf("project invalidations=%#v", project)
	}
	connector := Invalidations("connector_update", map[string]any{"connector_id": "mcp-1", "agent_name": "research"})
	wantConnectorQueries := map[string]bool{
		"mcpConnectors": false, "mcpAttachmentCounts": false,
		"agentCustomMCPServers": false, "mcpToolGrants": false,
	}
	for _, invalidation := range connector {
		if _, tracked := wantConnectorQueries[invalidation.Query]; tracked {
			wantConnectorQueries[invalidation.Query] = true
		}
	}
	for query, found := range wantConnectorQueries {
		if !found {
			t.Fatalf("connector invalidations missing %s: %#v", query, connector)
		}
	}
	if got := Invalidations("text_chunk", map[string]any{"root_frame_id": "root"}); len(got) != 0 {
		t.Fatalf("fanout event unexpectedly invalidated queries: %#v", got)
	}
}

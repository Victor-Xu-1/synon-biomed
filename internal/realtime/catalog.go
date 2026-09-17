package realtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/compat/contracts"
)

type DeliveryKind string

const (
	DeliveryComposite  DeliveryKind = "composite"
	DeliveryInvalidate DeliveryKind = "invalidate"
	DeliveryFanout     DeliveryKind = "fanout"
	DeliveryNone       DeliveryKind = "none"
)

type EventSpec struct {
	Name   string       `json:"name"`
	Kind   DeliveryKind `json:"kind"`
	Via    string       `json:"via,omitempty"`
	Owner  string       `json:"owner,omitempty"`
	Reason string       `json:"reason,omitempty"`
}

type QueryInvalidation struct {
	Query  string `json:"query"`
	Key    []any  `json:"key"`
	Policy string `json:"policy"`
	Match  string `json:"match"`
}

var eventSpecs = map[string]EventSpec{
	"frame_update":                  {Name: "frame_update", Kind: DeliveryComposite},
	"frame_messages_delta":          {Name: "frame_messages_delta", Kind: DeliveryComposite},
	"text_chunk":                    {Name: "text_chunk", Kind: DeliveryFanout, Owner: "text-stream"},
	"text_reset":                    {Name: "text_reset", Kind: DeliveryFanout, Owner: "text-stream"},
	"frame_activity":                {Name: "frame_activity", Kind: DeliveryFanout, Owner: "frame-activity"},
	"compaction_status":             {Name: "compaction_status", Kind: DeliveryFanout, Owner: "compaction-status"},
	"rolling_compact_status":        {Name: "rolling_compact_status", Kind: DeliveryNone, Reason: "v1.1 client pathway deleted"},
	"rate_limit_notice":             {Name: "rate_limit_notice", Kind: DeliveryFanout, Owner: "rate-limit-notice"},
	"tool_stdout_chunk":             {Name: "tool_stdout_chunk", Kind: DeliveryFanout, Owner: "tool-stdout-stream"},
	"transcript_annotations_update": {Name: "transcript_annotations_update", Kind: DeliveryComposite, Via: "raw"},
	"mcp_app_tool_call":             {Name: "mcp_app_tool_call", Kind: DeliveryFanout, Via: "raw", Owner: "mcp-app-bridge"},
	"mcp_app_pin":                   {Name: "mcp_app_pin", Kind: DeliveryFanout, Via: "raw", Owner: "mcp-app-bridge"},
	"artifact_created":              {Name: "artifact_created", Kind: DeliveryComposite},
	"artifact_deleted":              {Name: "artifact_deleted", Kind: DeliveryComposite},
	"artifact_priority_update":      {Name: "artifact_priority_update", Kind: DeliveryComposite},
	"artifact_renamed":              {Name: "artifact_renamed", Kind: DeliveryComposite},
	"artifact_moved":                {Name: "artifact_moved", Kind: DeliveryComposite},
	"lineage_ready":                 {Name: "lineage_ready", Kind: DeliveryInvalidate},
	"folder_created":                {Name: "folder_created", Kind: DeliveryComposite},
	"folder_updated":                {Name: "folder_updated", Kind: DeliveryComposite},
	"folder_deleted":                {Name: "folder_deleted", Kind: DeliveryComposite},
	"note_update":                   {Name: "note_update", Kind: DeliveryInvalidate},
	"routine_update":                {Name: "routine_update", Kind: DeliveryInvalidate},
	"compute_job_update":            {Name: "compute_job_update", Kind: DeliveryInvalidate},
	"compute_job_log_chunk":         {Name: "compute_job_log_chunk", Kind: DeliveryFanout, Owner: "compute-job-log"},
	"managed_endpoint_transcript":   {Name: "managed_endpoint_transcript", Kind: DeliveryFanout, Owner: "managed-endpoint-transcript"},
	"managed_endpoint_update":       {Name: "managed_endpoint_update", Kind: DeliveryInvalidate},
	"managed_endpoint_removed":      {Name: "managed_endpoint_removed", Kind: DeliveryInvalidate},
	"file_changed":                  {Name: "file_changed", Kind: DeliveryFanout, Owner: "file-watch"},
	"file_deleted":                  {Name: "file_deleted", Kind: DeliveryFanout, Owner: "file-watch"},
	"verification_update":           {Name: "verification_update", Kind: DeliveryInvalidate},
	"project_deleted":               {Name: "project_deleted", Kind: DeliveryComposite},
	"environment_status":            {Name: "environment_status", Kind: DeliveryComposite},
	"connector_status":              {Name: "connector_status", Kind: DeliveryInvalidate},
	"host_access_granted":           {Name: "host_access_granted", Kind: DeliveryInvalidate},
	"auth_status_changed":           {Name: "auth_status_changed", Kind: DeliveryInvalidate},
	"network_access_granted":        {Name: "network_access_granted", Kind: DeliveryInvalidate},
	"update_available":              {Name: "update_available", Kind: DeliveryFanout, Via: "raw", Owner: "update-status"},
	"update_retracted":              {Name: "update_retracted", Kind: DeliveryFanout, Via: "raw", Owner: "update-status"},
	"update_required":               {Name: "update_required", Kind: DeliveryFanout, Via: "raw", Owner: "version-gate"},
	"daemon_restarting":             {Name: "daemon_restarting", Kind: DeliveryFanout, Via: "raw", Owner: "restart-blocker"},
	"daemon_restart_aborted":        {Name: "daemon_restart_aborted", Kind: DeliveryFanout, Via: "raw", Owner: "update-status"},
	"pong":                          {Name: "pong", Kind: DeliveryNone, Reason: "transport heartbeat resolved before fanout"},
	"execution_cell_update":         {Name: "execution_cell_update", Kind: DeliveryFanout, Owner: "kernel-notebook"},
	"kernel_terminal_ack":           {Name: "kernel_terminal_ack", Kind: DeliveryNone, Reason: "terminal request acknowledgement resolved before routing"},
	"connector_update":              {Name: "connector_update", Kind: DeliveryComposite},
	"connector_snapshot":            {Name: "connector_snapshot", Kind: DeliveryComposite},
}

var extensionEventSpecs = map[string]EventSpec{
	"confirmation.add":            {Name: "confirmation.add", Kind: DeliveryFanout, Owner: "synon-web"},
	"confirmation.remove":         {Name: "confirmation.remove", Kind: DeliveryFanout, Owner: "synon-web"},
	"confirmation.update":         {Name: "confirmation.update", Kind: DeliveryFanout, Owner: "synon-web"},
	"conversation.listChanged":    {Name: "conversation.listChanged", Kind: DeliveryFanout, Owner: "synon-web"},
	"conversation.historyRebased": {Name: "conversation.historyRebased", Kind: DeliveryInvalidate, Owner: "transcript-history"},
	"message.stream":              {Name: "message.stream", Kind: DeliveryFanout, Owner: "synon-web"},
	"message.userCreated":         {Name: "message.userCreated", Kind: DeliveryFanout, Owner: "synon-web"},
	"runtime.statusChanged":       {Name: "runtime.statusChanged", Kind: DeliveryFanout, Owner: "synon-web"},
	"turn.completed":              {Name: "turn.completed", Kind: DeliveryFanout, Owner: "synon-web"},
}

func LookupEvent(name string) (EventSpec, bool) {
	name = strings.TrimSpace(name)
	if spec, ok := eventSpecs[name]; ok {
		return spec, true
	}
	spec, ok := extensionEventSpecs[name]
	return spec, ok
}

func EventSpecs() []EventSpec {
	out := make([]EventSpec, 0, len(contracts.EventTypes))
	for _, baseline := range contracts.EventTypes {
		if spec, ok := eventSpecs[baseline.Name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

func ExtensionEventSpecs() []EventSpec {
	out := make([]EventSpec, 0, len(extensionEventSpecs))
	for _, spec := range extensionEventSpecs {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func QueryContracts() []contracts.QueryKey {
	out := make([]contracts.QueryKey, len(contracts.QueryKeys))
	copy(out, contracts.QueryKeys)
	return out
}

func ResolveQueryKey(name string, args ...any) ([]any, error) {
	name = strings.TrimSpace(name)
	switch name {
	case "me":
		return []any{"me"}, nil
	case "firstRunOnboarding":
		return []any{"first-run-onboarding"}, nil
	case "remoteImageAllowlist":
		return []any{"remote-image-allowlist"}, nil
	case "agents":
		return []any{"agents"}, nil
	case "computeProviders":
		return []any{"operon", "compute", "providers"}, nil
	case "managedEndpoints":
		return []any{"operon", "compute", "managed-endpoints"}, nil
	case "projectList":
		return []any{"project-list"}, nil
	case "dashboard":
		return []any{"dashboard"}, nil
	case "skillCatalog":
		return []any{"skill-catalog"}, nil
	case "skillDrafts":
		return []any{"skill-drafts"}, nil
	case "customMCPServers":
		return []any{"custom-mcp-servers"}, nil
	case "mcpAttachmentCounts":
		return []any{"mcp-attachment-counts"}, nil
	case "mcpConnectors":
		return []any{"mcp-connectors"}, nil
	case "mcpDirectoryHealth":
		return []any{"mcp-directory-health"}, nil
	case "cloudCredentials":
		return []any{"cloud-credentials"}, nil
	case "secrets":
		return []any{"secrets"}, nil
	case "hostGrants":
		return []any{"host-grants"}, nil
	case "processingCounts":
		return []any{"processing-counts"}, nil
	case "environmentStatus":
		return []any{"environments", "status"}, nil
	case "trace":
		if err := requireArgs(name, args, 1); err != nil {
			return nil, err
		}
		if len(args) > 1 && args[1] != nil {
			return []any{"trace", args[0], args[1]}, nil
		}
		return []any{"trace", args[0]}, nil
	case "branchMessages":
		if err := requireArgs(name, args, 3); err != nil {
			return nil, err
		}
		return []any{"branch-messages", args[0], args[1], args[2]}, nil
	case "artifactLineage":
		if err := requireArgs(name, args, 1); err != nil {
			return nil, err
		}
		mode := "full"
		if len(args) > 1 {
			if slim, ok := args[1].(bool); ok && slim {
				mode = "slim"
			}
		}
		return []any{"artifact-lineage", args[0], mode}, nil
	case "executionLog":
		if err := requireArgs(name, args, 2); err != nil {
			return nil, err
		}
		return []any{"execution-log", args[0], args[1]}, nil
	case "annotations":
		if err := requireArgs(name, args, 1); err != nil {
			return nil, err
		}
		target := fmt.Sprint(args[0])
		if strings.HasPrefix(target, "av:") {
			return []any{"annotations", args[0]}, nil
		}
		var project any
		if len(args) > 1 {
			project = args[1]
		}
		return []any{"annotations", args[0], project}, nil
	case "benchNameSearch":
		return resolveFixedArgs(name, "bench-name-search", args, 2)
	case "benchNameSearchBatch":
		return resolveFixedArgs(name, "bench-name-search-batch", args, 2)
	case "bench":
		return resolveFixedArgs(name, "bench", args, 2)
	case "catalogSkillContent":
		if err := requireArgs(name, args, 1); err != nil {
			return nil, err
		}
		path := any("SKILL.md")
		if len(args) > 1 && args[1] != nil {
			path = args[1]
		}
		return []any{"catalog-skill-content", args[0], path}, nil
	}
	oneArgPrefixes := map[string]string{
		"frame": "frame", "agentDetail": "agent-detail", "models": "models",
		"artifacts": "artifacts", "artifact": "artifact", "artifactVersions": "artifact-versions",
		"sessionKernels": "session-kernels", "verificationChecks": "verification-checks",
		"artifactVerification": "artifact-verification", "transcriptAnnotations": "transcript-annotations",
		"computeJobs": "compute-jobs", "routines": "routines", "tokenSeries": "token-series",
		"folders": "folders", "benches": "benches", "benchesBatch": "benches-batch",
		"artifactsBatch": "artifacts-batch", "searchArtifactsBatch": "search-artifacts-batch",
		"searchBenchesBatch": "search-benches-batch", "collaborators": "collaborators",
		"projectConversation": "projectConversation", "project": "project",
		"catalogSkillFiles": "catalog-skill-files", "notes": "notes",
		"customAgentPrompt": "custom-agent-prompt", "customMCPServer": "custom-mcp-server",
		"agentCustomMCPServers": "agent-custom-mcp-servers", "agentExcludedTools": "agent-excluded-tools",
		"mcpToolGrants": "mcp-tool-grants", "mcpToolPermissions": "mcp-tool-permissions",
		"cloudCredential": "cloud-credential", "hostDirectory": "host-directory",
	}
	if prefix, ok := oneArgPrefixes[name]; ok {
		return resolveFixedArgs(name, prefix, args, 1)
	}
	return nil, fmt.Errorf("unknown v1.1 query key %q", name)
}

func Invalidations(eventType string, payload map[string]any) []QueryInvalidation {
	projectID := payloadString(payload, "project_id", "projectId")
	rootFrameID := payloadString(payload, "root_frame_id", "rootFrameId")
	frameID := payloadString(payload, "frame_id", "frameId")
	artifactID := payloadString(payload, "artifact_id", "artifactId")
	immediate := func(query string, key []any) QueryInvalidation {
		return QueryInvalidation{Query: query, Key: key, Policy: "immediate", Match: "exact"}
	}
	debounced := func(query string, key []any) QueryInvalidation {
		return QueryInvalidation{Query: query, Key: key, Policy: "debounced", Match: "exact"}
	}
	prefix := func(query string, key []any, policy string) QueryInvalidation {
		return QueryInvalidation{Query: query, Key: key, Policy: policy, Match: "prefix"}
	}
	var out []QueryInvalidation
	switch eventType {
	case "frame_update":
		out = appendIfResolved(out, "trace", []any{rootFrameID}, "immediate", "prefix", rootFrameID != "")
		out = appendIfResolved(out, "frame", []any{frameID}, "immediate", "exact", frameID != "")
		out = appendIfResolved(out, "benches", []any{projectID}, "debounced", "exact", projectID != "")
		out = append(out,
			prefix("benchesBatch", []any{"benches-batch"}, "debounced"),
			prefix("searchBenchesBatch", []any{"search-benches-batch"}, "debounced"),
			prefix("benchNameSearchBatch", []any{"bench-name-search-batch"}, "debounced"),
			prefix("tokenSeries", []any{"token-series"}, "debounced"),
			debounced("dashboard", []any{"dashboard"}), debounced("projectList", []any{"project-list"}),
		)
		if projectID != "" {
			out = append(out, prefix("benchNameSearch", []any{"bench-name-search", projectID}, "debounced"))
		}
		if projectID != "" && rootFrameID != "" {
			out = append(out, debounced("bench", []any{"bench", projectID, rootFrameID}))
		}
		branchID := payloadString(payload, "branch_id", "branchId")
		if frameID != "" && branchID != "" {
			out = append(out, prefix("branchMessages", []any{"branch-messages", frameID, branchID}, "immediate"))
		}
	case "frame_messages_delta":
		out = appendIfResolved(out, "trace", []any{rootFrameID}, "immediate", "prefix", rootFrameID != "")
		out = appendIfResolved(out, "frame", []any{frameID}, "immediate", "exact", frameID != "")
		out = append(out, prefix("tokenSeries", []any{"token-series"}, "debounced"))
	case "transcript_annotations_update":
		out = appendIfResolved(out, "transcriptAnnotations", []any{rootFrameID}, "immediate", "exact", rootFrameID != "")
	case "artifact_created", "artifact_deleted", "artifact_priority_update", "artifact_renamed", "artifact_moved":
		out = appendIfResolved(out, "artifacts", []any{projectID}, "debounced", "exact", projectID != "")
		out = appendIfResolved(out, "artifact", []any{artifactID}, "immediate", "exact", artifactID != "")
		out = appendIfResolved(out, "artifactVersions", []any{artifactID}, "immediate", "exact", artifactID != "")
		out = append(out, debounced("dashboard", []any{"dashboard"}), prefix("artifactsBatch", []any{"artifacts-batch"}, "debounced"), prefix("searchArtifactsBatch", []any{"search-artifacts-batch"}, "debounced"))
	case "lineage_ready":
		for _, versionID := range payloadStrings(payload["version_ids"]) {
			out = append(out, immediate("artifactLineage", []any{"artifact-lineage", versionID}))
		}
	case "folder_created", "folder_updated", "folder_deleted":
		out = appendIfResolved(out, "folders", []any{projectID}, "immediate", "exact", projectID != "")
	case "note_update":
		out = appendIfResolved(out, "notes", []any{projectID}, "debounced", "exact", projectID != "")
	case "routine_update":
		out = appendIfResolved(out, "routines", []any{projectID}, "immediate", "exact", projectID != "")
	case "compute_job_update":
		out = appendIfResolved(out, "computeJobs", []any{projectID}, "immediate", "exact", projectID != "")
	case "managed_endpoint_update", "managed_endpoint_removed":
		out = append(out, immediate("managedEndpoints", []any{"operon", "compute", "managed-endpoints"}), immediate("computeProviders", []any{"operon", "compute", "providers"}))
	case "verification_update":
		out = appendIfResolved(out, "verificationChecks", []any{rootFrameID}, "debounced", "exact", rootFrameID != "")
		for _, versionID := range payloadStrings(payload["version_ids"]) {
			out = append(out, debounced("artifactVerification", []any{"artifact-verification", versionID}))
		}
	case "project_deleted":
		out = appendIfResolved(out, "project", []any{projectID}, "immediate", "exact", projectID != "")
		out = appendIfResolved(out, "artifacts", []any{projectID}, "immediate", "exact", projectID != "")
		out = appendIfResolved(out, "collaborators", []any{projectID}, "immediate", "exact", projectID != "")
		out = appendIfResolved(out, "folders", []any{projectID}, "immediate", "exact", projectID != "")
		out = appendIfResolved(out, "projectConversation", []any{projectID}, "immediate", "exact", projectID != "")
		out = append(out, immediate("dashboard", []any{"dashboard"}), immediate("projectList", []any{"project-list"}),
			prefix("benchesBatch", []any{"benches-batch"}, "immediate"),
			prefix("artifactsBatch", []any{"artifacts-batch"}, "immediate"),
			prefix("searchArtifactsBatch", []any{"search-artifacts-batch"}, "immediate"),
			prefix("searchBenchesBatch", []any{"search-benches-batch"}, "immediate"),
			prefix("benchNameSearchBatch", []any{"bench-name-search-batch"}, "immediate"),
		)
	case "environment_status":
		out = append(out, immediate("environmentStatus", []any{"environments", "status"}))
	case "connector_status", "connector_update", "connector_snapshot":
		out = append(out, debounced("mcpConnectors", []any{"mcp-connectors"}))
		connectorID := payloadString(payload, "connector_id", "connectorId")
		agentName := payloadString(payload, "agent_name", "agentName")
		out = append(out, debounced("mcpAttachmentCounts", []any{"mcp-attachment-counts"}))
		out = appendIfResolved(out, "customMCPServer", []any{connectorID}, "debounced", "exact", connectorID != "")
		out = appendIfResolved(out, "mcpToolGrants", []any{connectorID}, "debounced", "exact", connectorID != "")
		out = appendIfResolved(out, "agentCustomMCPServers", []any{agentName}, "debounced", "exact", agentName != "")
	case "host_access_granted":
		out = append(out, debounced("hostGrants", []any{"host-grants"}))
	case "auth_status_changed":
		out = append(out, immediate("auth.status", []any{"auth", "status"}), immediate("me", []any{"me"}))
	case "network_access_granted":
		out = append(out, debounced("remoteImageAllowlist", []any{"remote-image-allowlist"}))
	}
	return deduplicateInvalidations(out)
}

func requireArgs(name string, args []any, count int) error {
	if len(args) < count {
		return fmt.Errorf("query key %s requires %d argument(s)", name, count)
	}
	return nil
}

func resolveFixedArgs(name, prefix string, args []any, count int) ([]any, error) {
	if err := requireArgs(name, args, count); err != nil {
		return nil, err
	}
	key := make([]any, 1, count+1)
	key[0] = prefix
	key = append(key, args[:count]...)
	return key, nil
}

func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func payloadStrings(value any) []string {
	values, ok := value.([]string)
	if ok {
		return values
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value := strings.TrimSpace(fmt.Sprint(item)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func appendIfResolved(values []QueryInvalidation, query string, suffix []any, policy, match string, condition bool) []QueryInvalidation {
	if !condition {
		return values
	}
	prefixes := map[string]string{
		"trace": "trace", "frame": "frame", "benches": "benches", "artifacts": "artifacts",
		"artifact": "artifact", "artifactVersions": "artifact-versions", "transcriptAnnotations": "transcript-annotations",
		"folders": "folders", "notes": "notes", "routines": "routines", "computeJobs": "compute-jobs",
		"verificationChecks": "verification-checks", "project": "project", "collaborators": "collaborators",
		"projectConversation": "projectConversation",
		"customMCPServer":     "custom-mcp-server", "mcpToolGrants": "mcp-tool-grants",
		"agentCustomMCPServers": "agent-custom-mcp-servers",
	}
	prefix, ok := prefixes[query]
	if !ok {
		return values
	}
	key := make([]any, 1, len(suffix)+1)
	key[0] = prefix
	key = append(key, suffix...)
	return append(values, QueryInvalidation{Query: query, Key: key, Policy: policy, Match: match})
}

func deduplicateInvalidations(values []QueryInvalidation) []QueryInvalidation {
	seen := map[string]bool{}
	out := make([]QueryInvalidation, 0, len(values))
	for _, value := range values {
		key := fmt.Sprintf("%s|%v|%s|%s", value.Query, value.Key, value.Policy, value.Match)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func ValidateCatalog() error {
	if len(eventSpecs) != len(contracts.EventTypes) {
		return fmt.Errorf("event spec count %d does not match baseline %d", len(eventSpecs), len(contracts.EventTypes))
	}
	for _, baseline := range contracts.EventTypes {
		spec, ok := eventSpecs[baseline.Name]
		if !ok {
			return fmt.Errorf("missing event spec %s", baseline.Name)
		}
		if string(spec.Kind) != baseline.Kind {
			return fmt.Errorf("event %s kind %s does not match %s", baseline.Name, spec.Kind, baseline.Kind)
		}
	}
	for name, spec := range extensionEventSpecs {
		if _, exists := eventSpecs[name]; exists {
			return fmt.Errorf("extension event %s duplicates the v1.1 baseline", name)
		}
		if strings.TrimSpace(name) == "" || spec.Name != name || spec.Kind == "" {
			return fmt.Errorf("invalid extension event spec %q", name)
		}
	}
	seenQueries := map[string]bool{}
	for _, query := range contracts.QueryKeys {
		if seenQueries[query.Name] {
			return fmt.Errorf("duplicate query key %s", query.Name)
		}
		seenQueries[query.Name] = true
	}
	if len(seenQueries) != 60 {
		return fmt.Errorf("query key count %d does not match baseline 60", len(seenQueries))
	}
	return nil
}

var ErrUnknownEvent = errors.New("unknown v1.1 realtime event")

package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"
	"synon-go/internal/skills"
	toolregistry "synon-go/internal/tools/registry"
)

var defaultAgentRuntimeCapabilityCatalog = toolregistry.Default()

func defaultRegisteredAgentRuntimeTool(name string) (toolregistry.Tool, bool) {
	name = strings.TrimSpace(name)
	if tool, found := defaultAgentRuntimeCapabilityCatalog.Get(name); found {
		return tool, true
	}
	canonical, err := canonicalRuntimeToolName(name)
	if err != nil || canonical == name {
		return toolregistry.Tool{}, false
	}
	return defaultAgentRuntimeCapabilityCatalog.Get(canonical)
}

func canonicalAgentRuntimeToolAuthority(
	schemas []agentruntime.ToolSchema,
) ([]agentruntime.ToolSchema, error) {
	result := make([]agentruntime.ToolSchema, 0, len(schemas))
	encodedByName := make(map[string]string, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		key := strings.ToLower(name)
		if key == "" {
			return nil, fmt.Errorf("runtime Tool authority contains an empty name")
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("encode runtime Tool %q: %w", name, err)
		}
		if previous, duplicate := encodedByName[key]; duplicate {
			if previous != string(encoded) {
				return nil, fmt.Errorf("runtime Tool %q has competing schema authorities", name)
			}
			continue
		}
		encodedByName[key] = string(encoded)
		result = append(result, schema)
	}
	return result, nil
}

// partitionAgentRuntimeToolSchemasForModel mirrors the reference Harness:
// one compact fixed tool set. MCP methods remain available through the scoped
// repl/host.mcp bridge backed by the same live MCP pool; flattened mcp__ names,
// legacy aliases, and domain-specific convenience tools remain readable only
// at migration boundaries and never enter a new model snapshot.
func partitionAgentRuntimeToolSchemasForModel(
	schemas []agentruntime.ToolSchema,
) []agentruntime.ToolSchema {
	return partitionAgentRuntimeToolSchemasForModelWithSkills(schemas, nil)
}

// partitionAgentRuntimeToolSchemasForModelWithSkills adds only the exact tools
// declared by the Skills selected for this task. This keeps the root surface
// compact while making a loaded Skill executable: a patent Skill can expose
// the governed patent researcher without advertising every domain tool to
// unrelated tasks. Skill declarations never grant an absent schema, flattened
// MCP method, legacy alias, or fixed-job tool.
func partitionAgentRuntimeToolSchemasForModelWithSkills(
	schemas []agentruntime.ToolSchema,
	selectedSkills []skills.Skill,
) []agentruntime.ToolSchema {
	return projectAgentRuntimeToolSchemas(schemas, selectedSkills, nil, true)
}

func projectAgentRuntimeToolSchemas(
	schemas []agentruntime.ToolSchema,
	selectedSkills []skills.Skill,
	activatedNames map[string]struct{},
	includeDirect bool,
) []agentruntime.ToolSchema {
	skillTools := map[string]struct{}{}
	for _, skill := range selectedSkills {
		for _, tool := range skill.Tools {
			if normalized := normalizeAgentToolName(tool); normalized != "" {
				skillTools[normalized] = struct{}{}
			}
		}
	}
	modelSchemas := make([]agentruntime.ToolSchema, 0, len(schemas))
	seen := map[string]struct{}{}
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		key := strings.ToLower(name)
		_, selectedBySkill := skillTools[normalizeAgentToolName(name)]
		_, explicitlyActivated := activatedNames[normalizeAgentToolName(name)]
		exposure := effectiveAgentRuntimeToolExposure(schema)
		visible := explicitlyActivated || selectedBySkill || includeDirect && exposure == agentruntime.ToolExposureDirect
		if exposure == agentruntime.ToolExposureHidden || !visible {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		modelSchemas = append(modelSchemas, schema)
	}
	return modelSchemas
}

// registeredAgentRuntimeToolExposure is the sole adapter from a canonical
// registered Tool to the provider projection. It translates metadata; it does
// not infer or override the registration from a parallel name list.
func registeredAgentRuntimeToolExposure(tool toolregistry.Tool) agentruntime.ToolExposure {
	switch tool.Exposure {
	case toolregistry.ToolExposureDirect:
		return agentruntime.ToolExposureDirect
	case toolregistry.ToolExposureDeferred:
		return agentruntime.ToolExposureDeferred
	default:
		return agentruntime.ToolExposureHidden
	}
}

// runtimeInjectedAgentRuntimeToolExposure applies only to task-owned schemas
// that have no static registry entry, such as kernel tools. Static Tools must
// carry their own exposure in the canonical registry.
func runtimeInjectedAgentRuntimeToolExposure(name string) agentruntime.ToolExposure {
	name = strings.TrimSpace(name)
	if harnesscontract.ModelToolAllowed(name) {
		return agentruntime.ToolExposureDirect
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return agentruntime.ToolExposureHidden
	}
	if harnesscontract.ClassifyToolSurface(name) == harnesscontract.ToolSurfaceCompatibilityAPI {
		return agentruntime.ToolExposureDeferred
	}
	return agentruntime.ToolExposureHidden
}

func effectiveAgentRuntimeToolExposure(schema agentruntime.ToolSchema) agentruntime.ToolExposure {
	if tool, found := defaultAgentRuntimeCapabilityCatalog.Get(strings.TrimSpace(schema.Name)); found {
		return restrictRegisteredAgentRuntimeToolExposure(
			registeredAgentRuntimeToolExposure(tool), schema.Exposure,
		)
	}
	if schema.Exposure != "" {
		return schema.EffectiveExposure()
	}
	return runtimeInjectedAgentRuntimeToolExposure(schema.Name)
}

func restrictRegisteredAgentRuntimeToolExposure(
	registered agentruntime.ToolExposure,
	requested agentruntime.ToolExposure,
) agentruntime.ToolExposure {
	if registered == agentruntime.ToolExposureHidden || requested == agentruntime.ToolExposureHidden {
		return agentruntime.ToolExposureHidden
	}
	if registered == agentruntime.ToolExposureDeferred || requested == agentruntime.ToolExposureDeferred {
		return agentruntime.ToolExposureDeferred
	}
	return agentruntime.ToolExposureDirect
}

func agentRuntimeToolSchemaHasCapability(schema agentruntime.ToolSchema, capability string) bool {
	wanted := strings.ToLower(strings.TrimSpace(capability))
	if wanted == "" {
		return false
	}
	for _, candidate := range effectiveAgentRuntimeToolCapabilities(schema) {
		if strings.ToLower(strings.TrimSpace(candidate)) == wanted {
			return true
		}
	}
	return false
}

func agentRuntimeToolCapabilities(
	schemas []agentruntime.ToolSchema,
	toolName string,
) []string {
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), strings.TrimSpace(toolName)) {
			return effectiveAgentRuntimeToolCapabilities(schema)
		}
	}
	return nil
}

func effectiveAgentRuntimeToolCapabilities(schema agentruntime.ToolSchema) []string {
	if len(schema.Capabilities) > 0 {
		return append([]string(nil), schema.Capabilities...)
	}
	// Compatibility for callers that constructed a schema before capabilities
	// became first-class. Resolve only through the canonical Tool registry; do
	// not maintain another name-to-capability table here.
	if tool, found := defaultRegisteredAgentRuntimeTool(schema.Name); found {
		return append([]string(nil), tool.Capabilities...)
	}
	if owned := annotateOwnedAgentRuntimeToolSchema(schema); len(owned.Capabilities) > 0 {
		return append([]string(nil), owned.Capabilities...)
	}
	return nil
}

// annotateOwnedAgentRuntimeToolSchema supplies metadata for runtime-injected
// Tools that do not originate in the static registry. The mapping is by stable
// Tool contract, never by task, domain entity, filename, prompt, or selected
// scientific engine.
func annotateOwnedAgentRuntimeToolSchema(schema agentruntime.ToolSchema) agentruntime.ToolSchema {
	if schema.Exposure == "" {
		schema.Exposure = runtimeInjectedAgentRuntimeToolExposure(schema.Name)
	}
	if len(schema.Capabilities) > 0 {
		return schema
	}
	switch normalizeAgentToolName(schema.Name) {
	case normalizeAgentToolName(manageEnvironmentsToolName), normalizeAgentToolName(managePackagesToolName):
		schema.Capabilities = []string{"environment-management", "software-provisioning"}
	case "python", "r", "bash":
		schema.Capabilities = []string{"runtime-execution", "local-compute"}
	case "repl":
		schema.Capabilities = []string{"runtime-execution", "local-compute", "mcp-bridge"}
	case normalizeAgentToolName(listComputeToolName), normalizeAgentToolName(computeDetailsToolName), normalizeAgentToolName(askAboutComputeToolName):
		schema.Capabilities = []string{"compute-control", "read-only"}
	case normalizeAgentToolName(computeProviderToolName):
		schema.Capabilities = []string{"compute-provider-runtime", "remote-compute"}
	case normalizeAgentToolName(listHostGrantsToolName):
		schema.Capabilities = []string{"permission-control", "read-only"}
	case normalizeAgentToolName(requestHostAccessToolName), normalizeAgentToolName(requestNetworkAccessToolName), normalizeAgentToolName(deleteHostFilesToolName):
		schema.Capabilities = []string{"permission-control"}
	case "readfile":
		schema.Capabilities = []string{"artifact-read", "read-only"}
	case "editfile":
		schema.Capabilities = []string{"artifact-write", "artifact-edit"}
	case "saveartifacts":
		schema.Capabilities = []string{"artifact-write", "artifact-publication"}
	case "downloadpublicscientificfile", "downloadrcsbfile":
		schema.Capabilities = []string{"source-evidence", "source-download", "scientific-system-tool"}
	case "searchrcsbstructures":
		schema.Capabilities = []string{"source-evidence", "source-discovery", "scientific-system-tool"}
	case "waitfornotification":
		schema.Capabilities = []string{"durable-wait", "read-only"}
	}
	return schema
}

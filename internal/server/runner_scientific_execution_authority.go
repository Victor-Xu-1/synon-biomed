package server

import (
	"strings"

	"synon-go/internal/agentruntime"
)

// enforceGovernedScientificExecutionAuthority removes the retired aggregate
// software executor from every task snapshot. Environment management, package
// provisioning, local kernels, MCP, and remote compute remain independent Tool
// capabilities behind the one gateway; no task-text classifier decides which
// scientific route exists.
func enforceGovernedScientificExecutionAuthority(
	schemas []agentruntime.ToolSchema,
) []agentruntime.ToolSchema {
	filtered := make([]agentruntime.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if strings.EqualFold(name, softwareRuntimeToolName) {
			continue
		}
		filtered = append(filtered, schema)
	}
	return filtered
}

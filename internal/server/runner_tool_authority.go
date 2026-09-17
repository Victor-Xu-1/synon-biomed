package server

import (
	"context"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"
	sessionstore "synon-go/internal/persistence/sessions"
)

type sessionRunnerToolAuthority struct {
	Options       SessionRunnerChatOptions
	Schemas       []agentruntime.ToolSchema
	PolicyContext string
}

// bindSessionRunnerToolAuthority builds the complete task-scoped tool
// authority used by both durable kernel recovery and subsequent model work.
// Recovery must never run from the compact model-advertised tool partition:
// host bridges such as host.mcp validate against this full internal snapshot.
func (s *Server) bindSessionRunnerToolAuthority(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
) (sessionRunnerToolAuthority, error) {
	if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
		options.SessionID = sessionID
	}
	// Discover MCP for the internal authority snapshot. The model-facing
	// partition removes flattened mcp__ schemas later, but host.mcp must retain
	// their exact connector/method authority for validation and recovery.
	// Build the internal universe from the stable model root plus every exact
	// operation declared by a catalog Skill. Omitting either side makes Skill
	// admission circular or leaves loaded guidance without executable basics.
	// This is discovery only; the selected agent profile filters authority below
	// and the provider sees only the compact task-scoped partition.
	// The runtime universe is the stable reference-Harness root plus exact
	// catalog Skill additions. Starting from Skill tools alone silently removed
	// repl, environment management, file editing, and artifact publication when
	// no selected Skill happened to redeclare them.
	requestedSkillTools := harnesscontract.RootModelTools()
	if s != nil && s.skillCatalog != nil {
		catalogSkills := s.skillCatalog.Skills()
		catalogNames := make([]string, 0, len(catalogSkills))
		for _, skill := range catalogSkills {
			catalogNames = append(catalogNames, skill.Name)
		}
		requestedSkillTools = appendAvailableSkillDeclaredToolNames(
			requestedSkillTools, s.allRegisteredNames(), catalogSkills, catalogNames,
		)
	}
	schemas := s.agentRuntimeToolSchemasWithContextOptions(
		ctx, requestedSkillTools, options.DisableMCPDiscovery, options.SessionID,
	)
	// Kernel-backed Frames intentionally omit flattened MCP schemas from the
	// generic runtime catalog. Reattach the owner-scoped workspace snapshot to
	// the internal authority here; partitionAgentRuntimeToolSchemasForModel still
	// keeps these methods out of the provider's root tool list.
	if !options.DisableMCPDiscovery {
		mcpDiscovery := s.agentRuntimeWorkspaceMCPToolSchemas(ctx, options.SessionID, nil, schemas)
		schemas = append(schemas, mcpDiscovery.Schemas...)
	}
	if outputSchema, found := kernelDelegateOutputSchemaFromSession(session); found {
		schemas = append(schemas, kernelDelegateSubmitOutputToolSchema(outputSchema))
	}
	canonicalSchemas, err := canonicalAgentRuntimeToolAuthority(schemas)
	if err != nil {
		return sessionRunnerToolAuthority{}, err
	}
	schemas = canonicalSchemas
	bound, _, err := s.applySessionRunnerAgentProfile(
		session, options, agentRuntimeToolSchemaNames(schemas),
	)
	if err != nil {
		return sessionRunnerToolAuthority{}, err
	}
	// Workspace-backed agent profiles do not necessarily have a bundled
	// catalog entry. Expand the explicit compact dispatcher allowlist from the
	// already policy-filtered runtime universe here as the common final binding.
	// The no-tools sentinel remains closed by appendAvailableAgentFrameToolNames.
	bound.AllowedTools = appendAvailableAgentFrameToolNames(
		bound.AllowedTools, agentRuntimeToolSchemaNames(schemas),
	)
	if structuredOnboardingToolAllowed(bound.AllowedTools) &&
		!agentRuntimeToolSchemaNamed(schemas, onboardingReadAttachmentToolName) {
		schemas = append(schemas, structuredOnboardingAttachmentToolSchema())
	}
	bound, policyContext := s.applySessionRunnerAgentToolPolicy(session, bound)
	return sessionRunnerToolAuthority{
		Options: bound, Schemas: schemas, PolicyContext: policyContext,
	}, nil
}

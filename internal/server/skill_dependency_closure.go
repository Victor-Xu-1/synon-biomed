package server

import (
	"context"
	"fmt"
	"strings"

	"synon-go/internal/harnesscontract"
	"synon-go/internal/providers"
	"synon-go/internal/toolcontract"
	toolregistry "synon-go/internal/tools/registry"
)

func (s *Server) v11SkillDependencyClosure(sessionIDs ...string) toolregistry.SkillDependencyReport {
	if s == nil || s.skillCatalog == nil || s.tools == nil {
		return toolregistry.SkillDependencyReport{Closed: false, Errors: []string{"server skill catalog or tool registry is not configured"}}
	}
	projectID := ""
	sessionID := ""
	if len(sessionIDs) > 0 && s.sessionStore != nil {
		sessionID = strings.TrimSpace(sessionIDs[0])
		if session, found, err := s.sessionStore.Get(sessionID); err == nil && found {
			projectID = sessionRunnerProjectID(session)
		}
	}
	resolver := toolregistry.RuntimeResolverFunc(func(name string, class toolregistry.DependencyClass) toolregistry.RuntimeStatus {
		return s.v11DependencyRuntimeStatusForSession(name, class, projectID, sessionID)
	})
	return toolregistry.AuditSkillDependencyClosure(s.skillCatalog.Skills(), s.tools, resolver)
}

func (s *Server) v11DependencyRuntimeStatus(name string, class toolregistry.DependencyClass, projectID string) toolregistry.RuntimeStatus {
	return s.v11DependencyRuntimeStatusForSession(name, class, projectID, "")
}

func (s *Server) v11DependencyRuntimeStatusForSession(
	name string,
	class toolregistry.DependencyClass,
	projectID string,
	sessionID string,
) toolregistry.RuntimeStatus {
	if class == toolregistry.DependencyClassHostCapability {
		if name != "host.llm" {
			return toolregistry.RuntimeStatus{Availability: "unavailable", Route: "unsupported-host-capability"}
		}
		resolution, err := providers.ResolveRunnerModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, providers.ResolutionInput{ProjectID: projectID})
		if err != nil {
			return toolregistry.RuntimeStatus{Availability: "unavailable", Route: "runner-model-client-complete", Evidence: err.Error()}
		}
		if !resolution.Resolved || resolution.ModelProfile == nil {
			return toolregistry.RuntimeStatus{
				Availability: "conditional", Route: "runner-model-client-complete",
				Evidence: "host.llm is satisfied only inside an agent turn with an active saved provider profile and resolvable secret ref",
			}
		}
		if _, err := providers.NewRuntimeModelClient(*resolution.ModelProfile, s.httpClient, nil); err != nil {
			return toolregistry.RuntimeStatus{Availability: "unavailable", Route: "runner-model-client-complete", Evidence: fmt.Sprintf("construct runtime model client: %v", err)}
		}
		return toolregistry.RuntimeStatus{
			Executable: true, Availability: "configured", Route: "runner-model-client-complete",
			Evidence: fmt.Sprintf("saved provider %s resolves through secret ref %s to a concrete agentruntime.ModelClient.Complete path", resolution.ProviderID, resolution.ModelProfile.Provider.SecretRef),
		}
	}
	if class != toolregistry.DependencyClassTool {
		return toolregistry.RuntimeStatus{Availability: "evidence_only", Route: "external-runtime-evidence-only"}
	}
	canonical, ok := toolcontract.NormalizeRuntimeName(name)
	if !ok {
		return toolregistry.RuntimeStatus{Availability: "unavailable", Route: "invalid-tool-name"}
	}
	name = canonical
	disableMCPDiscovery := !strings.HasPrefix(strings.ToLower(name), "mcp__")
	schemas := s.agentRuntimeToolSchemasWithContextOptions(
		context.Background(), []string{name}, disableMCPDiscovery, sessionID,
	)
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), name) {
			return toolregistry.RuntimeStatus{
				Executable: true, AuthorityResolved: true, Availability: "available",
				Route: "unified-agent-runtime", Evidence: "the task-scoped Tool registry binds this exact schema to the canonical gateway",
			}
		}
	}
	if harnesscontract.ModelToolAllowed(name) {
		return toolregistry.RuntimeStatus{
			AuthorityResolved: true, Availability: "conditional", Route: "unified-agent-runtime",
			Evidence: "the canonical root contract exists but its task-scoped runtime dependency is not ready",
		}
	}
	return toolregistry.RuntimeStatus{Availability: "unavailable", Route: "unavailable"}
}

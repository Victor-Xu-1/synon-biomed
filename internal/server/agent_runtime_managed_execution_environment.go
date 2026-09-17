package server

import (
	"context"
	"strings"

	"synon-go/internal/sciencecapability"
)

// managedExecutionEnvironmentBoundary is the host-owned process boundary for
// implementation-specific managed environments. Once an environment is bound
// to a registered implementation, arbitrary Python/R/Bash cells cannot use it
// as a competing engine launcher: only the exact immutable execution-pack
// entrypoint is admitted. Data preparation remains available in an unbound
// analysis environment and through other registered capabilities.
func (s *Server) managedExecutionEnvironmentBoundary(
	ctx context.Context,
	publicName string,
	input map[string]any,
	environment string,
) map[string]any {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if s == nil || s.scienceCapabilities == nil || run == nil {
		return nil
	}
	bound := run.managedEnvironmentImplementationsSnapshot(environment)
	if len(bound) == 0 {
		return nil
	}
	if engine, found := s.canonicalManagedExecutionPack(publicName, input); found {
		for _, current := range bound {
			if s.managedExecutionPackMatchesImplementation(engine, current) {
				return nil
			}
		}
	}
	packs := make([]string, 0, len(bound))
	for _, implementation := range bound {
		for _, capability := range s.scienceCapabilities.Capabilities {
			for _, engine := range capability.AcceptedEngines {
				if engine.ExecutionPack.Mode == "local" &&
					s.managedExecutionPackMatchesImplementation(engine, implementation) {
					packs = append(packs, engine.ExecutionPack.ID)
				}
			}
		}
	}
	return map[string]any{
		"ok": false, "status": "managed_execution_environment_entrypoint_required", "executed": false,
		"environment": environment, "bound_implementations": append([]string(nil), bound...),
		"execution_pack_ids": uniqueSortedFolded(packs),
		"message":            "This managed environment is bound to a registered scientific implementation and cannot start an arbitrary process cell.",
		"recovery":           "Run the exact materialized execution-pack entrypoint in this implementation environment. Use a separate unbound analysis environment for ordinary preparation or inspection code.",
	}
}

func (s *Server) managedExecutionPackMatchesImplementation(
	engine sciencecapability.EngineDefinition,
	implementation string,
) bool {
	for _, identity := range append(engine.ManagedExecutionIdentifiers(), engine.ExecutionPack.Skill) {
		if taskImplementationMatchesRegistered(implementation, identity) ||
			taskImplementationMatchesRegistered(identity, implementation) {
			return true
		}
	}
	if s != nil && s.skillCatalog != nil {
		if skill, found := dedicatedSkillForImplementation(s.skillCatalog, implementation); found {
			return strings.EqualFold(strings.TrimSpace(skill.Name), strings.TrimSpace(engine.ExecutionPack.Skill))
		}
	}
	return false
}

func (s *Server) canonicalManagedExecutionPack(
	publicName string,
	input map[string]any,
) (sciencecapability.EngineDefinition, bool) {
	if s == nil || s.scienceCapabilities == nil ||
		!strings.EqualFold(strings.TrimSpace(publicName), "bash") {
		return sciencecapability.EngineDefinition{}, false
	}
	command := strings.TrimSpace(stringValue(input["command"]))
	if command == "" {
		return sciencecapability.EngineDefinition{}, false
	}
	for _, capability := range s.scienceCapabilities.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			pack := engine.ExecutionPack
			if pack.Mode == "local" && commandExecutesManagedExecutionPack(pack.Skill, pack, command) {
				return engine, true
			}
		}
	}
	return sciencecapability.EngineDefinition{}, false
}

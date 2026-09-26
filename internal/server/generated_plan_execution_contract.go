package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
)

func (s *Server) generatedPlanExecutionToolKnown(run *sessionRunnerChatRun, name string) bool {
	if run != nil {
		lock := run.scientificCapabilityLock()
		lock.Lock()
		_, found := run.toolCapabilityCatalog[normalizeAgentToolName(name)]
		captured := run.toolCapabilityCatalog != nil
		lock.Unlock()
		if captured {
			return found
		}
	}
	// Non-provider callers still use the one registered/injected contract
	// catalog; a plan never supplies its own name-to-capability mapping.
	if tool, found := defaultRegisteredAgentRuntimeTool(name); found {
		return string(tool.Exposure) != "hidden"
	}
	return len(annotateOwnedAgentRuntimeToolSchema(agentruntime.ToolSchema{Name: name}).Capabilities) > 0
}

func (s *Server) validateGeneratedPlanExecutionTools(run *sessionRunnerChatRun, plan generatedPlanDocument) error {
	for _, phase := range plan.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				if step.Kind == generatedPlanStepKindExecution && !s.generatedPlanExecutionToolKnown(run, step.ExecutionTool) {
					return fmt.Errorf("step.execution_tool %q is not in the task's tool authority; use an advertised executable tool identity, not a capability label or an invented executor", step.ExecutionTool)
				}
			}
		}
	}
	return nil
}

// A legacy invalid tool identity can be reconciled only to a successful exact
// registered pack with frame-owned execution provenance and verified outputs.
// Plain shell success, a marker file, or model-authored result text is not enough.
func (s *Server) generatedPlanReceiptExecutionPack(ctx context.Context, run *sessionRunnerChatRun, checkpoint sessionRunnerDurableToolCheckpoint, value map[string]any) (string, error) {
	input := map[string]any{}
	if json.Unmarshal(sessionRunnerDurableExecutedToolInput(checkpoint), &input) != nil {
		return "", nil
	}
	engine, found := s.canonicalManagedExecutionPack(checkpoint.ToolName, input)
	if !found || s.workspaceStore == nil {
		return "", nil
	}
	// Reading durable evidence needs workspace ownership, not a currently
	// available execution process or kernel manager.
	access, workspaceDir, authorized := s.resolveAgentWorkspaceAuthority(ctx, run.SessionID)
	if !authorized {
		return "", nil
	}
	executionID := strings.TrimSpace(stringValue(value["exec_id"]))
	record, found, err := s.workspaceStore.GetExecutionLog(run.SessionID, executionID)
	if err != nil || !found {
		return "", err
	}
	bindings, err := s.workspaceStore.KernelLocalExecutionBindings(ctx, access)
	if err != nil {
		return "", err
	}
	pack := engine.ExecutionPack
	if !strings.EqualFold(record.ExitStatus, "ok") || !agentSavedArtifactExecutionAuthorized(record, access, workspaceDir, bindings) ||
		!commandExecutesManagedExecutionPack(pack.Skill, pack, record.Source) {
		return "", nil
	}
	writes, err := managedExecutionReceiptWriteMap(workspaceDir, record.FilesWritten)
	if err != nil {
		return "", err
	}
	for path := range writes {
		if filepath.Base(path) != managedExecutionOutputOwnershipMarker {
			continue
		}
		if _, err := s.verifyManagedExecutionOutputAuthority(ctx, workspaceDir, filepath.Dir(path), pack.ID, record.ID, writes); err != nil {
			return "", err
		}
		return pack.ID, nil
	}
	return "", nil
}

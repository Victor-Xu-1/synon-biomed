package server

import (
	"fmt"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
	"synon-go/internal/tools/registry"
)

const adapterLiveSmokeRuntimeNamespace = "adapter-live-smoke"
const adapterLiveSmokeAuditMaxAge = 24 * time.Hour

const approvalDefaultsSettingKey = "approval.defaults"
const approvalRememberedSettingKey = "approval.rememberedDecisions"

func (s *Server) executeAgentRuntimeDoctorTool(input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("AgentRuntimeDoctor", input); err != nil {
		return nil, err
	}
	scope := strings.TrimSpace(stringValue(input["scope"]))
	if scope == "" {
		scope = "all"
	}
	scope = canonicalAgentRuntimeDoctorScope(scope)
	if !validAgentRuntimeDoctorScope(scope) {
		return nil, fmt.Errorf("AgentRuntimeDoctor.scope is unsupported: %s", scope)
	}
	release := buildinfo.Release()

	toolNames := s.allRegisteredNames()
	areas := make([]map[string]any, 0, 12)
	addArea := func(name string, status string, message string, evidence map[string]any, next []string) {
		if scope != "all" && scope != name {
			return
		}
		area := map[string]any{
			"name":    name,
			"status":  status,
			"message": message,
		}
		if len(evidence) > 0 {
			area["evidence"] = evidence
		}
		if len(next) > 0 {
			area["next"] = next
		}
		areas = append(areas, area)
	}

	queryEngineStatus, queryEngineText, queryEngineEvidence, queryEngineNext := s.queryEngineDoctorArea(toolNames)
	addArea("query-engine", queryEngineStatus, queryEngineText, queryEngineEvidence, queryEngineNext)
	modelRunnerStatus, modelRunnerText, modelRunnerEvidence, modelRunnerNext := s.modelRunnerDoctorArea()
	addArea("model-runner", modelRunnerStatus, modelRunnerText, modelRunnerEvidence, modelRunnerNext)
	toolGatewayStatus, toolGatewayText, toolGatewayEvidence, toolGatewayNext := s.toolGatewayDoctorArea(toolNames)
	addArea("tool-gateway", toolGatewayStatus, toolGatewayText, toolGatewayEvidence, toolGatewayNext)
	permissionsStatus, permissionsText, permissionsEvidence, permissionsNext := s.permissionsDoctorArea()
	addArea("permissions", permissionsStatus, permissionsText, permissionsEvidence, permissionsNext)
	settingsStorageStatus, settingsStorageText, settingsStorageEvidence, settingsStorageNext := s.settingsStorageDoctorArea()
	addArea("settings-storage", settingsStorageStatus, settingsStorageText, settingsStorageEvidence, settingsStorageNext)
	kernelComputeStatus, kernelComputeText, kernelComputeEvidence, kernelComputeNext := s.kernelComputeDoctorArea()
	addArea("kernel-compute", kernelComputeStatus, kernelComputeText, kernelComputeEvidence, kernelComputeNext)
	hooksStatus, hooksText, hooksEvidence, hooksNext := s.hooksDoctorArea()
	addArea("hooks", hooksStatus, hooksText, hooksEvidence, hooksNext)
	skillStatus, skillText, skillEvidence, skillNext := s.skillsDoctorArea()
	addArea("skills", skillStatus, skillText, skillEvidence, skillNext)
	sessionsStatus, sessionsText, sessionsEvidence, sessionsNext := s.sessionsDoctorArea()
	addArea("sessions", sessionsStatus, sessionsText, sessionsEvidence, sessionsNext)
	artifactStatus, artifactText, artifactEvidence, artifactNext := s.artifactSystemDoctorArea()
	addArea("artifact-system", artifactStatus, artifactText, artifactEvidence, artifactNext)
	workspaceWorktreeStatus, workspaceWorktreeText, workspaceWorktreeEvidence, workspaceWorktreeNext := s.workspaceWorktreeDoctorArea()
	addArea("workspace-worktree", workspaceWorktreeStatus, workspaceWorktreeText, workspaceWorktreeEvidence, workspaceWorktreeNext)
	compactStatus, compactText, compactEvidence, compactNext := s.compactMemoryDoctorArea()
	addArea("compact-memory", compactStatus, compactText, compactEvidence, compactNext)
	hasRecordArtifactSystemStepTool := hasRegisteredTool(s.tools, "artifact_register")
	hasShellCommandSystemStepTool := hasRegisteredTool(s.tools, "Shell") && hasRegisteredTool(s.tools, "Bash")
	hasWebResearchSystemStepTool := hasRegisteredTool(s.tools, "web_research") && hasRegisteredTool(s.tools, "web_search")
	hasLSPDiagnosticsSystemStepTool := hasRegisteredTool(s.tools, "LSP")
	hasPatchSystemStepTool := hasRegisteredTool(s.tools, "Patch") && hasRegisteredTool(s.tools, "file_patch")
	hasReadFilesSystemStepTool := hasRegisteredTool(s.tools, "Read") && hasRegisteredTool(s.tools, "ReadBatch")
	hasNotebookEditSystemStepTool := hasRegisteredTool(s.tools, "NotebookEdit")
	hasVisualReviewSystemStepTool := hasRegisteredTool(s.tools, "VisualReview")
	hasOriginalSystemToolSteps := hasRecordArtifactSystemStepTool &&
		hasShellCommandSystemStepTool &&
		hasWebResearchSystemStepTool &&
		hasLSPDiagnosticsSystemStepTool &&
		hasPatchSystemStepTool &&
		hasReadFilesSystemStepTool &&
		hasNotebookEditSystemStepTool &&
		hasVisualReviewSystemStepTool
	hasSessionRunnerNext := hasRegisteredTool(s.tools, "session_runner_next")
	hasSessionHeartbeat := hasRegisteredTool(s.tools, "session_heartbeat")
	hasSessionRelease := hasRegisteredTool(s.tools, "session_release")
	hasSessionRunnerPick := hasRegisteredTool(s.tools, "session_runner_pick")
	hasSessionRunnerCheckpoint := hasRegisteredTool(s.tools, "session_runner_checkpoint")
	hasSessionRunnerFinish := hasRegisteredTool(s.tools, "session_runner_finish")
	hasSessionRunnerQueue := hasRegisteredTool(s.tools, "session_runner_queue")
	hasSessionRunnerBacklog := hasRegisteredTool(s.tools, "session_runner_backlog")
	hasSessionRunnerProtocolTools := hasSessionRunnerNext &&
		hasSessionHeartbeat &&
		hasSessionRelease &&
		hasSessionRunnerPick &&
		hasSessionRunnerCheckpoint &&
		hasSessionRunnerFinish &&
		hasSessionRunnerQueue &&
		hasSessionRunnerBacklog
	hasSessionRunnerStores := s.sessionStore != nil && s.eventJournal != nil
	hasRunnerCheckpointFinishIdempotency := hasSessionRunnerCheckpoint && hasSessionRunnerFinish && hasSessionRunnerStores
	hasRunnerLeaseHeartbeatRenewal := hasSessionRunnerNext && hasSessionHeartbeat && hasSessionRelease && hasSessionRunnerStores
	hasRunnerExpiredLeaseReclaim := hasSessionRunnerNext && hasSessionRunnerPick && hasSessionRunnerStores
	hasRunnerExpiredLeaseStaleWriterRejection := hasRunnerExpiredLeaseReclaim && hasSessionRunnerCheckpoint && hasSessionRunnerFinish && hasSessionRunnerStores
	hasRunnerBacklogRecoveryPlan := hasSessionRunnerBacklog && hasSessionRunnerQueue && hasSessionRunnerStores
	hasTaskRunProgressTrace := hasRunnerCheckpointFinishIdempotency && s.taskRunStore != nil
	hasTaskRunFailureStepAttribution := hasTaskRunProgressTrace && hasSessionRunnerFinish && hasSessionRunnerStores
	hasDelegatedProgressEvents := hasTaskRunProgressTrace && s.eventJournal != nil
	hasTeamCreate := hasRegisteredTool(s.tools, "TeamCreate")
	hasTeamDelete := hasRegisteredTool(s.tools, "TeamDelete")
	hasTeamSubagentOrchestration := hasTeamCreate && hasTeamDelete && s.taskRunStore != nil && s.runtimeStore != nil && hasSessionRunnerStores
	hasSynonLinkService := s.synonLink != nil
	hasSynonLinkTools := hasRegisteredTool(s.tools, "synon_link")
	hasSynonLinkPairingStore := s.pairingStore != nil
	hasSynonLinkClientGate := hasSynonLinkService && hasSynonLinkTools && hasSynonLinkPairingStore
	hasSynonLinkDirectExecutor := hasSynonLinkClientGate && s.taskRunStore != nil && s.runtimeStore != nil
	hasTaskRunTool := hasRegisteredTool(s.tools, "TaskRun")
	hasAgentTool := hasRegisteredTool(s.tools, "Agent")
	hasTaskRunMonitor := hasTaskRunTool && s.taskRunStore != nil && s.runtimeStore != nil && hasSessionRunnerStores && hasSessionRunnerProtocolTools
	hasTaskRunMonitorSelfCheck := hasTaskRunMonitor
	hasAgentReadOnlyPolicy := hasAgentTool
	hasAgentRestrictedPolicy := hasAgentTool
	hasAgentFullAccessPolicy := hasAgentTool
	hasAgentPolicyAliases := hasAgentTool
	hasAgentSessionIsolation := hasAgentTool && s.taskRunStore != nil && hasSessionRunnerStores
	hasPermissionInheritance := hasAgentTool && hasSessionRunnerProtocolTools
	taskRunReady := hasTaskRunTool &&
		hasAgentTool &&
		s.taskRunStore != nil &&
		s.runtimeStore != nil &&
		hasSessionRunnerStores &&
		hasSessionRunnerProtocolTools &&
		hasOriginalSystemToolSteps &&
		hasTeamSubagentOrchestration &&
		hasSynonLinkClientGate &&
		hasSynonLinkDirectExecutor &&
		hasTaskRunMonitor &&
		hasTaskRunMonitorSelfCheck &&
		hasAgentReadOnlyPolicy &&
		hasAgentRestrictedPolicy &&
		hasAgentFullAccessPolicy &&
		hasAgentPolicyAliases &&
		hasAgentSessionIsolation &&
		hasPermissionInheritance &&
		hasRunnerExpiredLeaseStaleWriterRejection &&
		hasTaskRunFailureStepAttribution
	taskRunStatus := "partial"
	taskRunNext := []string{}
	if taskRunReady {
		taskRunStatus = "pass"
	} else {
		if !hasRegisteredTool(s.tools, "TaskRun") || !hasRegisteredTool(s.tools, "Agent") {
			taskRunNext = append(taskRunNext, "Register TaskRun and Agent tools for delegated runtime execution.")
		}
		if s.taskRunStore == nil {
			taskRunNext = append(taskRunNext, "Configure durable TaskRun store.")
		}
		if s.runtimeStore == nil {
			taskRunNext = append(taskRunNext, "Configure runtime KV for goal-run ledgers and active indexes.")
		}
		if !hasSessionRunnerStores {
			taskRunNext = append(taskRunNext, "Configure durable session store and event journal for session runner protocol state.")
		}
		if !hasSessionRunnerProtocolTools {
			taskRunNext = append(taskRunNext, "Register session_runner_next, session_heartbeat, session_release, session_runner_pick, session_runner_checkpoint, session_runner_finish, session_runner_queue, and session_runner_backlog tools.")
		}
		if !hasOriginalSystemToolSteps {
			taskRunNext = append(taskRunNext, "Register every deterministic TaskRun system-step tool: artifact, shell, web research, LSP, patch, read files, notebook edit, and visual review.")
		}
		if !hasTeamSubagentOrchestration {
			taskRunNext = append(taskRunNext, "Register TeamCreate and TeamDelete with durable TaskRun/runtime/session stores for team subagent orchestration.")
		}
		if !hasSynonLinkClientGate || !hasSynonLinkDirectExecutor {
			taskRunNext = append(taskRunNext, "Enable the Synon Link service, canonical synon_link tool, and durable pairing storage for browser TaskRun executors.")
		}
		if !hasTaskRunMonitor || !hasTaskRunMonitorSelfCheck {
			taskRunNext = append(taskRunNext, "Register TaskRun with durable TaskRun/runtime/session runner stores so monitor advance and self-check actions can run.")
		}
	}
	addArea("taskrun-agent", taskRunStatus, "The compatibility TaskRun boundary accepts only explicit task graphs and delegates every executable step to the canonical session runner. It does not infer task categories or retain hard-coded playbook planning. Agent tools queue isolated durable child sessions with explicit Go runtime provenance, delegated agents support inherit/read_only/restricted/full_access policy profiles without exceeding the parent runner tool boundary, and runner progress remains durable and fenced.", map[string]any{
		"hasTaskRun":                                hasTaskRunTool,
		"hasAgent":                                  hasAgentTool,
		"taskRunStore":                              s.taskRunStore != nil,
		"hasSessionRunnerStores":                    hasSessionRunnerStores,
		"hasSessionRunnerProtocolTools":             hasSessionRunnerProtocolTools,
		"hasSessionRunnerNext":                      hasSessionRunnerNext,
		"hasSessionHeartbeat":                       hasSessionHeartbeat,
		"hasSessionRelease":                         hasSessionRelease,
		"hasSessionRunnerPick":                      hasSessionRunnerPick,
		"hasSessionRunnerCheckpoint":                hasSessionRunnerCheckpoint,
		"hasSessionRunnerFinish":                    hasSessionRunnerFinish,
		"hasSessionRunnerQueue":                     hasSessionRunnerQueue,
		"hasSessionRunnerBacklog":                   hasSessionRunnerBacklog,
		"hasTaskRunChatLoop":                        true,
		"hasOriginalSystemToolSteps":                hasOriginalSystemToolSteps,
		"hasRecordArtifactSystemStepTool":           hasRecordArtifactSystemStepTool,
		"hasShellCommandSystemStepTool":             hasShellCommandSystemStepTool,
		"hasWebResearchSystemStepTool":              hasWebResearchSystemStepTool,
		"hasLSPDiagnosticsSystemStepTool":           hasLSPDiagnosticsSystemStepTool,
		"hasPatchSystemStepTool":                    hasPatchSystemStepTool,
		"hasReadFilesSystemStepTool":                hasReadFilesSystemStepTool,
		"hasNotebookEditSystemStepTool":             hasNotebookEditSystemStepTool,
		"hasVisualReviewSystemStepTool":             hasVisualReviewSystemStepTool,
		"hasSynonLinkService":                       hasSynonLinkService,
		"hasSynonLinkTools":                         hasSynonLinkTools,
		"hasSynonLinkPairingStore":                  hasSynonLinkPairingStore,
		"hasSynonLinkClientGate":                    hasSynonLinkClientGate,
		"hasSynonLinkDirectExecutor":                hasSynonLinkDirectExecutor,
		"systemStepOperations":                      []string{"record_artifact", "shell_command", "web_research", "lsp_diagnostics", "patch", "read_files", "notebook_edit", "visual_review"},
		"hasRuntimeProvenance":                      true,
		"hasAgentReadOnlyPolicy":                    hasAgentReadOnlyPolicy,
		"hasAgentRestrictedPolicy":                  hasAgentRestrictedPolicy,
		"hasAgentFullAccessPolicy":                  hasAgentFullAccessPolicy,
		"hasAgentPolicyAliases":                     hasAgentPolicyAliases,
		"hasAgentSessionIsolation":                  hasAgentSessionIsolation,
		"hasPermissionInheritance":                  hasPermissionInheritance,
		"hasTaskRunProgressTrace":                   hasTaskRunProgressTrace,
		"hasTaskRunFailureStepAttribution":          hasTaskRunFailureStepAttribution,
		"hasRunnerLeaseHeartbeatRenewal":            hasRunnerLeaseHeartbeatRenewal,
		"hasRunnerExpiredLeaseReclaim":              hasRunnerExpiredLeaseReclaim,
		"hasRunnerExpiredLeaseStaleWriterRejection": hasRunnerExpiredLeaseStaleWriterRejection,
		"hasRunnerCheckpointFinishIdempotency":      hasRunnerCheckpointFinishIdempotency,
		"hasRunnerBacklogRecoveryPlan":              hasRunnerBacklogRecoveryPlan,
		"hasDelegatedProgressEvents":                hasDelegatedProgressEvents,
		"hasTaskRunGoalLedger":                      s.runtimeStore != nil,
		"hasTaskRunGoalActiveIndex":                 s.runtimeStore != nil,
		"hasTaskRunMonitor":                         hasTaskRunMonitor,
		"hasTaskRunMonitorSelfCheck":                hasTaskRunMonitorSelfCheck,
		"requiresExplicitTaskGraph":                 true,
		"hasHardCodedTaskPlaybooks":                 false,
		"taskRunMonitorActions":                     []string{"reconcile", "advance", "verify"},
		"taskRunGoalLedgerNamespace":                goalRunLedgerRuntimeNamespace,
		"taskRunGoalActiveNamespace":                goalRunActiveRuntimeNamespace,
		"taskRunGoalLedgerStatusMapping":            []string{"completed->completed", "blocked/failed->blocked", "cancelled->stopped", "other->active"},
		"hasTeamCreate":                             hasTeamCreate,
		"hasTeamDelete":                             hasTeamDelete,
		"hasTeamSubagentOrchestration":              hasTeamSubagentOrchestration,
		"chatEngine":                                "internal/agentruntime",
		"runnerProtocol":                            "session_runner",
		"agentDelegationMode":                       "explicit_graph",
		"orchestrationSurfaces":                     []string{"Agent result", "TaskRun record", "session index", "runner backlog"},
		"progressTraceEvents":                       []string{"runner_checkpoint", "runner_finished"},
		"permissionInheritanceMode":                 "inherit/read_only/restricted/full_access profiles never expand beyond parent allowed tools",
	}, taskRunNext)
	mcpStatus, mcpText, mcpEvidence, mcpNext := s.mcpDoctorArea()
	addArea("mcp", mcpStatus, mcpText, mcpEvidence, mcpNext)
	synonLinkIMStatus, synonLinkIMText, synonLinkIMEvidence, synonLinkIMNext := s.synonLinkIMDoctorArea()
	addArea("synon-link-im", synonLinkIMStatus, synonLinkIMText, synonLinkIMEvidence, synonLinkIMNext)

	summary := map[string]int{"pass": 0, "partial": 0, "missing": 0, "fail": 0}
	for _, area := range areas {
		if status, ok := area["status"].(string); ok {
			if _, exists := summary[status]; exists {
				summary[status]++
			}
		}
	}
	return map[string]any{
		"ok":      summary["fail"] == 0 && summary["missing"] == 0 && summary["partial"] == 0,
		"scope":   scope,
		"summary": summary,
		"areas":   areas,
		"contract": map[string]any{
			"source":         "synon-harness",
			"target":         release.MachineSlug,
			"productName":    release.Name,
			"productVersion": release.Version,
			"requirement":    "scientific workbench behavior parity with governed exclusions",
			"tuiMainline":    false,
		},
	}, nil
}

func validAgentRuntimeDoctorScope(scope string) bool {
	switch scope {
	case "all", "query-engine", "model-runner", "tool-gateway", "permissions", "settings-storage", "kernel-compute", "hooks", "skills", "sessions", "artifact-system", "workspace-worktree", "compact-memory", "taskrun-agent", "mcp", "synon-link-im":
		return true
	default:
		return false
	}
}

func canonicalAgentRuntimeDoctorScope(scope string) string {
	switch strings.TrimSpace(scope) {
	case "compute", "molecular-docking":
		return "kernel-compute"
	default:
		return strings.TrimSpace(scope)
	}
}

func hasRegisteredTool(reg *registry.Registry, name string) bool {
	if reg == nil {
		return false
	}
	_, ok := reg.Get(name)
	return ok
}

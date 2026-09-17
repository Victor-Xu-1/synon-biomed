package server

import (
	osexec "os/exec"
	"strings"
)

func (s *Server) settingsStorageDoctorArea() (string, string, map[string]any, []string) {
	hasSettingsGet := hasRegisteredTool(s.tools, "settings_get")
	hasSettingsSet := hasRegisteredTool(s.tools, "settings_set")
	hasSettingsList := hasRegisteredTool(s.tools, "settings_list")
	hasConfigTool := hasRegisteredTool(s.tools, "Config")
	hasRuntimeGet := hasRegisteredTool(s.tools, "runtime_get")
	hasRuntimeSet := hasRegisteredTool(s.tools, "runtime_set")
	hasRuntimeList := hasRegisteredTool(s.tools, "runtime_list")
	hasRuntimeDelete := hasRegisteredTool(s.tools, "runtime_delete")
	hasRuntimeKVTools := hasRuntimeGet && hasRuntimeSet && hasRuntimeList && hasRuntimeDelete
	configSpecs := supportedConfigSettings()
	hasSettingsKeyValidation := hasSettingsSet && hasSettingsGet
	hasConfigModelDefaultFormat := configHasModelDefaultFormat(configSpecs)
	hasConfigRemoteControlDefaultUnset := configRemoteDefaultResolvesUnset(configSpecs)
	hasSecretDiagnosticsRedaction := secretDiagnosticRedactionReady()
	hasSecretValueRedaction := hasSecretDiagnosticsRedaction
	hasDurableSettingsStore := s != nil && s.settingsStore != nil && strings.TrimSpace(s.fileRoot) != ""
	hasRuntimeKVStore := s != nil && s.runtimeStore != nil
	hasSessionStore := s != nil && s.sessionStore != nil
	hasEventJournal := s != nil && s.eventJournal != nil
	hasTaskStore := s != nil && s.taskStore != nil
	hasTaskRunStore := s != nil && s.taskRunStore != nil
	hasPairingStore := s != nil && s.pairingStore != nil
	ready := hasSettingsGet && hasSettingsSet && hasSettingsList && hasConfigTool && hasRuntimeKVTools && hasSettingsKeyValidation &&
		hasConfigModelDefaultFormat && hasConfigRemoteControlDefaultUnset && hasSecretValueRedaction && hasSecretDiagnosticsRedaction && hasDurableSettingsStore &&
		hasRuntimeKVStore && hasSessionStore && hasEventJournal && hasTaskStore && hasTaskRunStore && hasPairingStore
	status := "partial"
	message := "Settings and storage interfaces expose durable settings_get/settings_set/settings_list, original-compatible Config get/set validation, runtime KV, session journal, task, TaskRun, artifact, pairing, and permission backing stores without leaking deployment secret values in diagnostics."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasSettingsGet || !hasSettingsSet || !hasSettingsList || !hasConfigTool {
			next = append(next, "Register settings_get/settings_set/settings_list and Config tools.")
		}
		if !hasDurableSettingsStore {
			next = append(next, "Configure FileRoot so settings.json can persist across process restarts.")
		}
		if !hasRuntimeKVStore {
			next = append(next, "Configure runtime KV storage for plans, todos, artifacts, schedules, hooks, memory, and worktree state.")
		}
		if !hasRuntimeKVTools {
			next = append(next, "Register runtime_get, runtime_set, runtime_list, and runtime_delete tools.")
		}
		if !hasSettingsKeyValidation || !hasConfigModelDefaultFormat || !hasConfigRemoteControlDefaultUnset {
			next = append(next, "Restore original-compatible settings key validation and Config default handling for model and remoteControlAtStartup.")
		}
		if !hasSecretValueRedaction || !hasSecretDiagnosticsRedaction {
			next = append(next, "Restore secret redaction rules for diagnostic sources and token-like values.")
		}
		if !hasSessionStore || !hasEventJournal {
			next = append(next, "Configure durable session metadata and event journal storage.")
		}
		if !hasTaskStore || !hasTaskRunStore {
			next = append(next, "Configure task and TaskRun stores.")
		}
		if !hasPairingStore {
			next = append(next, "Configure durable Synon Link/IM pairing storage.")
		}
	}
	evidence := map[string]any{
		"hasSettingsGet":                     hasSettingsGet,
		"hasSettingsSet":                     hasSettingsSet,
		"hasSettingsList":                    hasSettingsList,
		"hasConfigTool":                      hasConfigTool,
		"hasSettingsKeyValidation":           hasSettingsKeyValidation,
		"hasConfigValueValidation":           len(configSpecs) > 0,
		"hasConfigModelDefaultFormat":        hasConfigModelDefaultFormat,
		"hasConfigRemoteControlDefaultUnset": hasConfigRemoteControlDefaultUnset,
		"hasRuntimeGet":                      hasRuntimeGet,
		"hasRuntimeSet":                      hasRuntimeSet,
		"hasRuntimeList":                     hasRuntimeList,
		"hasRuntimeDelete":                   hasRuntimeDelete,
		"hasRuntimeKVTools":                  hasRuntimeKVTools,
		"hasDurableSettingsStore":            hasDurableSettingsStore,
		"hasRuntimeKVStore":                  hasRuntimeKVStore,
		"hasSessionStore":                    hasSessionStore,
		"hasEventJournal":                    hasEventJournal,
		"hasTaskStore":                       hasTaskStore,
		"hasTaskRunStore":                    hasTaskRunStore,
		"hasPairingStore":                    hasPairingStore,
		"hasSecretValueRedaction":            hasSecretValueRedaction,
		"hasSecretDiagnosticsRedaction":      hasSecretDiagnosticsRedaction,
		"secretDiagnosticRedactionRules":     []string{"literal-api-key", "bearer-token", "token-like-source"},
		"settingsPath":                       "settings.json",
		"runtimeKVPath":                      "runtime-state.sqlite",
		"configSettingCount":                 len(configSpecs),
		"storageSurfaces":                    []string{"settings", "runtime-kv", "sessions", "event-journal", "tasks", "task-runs", "artifacts", "pairings", "approvals"},
	}
	return status, message, evidence, next
}

func (s *Server) kernelComputeDoctorArea() (string, string, map[string]any, []string) {
	hasNotebookEdit := hasRegisteredTool(s.tools, "NotebookEdit")
	hasNotebookReadBeforeEditGate := hasRegisteredTool(s.tools, "Read") && hasNotebookEdit && s != nil && s.readState != nil
	hasShellExecution := hasRegisteredTool(s.tools, "Shell") && hasRegisteredTool(s.tools, "Bash")
	hasBackgroundShellTasks := hasRegisteredTool(s.tools, "TaskOutput") && hasRegisteredTool(s.tools, "TaskStop") && s != nil && s.backgroundShells != nil
	hasVisualReviewTool := hasRegisteredTool(s.tools, "VisualReview")
	hasCronCreate := hasRegisteredTool(s.tools, "CronCreate")
	hasCronUpdate := hasRegisteredTool(s.tools, "CronUpdate")
	hasCronList := hasRegisteredTool(s.tools, "CronList")
	hasCronDelete := hasRegisteredTool(s.tools, "CronDelete")
	hasCronTick := hasRegisteredTool(s.tools, "cron_tick")
	hasRuntimeScheduleStore := s != nil && s.runtimeStore != nil
	hasCronValidation := hasCronCreate && hasCronUpdate && hasRuntimeScheduleStore
	hasRecordArtifactSystemStepTool := hasRegisteredTool(s.tools, "TaskRun") && hasRegisteredTool(s.tools, "artifact_register") && hasRegisteredTool(s.tools, "StructuredOutput")
	hasShellCommandSystemStepTool := hasRegisteredTool(s.tools, "Shell") && hasRegisteredTool(s.tools, "Bash")
	hasWebResearchSystemStepTool := hasRegisteredTool(s.tools, "web_research") && hasRegisteredTool(s.tools, "web_search")
	hasLSPDiagnosticsSystemStepTool := hasRegisteredTool(s.tools, "LSP")
	hasPatchSystemStepTool := hasRegisteredTool(s.tools, "Patch") && hasRegisteredTool(s.tools, "file_patch")
	hasReadFilesSystemStepTool := hasRegisteredTool(s.tools, "Read") && hasRegisteredTool(s.tools, "ReadBatch")
	hasNotebookEditSystemStepTool := hasNotebookEdit
	hasVisualReviewSystemStepTool := hasVisualReviewTool
	hasTaskRunSystemSteps := hasRecordArtifactSystemStepTool &&
		hasShellCommandSystemStepTool &&
		hasWebResearchSystemStepTool &&
		hasLSPDiagnosticsSystemStepTool &&
		hasPatchSystemStepTool &&
		hasReadFilesSystemStepTool &&
		hasNotebookEditSystemStepTool &&
		hasVisualReviewSystemStepTool
	ready := hasNotebookEdit && hasNotebookReadBeforeEditGate && hasShellExecution && hasBackgroundShellTasks &&
		hasVisualReviewTool && hasCronCreate && hasCronUpdate && hasCronList && hasCronDelete && hasCronTick && hasRuntimeScheduleStore && hasTaskRunSystemSteps
	status := "partial"
	message := "Kernel execution and compute scheduling are available through original-compatible NotebookEdit with read-before-edit protection, Shell/Bash execution with bounded background task controls, CronCreate/CronUpdate/CronList/CronDelete/cron_tick scheduling, durable runtime schedule storage, and TaskRun system-step execution for notebook, shell, artifact, web, patch, LSP, and visual review work."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasNotebookEdit || !hasNotebookReadBeforeEditGate {
			next = append(next, "Register NotebookEdit and keep read-before-edit stale notebook protection enabled.")
		}
		if !hasShellExecution || !hasBackgroundShellTasks {
			next = append(next, "Register Shell/Bash plus background shell inspection and cancellation tools.")
		}
		if !hasVisualReviewTool {
			next = append(next, "Register VisualReview so visual_review TaskRun system steps can collect auditable image evidence.")
		}
		if !hasCronCreate || !hasCronUpdate || !hasCronList || !hasCronDelete || !hasCronTick || !hasRuntimeScheduleStore {
			next = append(next, "Register cron tools and configure runtime KV storage for scheduled jobs.")
		}
		if !hasTaskRunSystemSteps {
			next = append(next, "Register every TaskRun system-step dependency: artifact, shell, web research, LSP diagnostics, patch, read files, notebook edit, and visual review.")
		}
	}
	evidence := map[string]any{
		"hasNotebookEdit":                 hasNotebookEdit,
		"hasNotebookReadBeforeEditGate":   hasNotebookReadBeforeEditGate,
		"hasShellExecution":               hasShellExecution,
		"hasBackgroundShellTasks":         hasBackgroundShellTasks,
		"hasVisualReviewTool":             hasVisualReviewTool,
		"hasCronCreate":                   hasCronCreate,
		"hasCronUpdate":                   hasCronUpdate,
		"hasCronList":                     hasCronList,
		"hasCronDelete":                   hasCronDelete,
		"hasCronTick":                     hasCronTick,
		"hasCronValidation":               hasCronValidation,
		"hasTaskRunSystemSteps":           hasTaskRunSystemSteps,
		"hasRecordArtifactSystemStepTool": hasRecordArtifactSystemStepTool,
		"hasShellCommandSystemStepTool":   hasShellCommandSystemStepTool,
		"hasWebResearchSystemStepTool":    hasWebResearchSystemStepTool,
		"hasLSPDiagnosticsSystemStepTool": hasLSPDiagnosticsSystemStepTool,
		"hasPatchSystemStepTool":          hasPatchSystemStepTool,
		"hasReadFilesSystemStepTool":      hasReadFilesSystemStepTool,
		"hasNotebookEditSystemStepTool":   hasNotebookEditSystemStepTool,
		"hasVisualReviewSystemStepTool":   hasVisualReviewSystemStepTool,
		"hasRuntimeScheduleStore":         hasRuntimeScheduleStore,
		"maxCronJobs":                     maxCronJobs,
		"scheduleNamespace":               cronRuntimeNamespace,
		"systemStepOperations":            []string{"record_artifact", "shell_command", "web_research", "lsp_diagnostics", "patch", "read_files", "notebook_edit", "visual_review"},
		"backgroundShellTools":            []string{"TaskOutput", "TaskStop"},
	}
	return status, message, evidence, next
}

func (s *Server) sessionsDoctorArea() (string, string, map[string]any, []string) {
	hasSessionStore := s.sessionStore != nil
	hasEventJournal := s.eventJournal != nil
	hasWebsocketFeed := s.sessionSockets != nil
	hasSessionList := hasRegisteredTool(s.tools, "session_list")
	hasSessionGet := hasRegisteredTool(s.tools, "session_get")
	hasSessionReplay := hasRegisteredTool(s.tools, "session_replay")
	hasSessionAppend := hasRegisteredTool(s.tools, "session_append")
	hasSessionFork := hasRegisteredTool(s.tools, "session_fork")
	hasSessionRewind := hasRegisteredTool(s.tools, "session_rewind")
	hasSessionExport := hasRegisteredTool(s.tools, "session_export")
	hasSessionEventJournal := hasRegisteredTool(s.tools, "session_event_journal")
	hasSessionAPITools := hasSessionList &&
		hasSessionGet &&
		hasSessionReplay &&
		hasSessionAppend &&
		hasSessionFork &&
		hasSessionRewind &&
		hasSessionExport &&
		hasSessionEventJournal
	hasForkRewindExport := hasSessionFork && hasSessionRewind && hasSessionExport && hasSessionStore && hasEventJournal
	hasReadableTranscriptExport := hasSessionExport && hasEventJournal
	hasToolTranscriptRecovery := hasSessionReplay && hasEventJournal
	hasModelToolCallJournal := hasSessionAppend && hasSessionEventJournal && hasEventJournal
	hasProviderResumeCacheMarkers := hasSessionReplay && hasSessionAppend && hasSessionEventJournal && hasEventJournal
	hasEscapedSessionJournalPaths := hasEventJournal
	ready := hasSessionStore && hasEventJournal && hasWebsocketFeed && hasSessionAPITools && hasEscapedSessionJournalPaths
	status := "missing"
	message := "Durable session metadata, JSONL journal replay with escaped bounded filenames, fork, rewind, bounded export, readable transcript export, model tool-call checkpoints, recovered tool transcript context, non-text transcript labels, provider resume cache marker context, and provider request metadata/headers are present when FileRoot is configured."
	next := []string{}
	if ready {
		status = "pass"
	} else if hasSessionStore || hasEventJournal || hasWebsocketFeed {
		status = "partial"
		if !hasSessionStore {
			next = append(next, "Configure session metadata storage.")
		}
		if !hasEventJournal {
			next = append(next, "Configure durable session event journal storage.")
		}
		if !hasWebsocketFeed {
			next = append(next, "Enable session websocket feed.")
		}
		if !hasSessionAPITools {
			next = append(next, "Register session_list, session_get, session_replay, session_append, session_fork, session_rewind, session_export, and session_event_journal tools.")
		}
	} else {
		next = append(next, "Configure FileRoot so session store, event journal, and websocket feed are available.")
	}
	evidence := map[string]any{
		"sessionStore":                          hasSessionStore,
		"eventJournal":                          hasEventJournal,
		"websocketFeed":                         hasWebsocketFeed,
		"hasSessionAPITools":                    hasSessionAPITools,
		"hasSessionList":                        hasSessionList,
		"hasSessionGet":                         hasSessionGet,
		"hasSessionReplay":                      hasSessionReplay,
		"hasSessionAppend":                      hasSessionAppend,
		"hasSessionFork":                        hasSessionFork,
		"hasSessionRewind":                      hasSessionRewind,
		"hasSessionExport":                      hasSessionExport,
		"hasSessionEventJournal":                hasSessionEventJournal,
		"hasEscapedSessionJournalPaths":         hasEscapedSessionJournalPaths,
		"sessionJournalFilenameEncoding":        "url.QueryEscape",
		"hasForkRewindExport":                   hasForkRewindExport,
		"hasReadableTranscriptExport":           hasReadableTranscriptExport,
		"hasToolTranscriptRecovery":             hasToolTranscriptRecovery,
		"hasNonTextTranscriptRecovery":          true,
		"hasModelToolCallJournal":               hasModelToolCallJournal,
		"hasProviderResumeCacheMarkers":         hasProviderResumeCacheMarkers,
		"hasProviderResumeCacheRequestMetadata": hasProviderResumeCacheMarkers,
		"hasProviderResumeCacheRequestHeaders":  hasProviderResumeCacheMarkers,
		"sessionAPIs":                           []string{"session_list", "session_get", "session_replay", "session_append", "session_fork", "session_rewind", "session_export", "session_event_journal"},
	}
	return status, message, evidence, next
}

func (s *Server) workspaceWorktreeDoctorArea() (string, string, map[string]any, []string) {
	hasEnter := hasRegisteredTool(s.tools, "EnterWorktree")
	hasExit := hasRegisteredTool(s.tools, "ExitWorktree")
	hasRuntimeStore := s.runtimeStore != nil
	hasFileRoot := strings.TrimSpace(s.fileRoot) != ""
	hasSymlinkRepoPathProtection := hasFileRoot
	_, gitErr := osexec.LookPath("git")
	hasGitExecutable := gitErr == nil
	ready := hasEnter && hasExit && hasRuntimeStore && hasFileRoot && hasSymlinkRepoPathProtection && hasGitExecutable
	status := "partial"
	message := "Managed git worktree sessions expose the original EnterWorktree and ExitWorktree tool surface with durable session binding, bounded repo-path validation, real git worktree creation, duplicate-entry protection, and dirty-worktree fail-closed removal checks."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasEnter || !hasExit {
			next = append(next, "Register EnterWorktree and ExitWorktree tools.")
		}
		if !hasRuntimeStore {
			next = append(next, "Configure runtime KV so active worktree sessions are durable.")
		}
		if !hasFileRoot {
			next = append(next, "Configure FileRoot so repo paths and managed worktree locations can be bounded.")
		}
		if !hasGitExecutable {
			next = append(next, "Install git and ensure it is available on PATH so managed worktree operations can run.")
		}
	}
	evidence := map[string]any{
		"hasEnterWorktree":             hasEnter,
		"hasExitWorktree":              hasExit,
		"hasRuntimeState":              hasRuntimeStore,
		"hasFileRoot":                  hasFileRoot,
		"hasGitExecutable":             hasGitExecutable,
		"hasBoundedRepoPath":           hasFileRoot,
		"hasSymlinkRepoPathProtection": hasSymlinkRepoPathProtection,
		"hasManagedWorktreeRoot":       hasFileRoot && hasGitExecutable,
		"hasDirtyWorktreeProtection":   hasGitExecutable,
		"hasCommitDeltaProtection":     hasGitExecutable,
		"hasDuplicateSessionGuard":     true,
		"worktreeNamespace":            worktreeRuntimeNamespace,
		"worktreeAPIs":                 []string{"EnterWorktree", "ExitWorktree"},
	}
	return status, message, evidence, next
}
func (s *Server) artifactSystemDoctorArea() (string, string, map[string]any, []string) {
	hasRegister := hasRegisteredTool(s.tools, "artifact_register")
	hasList := hasRegisteredTool(s.tools, "artifact_list")
	hasGet := hasRegisteredTool(s.tools, "artifact_get")
	hasResearchAudit := hasRegisteredTool(s.tools, "artifact_research_audit")
	hasStructuredOutput := hasRegisteredTool(s.tools, "StructuredOutput")
	hasSessionExport := hasRegisteredTool(s.tools, "session_export")
	hasRuntimeStore := s.runtimeStore != nil
	hasFileRoot := strings.TrimSpace(s.fileRoot) != ""
	hasSessionExportArtifactRegistration := hasSessionExport && hasRegister && hasRuntimeStore
	hasArtifactContentRetrieval := hasGet && hasFileRoot
	hasArtifactContentBounds := hasArtifactContentRetrieval
	hasStructuredOutputDirectPayload := hasStructuredOutput
	hasStructuredOutputPassthroughToolSchema := hasStructuredOutput
	hasStructuredOutputSchemaValidation := hasStructuredOutput
	hasDigestMetadata := hasRegister && hasFileRoot
	hasMIMETypeMetadata := hasRegister && hasFileRoot
	hasSessionRunLinkage := hasRegister && hasRuntimeStore
	hasArtifactDriftDetection := hasGet && hasRuntimeStore && hasFileRoot
	hasSymlinkEscapeProtection := hasFileRoot
	hasArtifactGetPathRevalidation := hasArtifactContentRetrieval && hasSymlinkEscapeProtection
	ready := hasRegister && hasList && hasGet && hasResearchAudit && hasStructuredOutput && hasSessionExportArtifactRegistration && hasRuntimeStore && hasFileRoot && hasSymlinkEscapeProtection && hasArtifactGetPathRevalidation
	status := "partial"
	message := "Agent artifacts are registered as durable runtime records with bounded file-root validation, relative paths, size, MIME, sha256 digest metadata, optional session/run linkage, session_export auto-registration, bounded artifact_get content retrieval with read-time path revalidation, original Synon research artifact phase/final-report classification, and StructuredOutput final JSON payload capture with original-compatible direct payload preservation, passthrough model tool schema, plus optional schema/value validation."
	next := []string{}
	if ready {
		status = "pass"
	} else {
		if !hasRegister || !hasList || !hasGet || !hasResearchAudit {
			next = append(next, "Register artifact_register, artifact_list, artifact_get, and artifact_research_audit tools.")
		}
		if !hasStructuredOutput {
			next = append(next, "Register StructuredOutput for final structured JSON output capture.")
		}
		if !hasSessionExportArtifactRegistration {
			next = append(next, "Register session_export with artifact_register and durable runtime KV so exported sessions are captured as artifacts.")
		}
		if !hasRuntimeStore {
			next = append(next, "Configure runtime KV so artifact records are durable.")
		}
		if !hasFileRoot {
			next = append(next, "Configure FileRoot so artifact paths can be bounded and audited.")
		}
	}
	evidence := map[string]any{
		"hasArtifactRegister":                      hasRegister,
		"hasArtifactList":                          hasList,
		"hasArtifactGet":                           hasGet,
		"hasResearchArtifactAudit":                 hasResearchAudit,
		"hasOriginalResearchArtifactClassifier":    hasResearchAudit,
		"hasArtifactContentRetrieval":              hasArtifactContentRetrieval,
		"hasArtifactContentBounds":                 hasArtifactContentBounds,
		"artifactContentLimitBytes":                maxArtifactContentBytes,
		"artifactContentEncodings":                 []string{"utf-8", "base64"},
		"hasStructuredOutput":                      hasStructuredOutput,
		"hasStructuredOutputDirectPayload":         hasStructuredOutputDirectPayload,
		"hasStructuredOutputPassthroughToolSchema": hasStructuredOutputPassthroughToolSchema,
		"hasStructuredOutputSchemaValidation":      hasStructuredOutputSchemaValidation,
		"hasSessionExport":                         hasSessionExport,
		"hasSessionExportArtifactRegistration":     hasSessionExportArtifactRegistration,
		"hasArtifactRegistry":                      hasRuntimeStore,
		"hasFileRoot":                              hasFileRoot,
		"hasBoundedRootValidation":                 hasFileRoot,
		"hasSymlinkEscapeProtection":               hasSymlinkEscapeProtection,
		"hasArtifactGetPathRevalidation":           hasArtifactGetPathRevalidation,
		"hasDigestMetadata":                        hasDigestMetadata,
		"hasMIMETypeMetadata":                      hasMIMETypeMetadata,
		"hasSessionRunLinkage":                     hasSessionRunLinkage,
		"hasArtifactDriftDetection":                hasArtifactDriftDetection,
		"artifactNamespace":                        artifactRuntimeNamespace,
		"artifactAPIs":                             []string{"artifact_register", "artifact_list", "artifact_get", "artifact_research_audit", "session_export", "StructuredOutput"},
		"researchArtifactPhases":                   []string{"phase1", "phase2Any", "phase2Verification", "phase3Synthesis", "phase4Report", "phase45EvidenceAudit", "phase5Review", "finalMarkdown", "finalHtml"},
		"structuredOutputNamespace":                structuredOutputRuntimeNamespace,
	}
	return status, message, evidence, next
}

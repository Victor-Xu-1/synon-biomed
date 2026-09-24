package server

import (
	"context"

	"errors"
	"fmt"

	"log"

	"strings"

	"time"

	"synon-go/internal/agentruntime"

	eventjournal "synon-go/internal/persistence/journal"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"

	"synon-go/internal/providers"
)

func sessionRunnerPreparationStageDetail(stage string) string {
	switch strings.TrimSpace(stage) {
	case "tool_discovery":
		return "discovering the allowed runtime tool catalog"
	case "resume_state":
		return "restoring the durable assistant continuation state"
	case "completion_recovery":
		return "checking whether an immutable prior candidate can finish without regeneration"
	case "agent_authority":
		return "resolving the selected agent and its bounded capability policy"
	case "model_resolution":
		return "resolving the currently selected model authority"
	case "kernel_recovery":
		return "restoring approved durable kernel operations"
	case "history_replay":
		return "loading the bounded authoritative transcript replay"
	case "history_compaction":
		return "preparing bounded durable conversation history"
	case "context_replay":
		return "restoring canonical task and continuation context"
	case "input_materialization":
		return "materializing immutable attached inputs in the task workspace"
	case "lifecycle_hooks":
		return "running bounded lifecycle hooks"
	case "skill_discovery":
		return "resolving allowed skills and scientific capabilities"
	case "review_policy":
		return "resolving the user-authorized review policy"
	case "memory_context":
		return "restoring task-scoped memory context"
	case "execution_plan":
		return "building and checkpointing the execution plan"
	case "scientific_preflight":
		return "verifying required scientific compute capabilities"
	case "task_contract":
		return "binding the canonical task identity and acceptance contract"
	case "prompt_snapshot":
		return "persisting the exact prompt, tool and Skill snapshot"
	case "model_execution":
		return "starting model execution"
	default:
		return "preparing task execution"
	}
}

func sessionRunnerPreparationStageIdentity(run *sessionRunnerChatRun, stage string) string {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "unknown"
	}
	var resumeCheckpoint int64
	if run != nil && run.Transcript != nil {
		resumeCheckpoint = run.Transcript.Claim.ResumeCheckpoint
	}
	return fmt.Sprintf(
		"runner-preparation-%06d-%06d-%s",
		run.assistantSegmentOrdinal(), resumeCheckpoint, stage,
	)
}

func (s *Server) checkpointSessionRunnerPreparationStage(
	ctx context.Context,
	run *sessionRunnerChatRun,
	stage string,
) error {
	if run == nil {
		return nil
	}
	if err := advanceSessionRunnerPreparationPhase(run, stage); err != nil {
		return err
	}
	if run.Transcript == nil {
		return nil
	}
	operationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), sessionRunnerContentDeltaPersistenceTimeout,
	)
	defer cancel()
	err := s.checkpointTranscriptRunner(
		operationCtx, run.Transcript, transcriptstore.RunnerPhasePlanning,
		sessionRunnerPreparationStageIdentity(run, stage), map[string]any{
			"status":         "running",
			"stage":          strings.TrimSpace(stage),
			"detail":         sessionRunnerPreparationStageDetail(stage),
			"lifecyclePhase": sessionRunnerLifecyclePhase(run),
		}, true,
	)
	return classifySessionRunnerPreparationStageError(
		stage, sessionRunnerContentDeltaPersistenceTimeout, operationCtx, err,
	)
}

func (s *Server) runSessionRunnerChat(ctx context.Context, options SessionRunnerChatOptions, session sessionstore.Session, entries []eventjournal.Entry, run *sessionRunnerChatRun) (assistantMessage string, err error) {
	if err := ensureSessionRunnerExecutionPhaseMachine(run); err != nil {
		return "", err
	}
	if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
		// Transcript-backed Frames are the runtime authority. Keep the
		// gateway parent context aligned even when the legacy options path
		// started without a SessionID.
		options.SessionID = sessionID
	}
	if options.PreparationTimeout <= 0 {
		options.PreparationTimeout = defaultSessionRunnerChatPreparationTimeout
	}
	parentCtx := ctx
	preparationStage := "tool_discovery"
	preparationCtx, cancelPreparation := context.WithTimeoutCause(
		ctx, options.PreparationTimeout, errSessionRunnerPreparationDeadline,
	)
	preparationActive := true
	ctx = preparationCtx
	defer func() {
		cancelPreparation()
		if preparationActive && errors.Is(context.Cause(preparationCtx), errSessionRunnerPreparationDeadline) {
			assistantMessage = ""
			err = sessionRunnerPreparationTimeout{
				stage: preparationStage, timeout: options.PreparationTimeout, cause: context.DeadlineExceeded,
			}
		}
	}()
	checkpointPreparationStage := func(stage string) error {
		if err := preparationCtx.Err(); err != nil {
			return err
		}
		preparationStage = stage
		return s.checkpointSessionRunnerPreparationStage(preparationCtx, run, stage)
	}
	if err := checkpointPreparationStage(preparationStage); err != nil {
		return "", err
	}
	configuredAllowedToolCount := len(options.AllowedTools)
	// Use the authoritative Transcript owner while binding workspace-scoped
	// tools; the browser user is intentionally not the legacy session fallback.
	toolAuthority, err := s.bindSessionRunnerToolAuthority(
		withTranscriptRunnerChatRun(ctx, run), session, options,
	)
	if err != nil {
		return "", err
	}
	options = toolAuthority.Options
	runtimeToolUniverse := toolAuthority.Schemas
	policyContext := toolAuthority.PolicyContext
	if err := checkpointPreparationStage("history_compaction"); err != nil {
		return "", err
	}
	updatedEntries, autoCompactResult, err := s.autoCompactSessionForRunner(ctx, options, session, entries, run)
	if err != nil {
		log.Printf("session runner history compaction failed session=%s attempt=%d err_type=%T: %v",
			session.ID, run.Attempt, err, err)
		return "", err
	}
	if autoCompactResult.Triggered {
		entries = updatedEntries
	}
	if err := checkpointPreparationStage("context_replay"); err != nil {
		return "", err
	}
	if err := checkpointPreparationStage("input_materialization"); err != nil {
		return "", err
	}
	entries, err = s.prepareRunnerUserArtifactsForProvider(ctx, session.ID, entries)
	if err != nil {
		return "", err
	}
	trustedRuntimeNow := time.Now().UTC()
	trustedRuntimeContext := sessionRunnerTrustedRuntimeContext(
		trustedRuntimeNow, sessionRunnerTaskStartedAt(session, run), session, run,
	)
	messages, err := sessionEntriesToProviderMessages(
		appendSessionRunnerTrustedRuntimeContext(options.SystemPrompt, trustedRuntimeContext), entries,
	)
	if err != nil {
		return "", err
	}
	if !hasChatRole(messages, "user") {
		return "", errors.New("runner chat requires at least one user message")
	}
	currentLogicalEntries := entries
	if run != nil {
		// A completed Skill result in the exact provider replay is durable
		// execution authority. Restore it before constructing a new engine after
		// approval, AskUser, or process recovery so task-scoped execution routing
		// does not silently disappear at continuation boundaries.
		visibleSkillNames := providerVisibleSkillNames(messages)
		run.addExecutedSkillNames(visibleSkillNames...)
		for _, skillName := range visibleSkillNames {
			run.addExecutedSkillInvocationKeys(runtimeSkillInvocationKey(skillName, "", ""))
		}
		if err := s.loadProviderContinuation(ctx, run); err != nil {
			return "", err
		}
		scientificEntries, err := s.runnerEntriesForCurrentLogicalInput(ctx, entries, run)
		if err != nil {
			return "", err
		}
		currentLogicalEntries = scientificEntries
		run.setInputAttachmentReaders(inputAttachmentReaderStatesFromRunnerEntries(scientificEntries))
		run.PlanModeDenials = sessionRunnerPlanModeDenialCount(scientificEntries)
		// Rehydrate durable capability and Skill authority before validating an
		// auxiliary resolver. These receipts can be pinned independently of the
		// bounded provider message projection in a long task.
		run.addRequiredScientificCapabilities(requiredScientificCapabilitiesFromRunnerEntries(scientificEntries)...)
		run.addExecutedSkillNames(completedSkillNamesFromRunnerEntries(scientificEntries)...)
		run.setSelectedImplementations(selectedAskUserImplementationsFromRunnerEntries(scientificEntries)...)
		selectedResolvers, validResolvers := validatedSelectedAskUserEvidenceResolvers(
			s.skillCatalog, s.scienceCapabilities, run,
			selectedAskUserEvidenceResolversFromRunnerEntries(scientificEntries),
		)
		if !validResolvers {
			return "", errors.New("answered AskUser evidence resolver is not authorized by the active capability registry")
		}
		run.setSelectedEvidenceResolvers(selectedResolvers...)
		options = allowSelectedEvidenceResolverSkills(options, run)
		run.setResolvedUserEvidence(resolvedAskUserEvidenceFromRunnerEntries(scientificEntries))
		selectionRequired := implementationSelectionRequiredFromRunnerEntries(scientificEntries)
		if len(run.selectedImplementationsSnapshot()) > 0 {
			selectionRequired = false
		}
		run.setImplementationSelectionRequired(selectionRequired)
		run.addExecutedSkillInvocationKeys(completedSkillInvocationKeysFromRunnerEntries(scientificEntries)...)
		run.addTrustedScientificReviewSignals(trustedScientificReviewSignalsFromRunnerEntries(scientificEntries)...)
		run.addTrustedScientificCapabilityWitnesses(trustedScientificCapabilityWitnessesFromRunnerEntries(scientificEntries)...)
		messages = appendProviderContinuationContext(messages, run.ProviderContinuation)
	}
	taskIntent := promptContextFromMessages(messages)
	taskLanguage := sessionRunnerResponseLanguage(taskIntent)
	options.sourceToolActivity = newSessionRunnerSourceToolActivity(
		sessionRunnerGenericSourceToolAttemptCount(currentLogicalEntries),
	)
	taskIntentID := ""
	var taskIntentRevision int64
	if run != nil && run.Transcript != nil {
		if run.Transcript.Stream.Kind == transcriptstore.StreamKindFrameRef {
			intent, found, err := s.transcriptStore.EnsureActiveFrameTaskIntent(ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID)
			if err != nil {
				return "", fmt.Errorf("resolve canonical frame task intent: %w", err)
			}
			if !found || strings.TrimSpace(intent.Text) == "" {
				return "", errors.New("canonical frame task intent is unavailable")
			}
			taskIntent = strings.TrimSpace(intent.Text)
			taskLanguage = strings.TrimSpace(intent.Language)
			taskIntentID = strings.TrimSpace(intent.ID)
			taskIntentRevision = intent.Revision
		} else {
			taskIntentID = fmt.Sprintf("%s:input:%d", run.Transcript.Stream.UID, run.Transcript.Claim.ClaimedInputRevision)
			taskIntentRevision = run.Transcript.Claim.ClaimedInputRevision
		}
		if strings.TrimSpace(taskIntent) == "" {
			return "", errors.New("canonical transcript task intent is unavailable")
		}
		currentTaskIntent := taskIntent
		if continuationEntries := sessionRunnerContinuationEvidenceEntries(currentTaskIntent, entries); len(continuationEntries) > 0 {
			if rootTaskIntent := sessionRunnerContinuationRootTaskIntent(currentTaskIntent, entries); rootTaskIntent != "" {
				taskIntent = rootTaskIntent
				taskLanguage = sessionRunnerResponseLanguage(rootTaskIntent)
			}
			run.addRequiredScientificCapabilities(requiredScientificCapabilitiesFromRunnerEntries(continuationEntries)...)
			run.addExecutedSkillNames(completedSkillNamesFromRunnerEntries(continuationEntries)...)
			run.addExecutedSkillInvocationKeys(completedSkillInvocationKeysFromRunnerEntries(continuationEntries)...)
			run.addTrustedScientificReviewSignals(trustedScientificReviewSignalsFromRunnerEntries(continuationEntries)...)
			run.addTrustedScientificCapabilityWitnesses(trustedScientificCapabilityWitnessesFromRunnerEntries(continuationEntries)...)
			run.ContinuationArtifactReferences = artifactReferencesFromRunnerEntries(continuationEntries)
		}
		run.TaskIntent = taskIntent
		run.TaskIntentID = taskIntentID
		run.TaskIntentRevision = taskIntentRevision
		messages = deactivateProviderSkillResultsForSelectedImplementation(
			messages, s.skillCatalog, run.selectedImplementationsSnapshot(), run.TaskIntent,
		)
	}
	if run != nil {
		run.ResponseLanguage = canonicalSessionRunnerResponseLanguage(taskLanguage)
	}
	var researchModelContext map[string]any
	_, _, hasCompactBoundary := latestCompactModelContext(entries)
	if run != nil && (run.Attempt > 1 || hasCompactBoundary) {
		var researchContextErr error
		researchModelContext, researchContextErr = s.sessionRunnerResearchModelContext(ctx, run)
		if researchContextErr != nil {
			return "", fmt.Errorf("restore durable task research context: %w", researchContextErr)
		}
		if len(researchModelContext) > 0 {
			messages = attachRuntimeTaskResearchContext(messages, researchModelContext)
		}
	}
	if runnerRequiresPublicScientificDownloadRecovery(entries) && run != nil && run.Transcript != nil {
		recovery, recoveryErr := s.agentPublicScientificDownloadRecoveryState(
			ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID,
			agentPublicScientificRecoveryCandidateLimit,
		)
		if recoveryErr != nil {
			return "", fmt.Errorf("recover legacy public scientific download state: %w", recoveryErr)
		}
		messages = appendRuntimeAgentPolicyContextMessage(
			messages, agentPublicScientificDownloadRecoveryStateContext(recovery),
		)
	}
	if strings.TrimSpace(policyContext) != "" {
		messages = appendRuntimeAgentPolicyContextMessage(messages, policyContext)
	}
	toolContinuity, err := sessionRunnerToolContinuityRecords(currentLogicalEntries)
	if err != nil {
		return "", err
	}
	if continuityContext := sessionRunnerToolContinuityContext(toolContinuity); continuityContext != "" {
		messages = appendRuntimeAgentPolicyContextMessage(messages, continuityContext)
	}
	replRecoveryAttempt := 0
	if run != nil {
		replRecoveryAttempt = run.Attempt
	}
	if recoveryContext := sessionRunnerREPLRecoveryContext(options.TranscriptResumeSource, replRecoveryAttempt); recoveryContext != "" {
		messages = appendRuntimeAgentPolicyContextMessage(messages, recoveryContext)
	}
	intakeFrameID := strings.TrimSpace(session.ID)
	if run != nil && run.Transcript != nil && strings.TrimSpace(run.Transcript.Stream.FrameID) != "" {
		intakeFrameID = strings.TrimSpace(run.Transcript.Stream.FrameID)
	}
	if err := checkpointPreparationStage("lifecycle_hooks"); err != nil {
		return "", err
	}
	hookContext, err := s.runSessionRunnerLifecycleHooks(ctx, options, session, messages)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(hookContext) != "" {
		messages = appendRuntimeHookContextMessage(messages, hookContext)
	}
	if err := checkpointPreparationStage("skill_discovery"); err != nil {
		return "", err
	}
	refreshSelectedMCPContracts := func(names []string) bool {
		if !runtimeSelectionContainsMCPConnectorSkill(names) {
			return false
		}
		refreshed := s.agentRuntimeWorkspaceMCPToolSchemas(
			ctx, options.SessionID, nil, runtimeToolUniverse,
		)
		if len(refreshed.Schemas) == 0 {
			return false
		}
		runtimeToolUniverse = append(runtimeToolUniverse, refreshed.Schemas...)
		return true
	}
	explicitlySelectedSkills, err := s.runtimeSkillsByNameWithConnectorSchemas(
		options.SelectedSkillNames, options.ExcludedSkillNames, runtimeToolUniverse,
	)
	if err != nil && errors.Is(err, errSelectedSkillContractUnavailable) &&
		refreshSelectedMCPContracts(options.SelectedSkillNames) {
		explicitlySelectedSkills, err = s.runtimeSkillsByNameWithConnectorSchemas(
			options.SelectedSkillNames, options.ExcludedSkillNames, runtimeToolUniverse,
		)
	}
	if err != nil {
		return "", err
	}
	if run != nil {
		explicitlySelectedSkills = runtimeSkillsForSelectedImplementation(
			explicitlySelectedSkills, run.selectedImplementationsSnapshot(), run.TaskIntent,
		)
	}
	selectedSkillNames := append([]string(nil), options.SelectedSkillNames...)
	if run != nil {
		selectedSkillNames = append(selectedSkillNames, run.executedSkillNamesSnapshot()...)
	}
	selectedSkills, err := s.runtimeSkillsByNameWithConnectorSchemas(
		uniqueSortedFolded(selectedSkillNames), options.ExcludedSkillNames, runtimeToolUniverse,
	)
	if err != nil && errors.Is(err, errSelectedSkillContractUnavailable) &&
		refreshSelectedMCPContracts(selectedSkillNames) {
		// A source restart can race the bundled connector catalog warmup. The
		// durable transcript still proves which connector Skills were loaded, so
		// perform one bounded live refresh before declaring their contract gone.
		// This is the same current-schema gate used for normal execution; it never
		// revives a stale body from transcript text or grants a missing connector.
		selectedSkills, err = s.runtimeSkillsByNameWithConnectorSchemas(
			uniqueSortedFolded(selectedSkillNames), options.ExcludedSkillNames, runtimeToolUniverse,
		)
	}
	if err != nil {
		return "", err
	}
	if run != nil {
		selectedSkills = runtimeSkillsForSelectedImplementation(
			selectedSkills, run.selectedImplementationsSnapshot(), run.TaskIntent,
		)
	}
	runtimeToolSchemas := filterAgentRuntimeToolSchemas(runtimeToolUniverse, options.AllowedTools)
	runtimeToolSchemas = enforceGovernedScientificExecutionAuthority(runtimeToolSchemas)
	explicitSkillClosure, err := s.expandRuntimeSkillDependencies(
		explicitlySelectedSkills,
		options.ExcludedSkillNames,
		runtimeSkillAllowedNamesWithConnectorDependencies(options.AllowedSkillNames, explicitlySelectedSkills),
		options.RestrictSkillDiscovery,
		agentRuntimeToolSchemaNameSet(runtimeToolSchemas),
	)
	if err != nil {
		return "", err
	}
	if run != nil {
		explicitSkillClosure = runtimeSkillsForSelectedImplementation(
			explicitSkillClosure, run.selectedImplementationsSnapshot(), run.TaskIntent,
		)
	}
	explicitScientificCapabilities := requiredScientificCapabilities(explicitSkillClosure)
	planModeEnabled, planApproved, err := s.restoreSessionRunnerPlanControl(session, run)
	if err != nil {
		return "", err
	}
	// Presentation follows plan state; execution authority remains the exact
	// permission-filtered snapshot. A plan created during this run can expose
	// its already-authorized progress tool on the next model round.
	runtimeToolAuthority := append([]agentruntime.ToolSchema(nil), runtimeToolSchemas...)
	if run != nil {
		run.setToolCapabilityCatalog(runtimeToolAuthority)
		run.restoreToolCapabilitiesFromEntries(currentLogicalEntries)
		run.restoreToolCapabilitiesFromContinuity(toolContinuity)
	}
	runtimeToolSchemas = filterSessionRunnerPlanningToolsForState(runtimeToolSchemas, planModeEnabled, planApproved)
	if !options.DisableSkillDiscovery &&
		agentRuntimeToolSchemaNamed(runtimeToolSchemas, "search_skills") &&
		agentRuntimeToolSchemaNamed(runtimeToolSchemas, "skill") {
		discovery := s.runtimeSkillDiscovery(
			taskIntent,
			explicitlySelectedSkills,
			options.ExcludedSkillNames,
			options.AllowedSkillNames,
			options.RestrictSkillDiscovery,
			agentRuntimeToolSchemaNameSet(runtimeToolSchemas),
		)
		if run != nil {
			discovery.autoReference = append(
				discovery.autoReference,
				s.runtimeImplementationAutoReferenceSkills(
					run.selectedImplementationsSnapshot(), selectedSkills,
					options.ExcludedSkillNames, options.AllowedSkillNames,
					options.RestrictSkillDiscovery, agentRuntimeToolSchemaNameSet(runtimeToolSchemas),
				)...,
			)
		}
		candidateContext := s.runtimeSkillCandidateContext(
			taskIntent,
			explicitlySelectedSkills,
			options.ExcludedSkillNames,
			options.AllowedSkillNames,
			options.RestrictSkillDiscovery,
			agentRuntimeToolSchemaNameSet(runtimeToolSchemas),
		)
		messages = appendRuntimeSkillCandidateContextMessage(messages, candidateContext)
		selectedNames := make(map[string]struct{}, len(selectedSkills)+len(discovery.autoReference))
		for _, skill := range selectedSkills {
			selectedNames[strings.ToLower(strings.TrimSpace(skill.Name))] = struct{}{}
		}
		for _, skill := range discovery.autoReference {
			name := strings.ToLower(strings.TrimSpace(skill.Name))
			if name == "" {
				continue
			}
			if _, exists := selectedNames[name]; exists {
				continue
			}
			selectedNames[name] = struct{}{}
			selectedSkills = append(selectedSkills, skill)
		}
	}
	selectedSkills, err = s.expandRuntimeSkillDependencies(
		selectedSkills,
		options.ExcludedSkillNames,
		runtimeSkillAllowedNamesWithConnectorDependencies(options.AllowedSkillNames, selectedSkills),
		options.RestrictSkillDiscovery,
		agentRuntimeToolSchemaNameSet(runtimeToolSchemas),
	)
	if err != nil {
		return "", err
	}
	if run != nil {
		selectedSkills = runtimeSkillsForSelectedImplementation(
			selectedSkills, run.selectedImplementationsSnapshot(), run.TaskIntent,
		)
	}
	planModePending, err := s.sessionRunnerPlanModePending(session, run)
	if err != nil {
		return "", err
	}
	if agentRuntimeToolSchemaNamed(runtimeToolSchemas, "ask_user") {
		messages = appendRuntimeAgentPolicyContextMessage(messages, sessionRunnerAskUserGuidance())
	}
	if agentRuntimeToolSchemaNamed(runtimeToolSchemas, generatePlanToolName) {
		messages = appendRuntimeAgentPolicyContextMessage(messages, sessionRunnerPlanningGuidance(planModeEnabled))
	}
	if planModePending {
		messages = appendRuntimeAgentPolicyContextMessage(messages, sessionRunnerPlanModeRules())
	}
	runtimeUniverseMCPCount := 0
	runtimeFilteredMCPCount := 0
	for _, schema := range runtimeToolUniverse {
		if strings.HasPrefix(strings.TrimSpace(schema.Name), "mcp__") {
			runtimeUniverseMCPCount++
		}
	}
	for _, schema := range runtimeToolSchemas {
		if strings.HasPrefix(strings.TrimSpace(schema.Name), "mcp__") {
			runtimeFilteredMCPCount++
		}
	}
	if runtimeUniverseMCPCount > 0 && runtimeFilteredMCPCount == 0 {
		log.Printf("workspace MCP tool snapshot filtered session=%q universe=%d universe_mcp=%d configured_allowed=%d profile_allowed=%d filtered=%d",
			strings.TrimSpace(options.SessionID), len(runtimeToolUniverse), runtimeUniverseMCPCount,
			configuredAllowedToolCount, len(options.AllowedTools), len(runtimeToolSchemas))
	}
	selectedSkills = s.agentRuntimeDiscoverableSkills(selectedSkills, agentRuntimeToolSchemaNameSet(runtimeToolSchemas))
	runtimeSkillSessionID := strings.TrimSpace(session.ID)
	if run != nil && strings.TrimSpace(run.SessionID) != "" {
		runtimeSkillSessionID = strings.TrimSpace(run.SessionID)
	}
	selectedSkills, err = s.prepareAgentSkillRuntimeContexts(ctx, runtimeSkillSessionID, selectedSkills)
	if err != nil {
		return "", fmt.Errorf("prepare selected skill runtime context: %w", err)
	}
	if executionPriority := runtimeSkillExecutionPriorityContext(selectedSkills); executionPriority != "" {
		messages = appendRuntimeAgentPolicyContextMessageWithSource(messages, executionPriority, agentruntime.ContextUsageSkills)
	}
	// Candidate discovery is prompt guidance, not evidence that a scientific
	// operation was requested or executed. Bind a capability before provider
	// execution only for explicit skill selections; trusted compute admissions
	// are added at completion from durable receipts.
	requiredScientificCapabilities := explicitScientificCapabilities
	if run != nil {
		run.addRequiredScientificCapabilities(requiredScientificCapabilities...)
	}
	// Candidate Skills are prompt context only. A mandatory review policy
	// may be attached before execution only from the root profile, an explicit
	// user selection, or already-durable executed Skill/capability evidence.
	// Newly executed Skills are incorporated by the completion-time refresh.
	if err := checkpointPreparationStage("review_policy"); err != nil {
		return "", err
	}
	session, err = s.resolveSessionRunnerReviewPolicyForRun(session, options, entries, explicitlySelectedSkills, run)
	if err != nil {
		return "", fmt.Errorf("resolve mandatory review policy: %w", err)
	}
	// Keep one authoritative Skill body at system priority for every provider.
	// The skill tool result remains a short durable receipt, while the current
	// catalog body is materialized exactly once below. This avoids competing
	// copies and prevents weaker OpenAI-compatible providers from treating the
	// scientific contract as optional low-priority tool data.
	messages = compactProviderVisibleSkillResultBodies(messages)
	skillContext, err := s.runtimeSkillContextFromSkills(selectedSkills)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(skillContext) != "" {
		messages = appendRuntimeSkillContextMessage(messages, skillContext)
	}
	if err := checkpointPreparationStage("memory_context"); err != nil {
		return "", err
	}
	workspaceMemoryContext, err := s.runnerTaskMemoryContext(
		ctx, session, messages, run, taskIntentID, taskIntentRevision,
	)
	if err != nil {
		return "", fmt.Errorf("resolve runner task memory: %w", err)
	}
	if workspaceMemoryContext != "" {
		messages = appendRuntimeWorkspaceMemoryContextMessage(messages, workspaceMemoryContext)
	}
	mcpContext := runtimeMCPContext(runtimeToolSchemas, selectedSkills)
	if strings.TrimSpace(mcpContext) != "" {
		messages = appendRuntimeMCPContextMessage(messages, mcpContext)
	}
	providerCacheMarkers := runtimeProviderCacheMarkersFromEntries(entries)
	providerCacheContext := providerCacheMarkers.Context()
	if strings.TrimSpace(providerCacheContext) != "" {
		messages = appendRuntimeProviderCacheContextMessage(messages, providerCacheContext)
	}
	if err := checkpointPreparationStage("task_contract"); err != nil {
		return "", err
	}
	taskContract := buildSessionRunnerTaskContract(taskIntent, taskIntentID, taskIntentRevision)
	if correction, found := latestRunnerCorrection(entries); found {
		staleAdvisory := sessionRunnerRecoveredCorrectionIsAdvisory(correction.ReasonCode, correction.Detail)
		if staleAdvisory {
			// This correction was emitted before the explicit review policy was
			// resolved. Drop only the synthetic correction context; replayed user,
			// assistant, tool, and artifact receipts remain authoritative.
			messages = sessionRunnerRemoveRecoveredCorrectionContext(messages)
		} else {
			taskContract = applyRecoveredRunnerCorrectionToTaskContract(taskContract, correction)
		}
		if run != nil {
			if staleAdvisory {
				run.CorrectionReason = ""
				run.CorrectionDetail = ""
			} else {
				run.CorrectionReason = correction.ReasonCode
				run.CorrectionDetail = correction.Detail
			}
		}
	} else if run != nil {
		run.CorrectionReason = ""
		run.CorrectionDetail = ""
	}
	if run != nil {
		state, found := sessionRunnerNoProgressRecoveryFromReplay(entries)
		if !found {
			state = newSessionRunnerNoProgressRecovery(
				runnerRecoveryObligationFingerprint(run.Transcript, run.CorrectionReason, run.CorrectionDetail),
			)
		}
		run.restoreNoProgressRecovery(state)
		if recoveryContext := sessionRunnerNoProgressRecoveryContext(*run.NoProgressRecovery); recoveryContext != "" {
			messages = appendRuntimeTerminalPolicyContextMessage(messages, recoveryContext)
		}
	}
	if desiredOutputs := s.generatedPlanDesiredOutputsContext(intakeFrameID); desiredOutputs != "" {
		messages = attachRuntimeResearchNavigationState(messages, desiredOutputs)
	}
	advertisedRuntimeToolSchemas := partitionAgentRuntimeToolSchemasForModelWithSkills(runtimeToolSchemas, selectedSkills)
	if run != nil {
		advertisedRuntimeToolSchemas = sessionRunnerCorrectionToolSchemas(
			advertisedRuntimeToolSchemas, run.CorrectionReason, run.CorrectionDetail,
		)
	}
	if err := checkpointPreparationStage("prompt_snapshot"); err != nil {
		return "", err
	}
	if err := s.persistSessionRunnerPromptSnapshot(
		ctx, session, options, advertisedRuntimeToolSchemas, selectedSkills, run, researchModelContext,
	); err != nil {
		return "", fmt.Errorf("persist runner prompt snapshot: %w", err)
	}
	skillDecisionConstraints := runtimeSkillCriticalConstraintsContext(selectedSkills)
	if skillDecisionConstraints != "" {
		messages = appendRuntimeTerminalPolicyContextMessageWithSource(messages, skillDecisionConstraints, agentruntime.ContextUsageSkills)
	}
	if implementationAuthority := selectedImplementationAuthorityContext(run); implementationAuthority != "" {
		messages = appendRuntimeTerminalPolicyContextMessage(messages, implementationAuthority)
	}
	if temporalGrounding := sessionRunnerTemporalGroundingContext(trustedRuntimeNow); temporalGrounding != "" {
		messages = appendRuntimeTerminalPolicyContextMessage(messages, temporalGrounding)
	}
	// Keep the collaboration contract adjacent to the active conversation. It
	// must remain one authoritative system instruction; placing it before Skill,
	// execution and recovery context lets weaker providers lose it among later
	// policy messages and fall into tool-only monologues.
	messages = appendSessionRunnerPublicCommunicationContract(messages)
	// Keep the user-language contract as the last system instruction before
	// provider execution. Skills and source context are often
	// English; placing the localized contract earlier lets a coding model copy
	// that internal language during long correction runs.
	messages = appendRuntimeResponseLanguageContextMessage(messages, taskLanguage)
	messages = moveRecoveredRunnerCorrectionContextToEnd(messages)
	if err := checkpointPreparationStage("model_execution"); err != nil {
		return "", err
	}
	preparationActive = false
	cancelPreparation()
	ctx = parentCtx
	deltaContext, cancelDelta := context.WithCancelCause(ctx)
	defer cancelDelta(nil)
	// Keep the source-evidence collector on the exact context shared by the
	// runtime gateway and its event checkpoint callback. A collector attached
	// only to a recovery subpath cannot attest ordinary direct MCP calls, and a
	// successful authoritative lookup would then be indistinguishable from
	// untrusted model prose after restart or completion validation.
	evidenceContext := withWorkspaceMCPSourceEvidenceCollector(deltaContext)
	// Construct the gateway with the task run already attached. The runtime can
	// later derive an approval execution context, but scientific Skill state must
	// remain bound to this exact logical task across that boundary.
	engine := s.newAgentRuntimeEngineWithContext(
		withTranscriptRunnerChatRun(evidenceContext, run), options, runtimeToolAuthority,
	)
	initialModel := sessionRunnerResolvedModelClient{}
	initialReady := false
	if options.ModelProfile != nil && engine.Model != nil {
		initialModel = sessionRunnerResolvedModelClient{
			client: newSessionOutputBudgetClient(
				engine.Model, s.runtimeStore, *options.ModelProfile, session.ID, "agent",
			),
			identity: strings.Join([]string{
				strings.TrimSpace(options.ModelProfile.Provider.ID),
				strings.TrimSpace(options.ModelProfile.Provider.Endpoint),
				strings.TrimSpace(options.ModelProfile.Model),
			}, "\x00"),
			model:             strings.TrimSpace(options.ModelProfile.Model),
			selection:         options.modelSelection,
			selectionRevision: options.modelSelectionRevision,
		}
		initialReady = true
	}
	engine.Model = &sessionRunnerDynamicModelClient{
		server: s, sessionID: session.ID, session: session, fallback: engine.Model,
		fallbackModel: options.Model, role: "agent", audit: options.ModelAudit,
		contextUsage: newSessionContextUsageRecorder(s, session.ID, sessionRunnerAttempt(run), options),
		initial:      initialModel, initialReady: initialReady,
		initialSelection: options.modelSelection, initialRevision: options.modelSelectionRevision,
		resolutionInput: providers.ResolutionInput{
			Context:   ctx,
			ProjectID: sessionRunnerProjectID(session), RequestTimeout: options.RequestTimeout,
			MaxAttempts: options.MaxAttempts, MaxResponseBytes: options.ModelResponseLimitBytes,
		},
	}
	if run != nil && run.ProviderContinuation != nil && run.ProviderContinuation.Content.Len() > 0 {
		engine.Model = &sessionRunnerContinuationModelClient{
			delegate:      engine.Model,
			prefix:        run.ProviderContinuation.Content.String(),
			privatePrefix: run.ProviderContinuation.PrivateCandidate.String(),
		}
	}
	if s.memoryExtraction != nil {
		engine.Model = &memoryExtractionRecordingModelClient{
			delegate: engine.Model, runtime: s.memoryExtraction, sessionID: session.ID,
		}
	}
	deltaBatcher := newSessionRunnerContentDeltaBatcher(
		sessionRunnerContentDeltaFlushInterval,
		sessionRunnerContentDeltaFlushBytes,
		func(persistCtx context.Context, delta string, index int) error {
			if err := s.checkpointChatContentDelta(persistCtx, options, run, delta, index); err != nil {
				return classifySessionRunnerContentDeltaPersistenceError(index, err)
			}
			return nil
		},
	)
	candidateStream := &sessionRunnerCandidateStreamBuffer{}
	privatePrefix := ""
	if run != nil && run.ProviderContinuation != nil {
		privatePrefix = run.ProviderContinuation.PrivateCandidate.String()
		candidateStream.append(privatePrefix)
	}
	publicProgressStream := &sessionRunnerCandidateStreamBuffer{}
	publicProgressBlockID := ""
	progressDeduper := &sessionRunnerPublicProgressDeduper{}
	progressSegmentPublished := false
	publicNarrationBytes := 0
	communicationSchedule, scheduleErr := s.loadSessionRunnerCommunicationSchedule(deltaContext, run)
	if scheduleErr != nil {
		return "", fmt.Errorf("load communication cadence: %w", scheduleErr)
	}
	var communicationObserver *sessionRunnerCommunicationObserver
	stopDeltaTimer := deltaBatcher.startTimer(
		deltaContext,
		sessionRunnerContentDeltaPersistenceTimeout,
		func(err error) { cancelDelta(fmt.Errorf("persist model content delta: %w", err)) },
	)
	defer stopDeltaTimer()
	flushDeltaBoundary := func() error {
		operationCtx, stop := context.WithTimeout(deltaContext, sessionRunnerContentDeltaPersistenceTimeout)
		defer stop()
		return deltaBatcher.boundary(operationCtx)
	}
	publishProgress := func(blockID, delta string) error {
		delta = sessionRunnerPublicProgressNarration(delta)
		if delta == "" {
			return nil
		}
		shouldPublish, err := progressDeduper.shouldPublish(blockID, delta)
		if err != nil {
			return err
		}
		if !shouldPublish {
			return nil
		}
		operationCtx, stop := context.WithTimeout(deltaContext, sessionRunnerContentDeltaPersistenceTimeout)
		defer stop()
		if err := deltaBatcher.append(operationCtx, delta); err != nil {
			return err
		}
		if err := deltaBatcher.boundary(operationCtx); err != nil {
			return err
		}
		progressSegmentPublished = true
		publicNarrationBytes += len(delta)
		return communicationSchedule.published()
	}
	publishProgressSegment := func() error {
		privatePrefix = ""
		return publishProgress("", candidateStream.take())
	}
	publishTypedProgressSegment := func(blockID string) error {
		return publishProgress(blockID, publicProgressStream.take())
	}
	engine.OnModelDelta = func(event agentruntime.ModelStreamEvent) error {
		if run == nil {
			return nil
		}
		switch event.Kind {
		case agentruntime.ModelStreamEventPublicProgressDelta:
			if publicProgressBlockID != "" && publicProgressBlockID != event.BlockID {
				return errors.New("public progress block changed before its boundary")
			}
			publicProgressBlockID = event.BlockID
			publicProgressStream.append(event.ContentDelta)
			return nil
		case agentruntime.ModelStreamEventPublicProgressBoundary:
			if publicProgressBlockID == "" || publicProgressBlockID != event.BlockID {
				return errors.New("public progress boundary does not match an open block")
			}
			blockID := publicProgressBlockID
			publicProgressBlockID = ""
			return publishTypedProgressSegment(blockID)
		case agentruntime.ModelStreamEventToolCallBoundary:
			if publicProgressBlockID != "" {
				return errors.New("tool boundary arrived before the public progress block closed")
			}
			if candidateStream.hasContent() {
				return publishProgressSegment()
			}
			return nil
		case agentruntime.ModelStreamEventContentDelta:
			if event.ContentDelta != "" {
				candidateStream.append(event.ContentDelta)
			}
			return nil
		default:
			return nil
		}
	}
	engine.OnEventError = func(event agentruntime.Event) error {
		if run == nil {
			return nil
		}
		if err := advanceSessionRunnerEventPhase(run, event); err != nil {
			return err
		}
		switch event.Type {
		case agentruntime.EventModelResponse, agentruntime.EventToolStarted, agentruntime.EventToolCompleted,
			agentruntime.EventToolFailed, agentruntime.EventToolPaused, agentruntime.EventFinal:
			if err := flushDeltaBoundary(); err != nil {
				return err
			}
		}
		switch event.Type {
		case agentruntime.EventModelResponse:
			defer communicationObserver.recordBoundary(len(event.ToolCalls) > 0)
			if len(event.ToolCalls) > 0 {
				// Engine may compact a noncompliant tool-round narration only after
				// the provider boundary reveals that this is progress rather than a
				// final answer. Replace the still-private streamed candidate with the
				// canonical event text before its first durable publication, so the UI
				// never displays and retracts the provider draft.
				message, calls := s.sanitizeManagedAskUserModelTurnForCheckpoint(
					run, event.Message, event.ToolCalls,
				)
				if progressSegmentPublished {
					// A provider-native tool boundary already proved that the
					// preceding text was progress and published it. Only a rare
					// trailing fragment remains in the private buffer.
					if candidateStream.hasContent() {
						if err := publishProgressSegment(); err != nil {
							return err
						}
					}
				} else {
					candidateStream.replace(message)
					if err := publishProgressSegment(); err != nil {
						return err
					}
				}
				progressSegmentPublished = false
				if err := s.checkpointChatModelToolCalls(options, run, calls); err != nil {
					return err
				}
				run.beginNextAssistantSegment()
				progressDeduper.reset()
				return nil
			}
			candidateStream.seal()
		case agentruntime.EventToolStarted, agentruntime.EventToolProgress, agentruntime.EventToolCompleted,
			agentruntime.EventToolFailed, agentruntime.EventToolPaused:
			if event.Type == agentruntime.EventToolStarted && candidateStream.hasContent() {
				// Some providers emit tool calls only on the tool-start boundary,
				// not on EventModelResponse. That boundary proves the preceding
				// text is progress narration rather than a final candidate.
				if err := publishProgressSegment(); err != nil {
					return err
				}
				run.beginNextAssistantSegment()
				progressDeduper.reset()
			}
			if err := s.checkpointSessionRunnerToolEvent(evidenceContext, options, run, event); err != nil {
				if sessionRunnerToolProgressPersistenceBestEffort(event, err) == nil {
					return nil
				}
				var pendingRecovery *kernelLocalOperationPendingRecoveryError
				if errors.As(err, &pendingRecovery) {
					return err
				}
				return sessionRunnerToolLifecyclePersistenceInterruption{
					eventType: event.Type, toolCallID: event.ToolCallID, cause: err,
				}
			}
			if event.Type == agentruntime.EventToolCompleted || event.Type == agentruntime.EventToolFailed {
				if err := communicationSchedule.settled(); err != nil {
					return fmt.Errorf("persist communication cadence: %w", err)
				}
			}
			return nil
		}
		return nil
	}
	runRequest := agentruntime.RunRequest{
		Messages:                          agentRuntimeMessagesFromChat(messages),
		Tools:                             advertisedRuntimeToolSchemas,
		MaxToolRounds:                     options.MaxToolRounds,
		MaxToolCallsPerRound:              options.MaxToolCallsPerRound,
		MaxConsecutiveIdenticalToolRounds: options.MaxConsecutiveIdenticalToolRounds,
		Metadata:                          providerCacheMarkers.Metadata(),
		Headers:                           providerCacheMarkers.Headers(),
	}
	// A recoverable artifact-save failure is part of the current durable
	// conversation even when it has not yet been converted into a resumable
	// correction checkpoint. Let the existing provider-neutral repair selector
	// constrain the very next model round so a weak provider cannot keep
	// repeating an invalid edit/save pair. For ordinary turns this selector is
	// a no-op and the provider retains full tool choice.
	planPriorityChoice := any(nil)
	if gateway, ok := engine.Tools.(serverAgentRuntimeToolGateway); ok {
		planPriorityChoice = gateway.generatedPlanPriorityToolChoice(runRequest.Messages, advertisedRuntimeToolSchemas)
	}
	if planPriorityChoice != nil {
		// A terminal source event already exists. Reconcile that immutable fact
		// or continue its source-owned frontier before older artifact/Skill
		// selectors can start another model action.
		runRequest.InitialToolChoice = planPriorityChoice
	} else if choice := sessionRunnerCorrectionRequiredToolChoice(run, runRequest.Messages, advertisedRuntimeToolSchemas); choice != nil {
		runRequest.InitialToolChoice = choice
	} else if initialToolChoice := recoveredRunnerInitialToolChoice(entries, advertisedRuntimeToolSchemas); initialToolChoice != nil {
		// A durable correction that explicitly requires new evidence must begin
		// with a real model-selected tool call. The engine owns bounded private
		// protocol repair; if the provider still returns prose, the outer runner
		// records one resumable interruption instead of streaming and resetting
		// the same unsupported candidate indefinitely.
		runRequest.InitialToolChoice = initialToolChoice
	} else if statefulToolChoice := sessionRunnerStatefulInitialToolChoice(
		engine, runRequest.Messages, advertisedRuntimeToolSchemas,
	); statefulToolChoice != nil {
		// Execution-unit rotation is not a research-state reset. Apply the same
		// gateway-owned Skill, MCP and plan transition policy on the first model
		// request after prior tools have settled; brand-new user turns still start
		// unconstrained because they have no settled tool boundary.
		runRequest.InitialToolChoice = statefulToolChoice
	}
	communicationObserver = &sessionRunnerCommunicationObserver{
		delegate:         engine.Model,
		publicationBytes: func() int { return publicNarrationBytes },
		audit:            func(record map[string]any) { s.recordSessionRunnerCommunicationAudit(run, record) },
	}
	engine.Model = &sessionRunnerResponseLanguageModelClient{
		language: taskLanguage,
		audit:    func(record map[string]any) { s.recordSessionRunnerCommunicationAudit(run, record) },
		delegate: &sessionRunnerResponseContractClient{
			delegate: communicationObserver, progressDue: communicationSchedule.due, progressAllowed: communicationSchedule.allowed,
		},
	}
	engine.AllowToolPreamble = func(text string) bool {
		return !progressSegmentPublished && strings.TrimSpace(text) != "" && sessionRunnerPublicProgressNarration(text) == strings.TrimSpace(text)
	}
	result, err := s.runVerifiedSessionAgent(
		withTranscriptRunnerChatRun(evidenceContext, run), session, options, engine, runRequest, taskContract, run,
		func(denial int, correction string) error {
			candidateStream.discard()
			privatePrefix = ""
			if err := flushDeltaBoundary(); err != nil {
				return err
			}
			if err := s.checkpointSessionRunnerPlanModeDenial(
				context.WithoutCancel(evidenceContext), options, run, denial, correction,
			); err != nil {
				return err
			}
			return resumeSessionRunnerProviderAfterCompletionGate(run)
		},
	)
	if err != nil && !candidateStream.isSealed() {
		// A transport interruption does not prove a public progress boundary.
		// Persist only new candidate bytes, preserving the exact private prefix
		// for durable continuation and subsequent completion validation.
		candidate := strings.TrimPrefix(candidateStream.take(), privatePrefix)
		persistCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), sessionRunnerContentDeltaPersistenceTimeout)
		persistErr := s.checkpointPrivateProviderCandidate(persistCtx, run, candidate)
		stop()
		if persistErr != nil {
			return "", persistErr
		}
	} else if err != nil {
		candidateStream.discard()
	}
	if timerErr := stopDeltaTimer(); timerErr != nil {
		return "", fmt.Errorf("persist model content delta: %w", timerErr)
	}
	finalFlushContext, stopFinalFlush := context.WithTimeout(
		context.WithoutCancel(ctx), sessionRunnerContentDeltaPersistenceTimeout,
	)
	flushErr := deltaBatcher.flush(finalFlushContext, true)
	stopFinalFlush()
	if flushErr != nil {
		return "", flushErr
	}
	if err != nil {
		var toolRoundLimit *agentruntime.ToolRoundLimitError
		var toolBatchLimit *agentruntime.ToolCallBatchLimitError
		var toolBatchValidation *agentruntime.ToolCallBatchValidationError
		var largeToolResultFailure *agentruntime.LargeToolResultInfrastructureError
		if errors.As(err, &largeToolResultFailure) {
			log.Printf(
				"runner large tool result failure session=%q tool_call=%q tool=%q code=%q",
				session.ID, largeToolResultFailure.ToolCallID, largeToolResultFailure.ToolName,
				largeToolResultFailure.ReasonCode(),
			)
		}
		if errors.As(err, &toolRoundLimit) {
			// Preserve the typed limit so the outer runner can persist a
			// resumable interruption for this frame instead of turning a
			// bounded safety stop into a terminal process failure.
			return "", err
		}
		if errors.As(err, &toolBatchLimit) || errors.As(err, &toolBatchValidation) {
			return "", errors.New(strings.NewReplacer("agent runtime", "runner chat").Replace(err.Error()))
		}
		return "", err
	}
	finalContent := result.FinalMessage.Content
	if strings.TrimSpace(finalContent) == "" {
		return "", &sessionRunnerEmptyFinalCandidateError{
			Detail: "the model completed its tool rounds without producing a user-visible final answer",
		}
	}
	if remaining, err := s.incompleteGeneratedPlanStepTitles(intakeFrameID); err != nil {
		return "", err
	} else if len(remaining) > 0 {
		return "", sessionRunnerPlanStepsIncomplete{steps: remaining}
	}
	// The candidate has passed every structural, task-contract, and optional
	// review gate; evidence-quality advisories remain attached to their durable
	// receipts without reopening execution. Keep it private until terminal
	// settlement has also completed runtime
	// cleanup. The settlement path then appends the authoritative assistant
	// message immediately before runner_finished; no final-shaped content_delta
	// can precede a later failure or correction.
	candidateStream.discard()
	if err := markSessionRunnerComplete(run); err != nil {
		return "", err
	}
	return finalContent, nil
}

// allowSelectedEvidenceResolverSkills carries an already validated, user-owned
// auxiliary resolver decision into the exact task Skill policy. It expands a
// restricted allow-list by only the registry-bound resolver Skill; it does not
// select the Skill, bypass exclusions, or open general discovery.
func allowSelectedEvidenceResolverSkills(
	options SessionRunnerChatOptions,
	run *sessionRunnerChatRun,
) SessionRunnerChatOptions {
	if run == nil || !options.RestrictSkillDiscovery {
		return options
	}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		options.AllowedSkillNames = appendUniqueFolded(options.AllowedSkillNames, resolver.Skill)
	}
	return options
}

func sessionRunnerStatefulInitialToolChoice(
	engine agentruntime.Engine,
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) any {
	policy, ok := engine.Tools.(agentruntime.ModelToolChoicePolicy)
	if !ok {
		return nil
	}
	if gateway, gatewayOK := engine.Tools.(serverAgentRuntimeToolGateway); gatewayOK && gateway.server != nil {
		if _, _, activePlanStep := gateway.server.generatedPlanActiveStep(gateway.sessionID); activePlanStep {
			// Durable plan state survives history compaction. It is sufficient
			// authority to constrain the first round even when no settled tool
			// message remains in the compact provider replay.
			return policy.RequiredToolChoice(messages, tools)
		}
	}
	if !sessionRunnerMessagesHaveSettledTool(messages) {
		return nil
	}
	return policy.RequiredToolChoice(messages, tools)
}

func sessionRunnerMessagesHaveSettledTool(messages []agentruntime.Message) bool {
	calls := runnerToolCallsByCallID(messages)
	for _, message := range messages {
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if _, found := calls[message.ToolCallID]; found {
			return true
		}
	}
	return false
}

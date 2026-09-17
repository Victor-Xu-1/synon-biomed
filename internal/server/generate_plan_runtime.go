package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	generatePlanToolName           = "generate_plan"
	generatePlanSchemaVersion      = 3
	maxGeneratedPlanBytes          = 128 << 10
	maxGeneratedPlanPhases         = 16
	maxGeneratedDelegations        = 64
	maxGeneratedPlanSteps          = 256
	generatedPlanStepKindWork      = "work"
	generatedPlanStepKindResearch  = "research"
	generatedPlanStepKindSynthesis = "synthesis"
	generatedPlanStepKindDelivery  = "delivery"
)

type generatedPlanDocument struct {
	Version        int                      `json:"version"`
	TaskSummary    string                   `json:"task_summary"`
	Phases         []generatedPlanPhase     `json:"phases"`
	DesiredOutputs []string                 `json:"desired_outputs,omitempty"`
	Feasibility    generatedPlanFeasibility `json:"feasibility"`
}

type generatedPlanRequest struct {
	HumanDescription string                   `json:"human_description,omitempty"`
	TaskSummary      string                   `json:"task_summary,omitempty"`
	Phases           []generatedPlanPhase     `json:"phases,omitempty"`
	DesiredOutputs   []string                 `json:"desired_outputs,omitempty"`
	Feasibility      generatedPlanFeasibility `json:"feasibility,omitempty"`
	Approve          bool                     `json:"approve,omitempty"`
}

type generatedPlanPhase struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Delegations []generatedPlanDelegation `json:"delegations"`
}

type generatedPlanDelegation struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	AgentName string              `json:"agent_name,omitempty"`
	Steps     []generatedPlanStep `json:"steps"`
}

type generatedPlanStep struct {
	ID               string               `json:"id"`
	Title            string               `json:"title"`
	Description      string               `json:"description"`
	Kind             string               `json:"kind,omitempty"`
	OutputModule     string               `json:"output_module,omitempty"`
	ResearchQuestion string               `json:"research_question,omitempty"`
	ResearchDepth    string               `json:"research_depth,omitempty"`
	DiscoveryQueries []generatedPlanQuery `json:"discovery_queries,omitempty"`
}

type generatedPlanQuery struct {
	Language string `json:"language"`
	Query    string `json:"query"`
}

type generatedPlanFeasibility struct {
	Confidence string `json:"confidence"`
	Rationale  string `json:"rationale"`
}

func (s *Server) executeAgentGeneratePlan(
	ctx context.Context,
	sessionID, toolCallID string,
	input map[string]any,
) (any, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return nil, errors.New("generate_plan requires the durable Frame and Transcript stores")
	}
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	if sessionID == "" || toolCallID == "" {
		return nil, errors.New("generate_plan requires the active Frame and tool-call identities")
	}
	if err := s.validateRegisteredTool(generatePlanToolName, input); err != nil {
		return nil, err
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil {
		return nil, errors.New("generate_plan requires the active Transcript runner authority")
	}
	stream, claim := run.Transcript.Stream, run.Transcript.Claim
	if sessionID != stream.SessionID || sessionID != stream.FrameID || claim.StreamUID != stream.UID {
		return nil, errors.New("generate_plan target does not match the active Transcript runner authority")
	}
	sourceEventID := run.ToolSourceEventIDs[toolCallID]
	if sourceEventID <= 0 {
		return nil, errors.New("generate_plan durable tool-start evidence is unavailable")
	}
	if boolValue(input["approve"], false) {
		_, hasPhases := input["phases"]
		_, hasTrackContent := input["delegations"]
		if !run.AutonomousPlanning || (!hasPhases && !hasTrackContent) {
			return s.approveAgentGeneratedPlan(ctx, sessionID, toolCallID, input)
		}
		// Autonomous content is already permitted to become a working plan.
		// A redundant model flag must neither manufacture user approval nor
		// send that content into the unrelated explicit-consent operation.
		input = copyMapAny(input)
		delete(input, "approve")
	}
	document, normalized, encoded, err := normalizeGeneratedPlan(input)
	if err != nil {
		return nil, err
	}
	// Revisions and progress updates share one metadata authority. Validate the
	// live source even for an idempotent request, which may skip artifact I/O.
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	if err := s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		_, err := tx.ValidateLiveRunnerClaim(ctx, claim)
		return err
	}); err != nil {
		return nil, err
	}
	validated, err := s.transcriptStore.ValidateRunnerToolArtifactSource(ctx, claim, sourceEventID, toolCallID, generatePlanToolName)
	if err != nil || !sameLargeToolResultStream(validated, stream) {
		return nil, errors.New("generate_plan source authority is no longer active")
	}
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadataWithContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	existing := strings.TrimSpace(stringValue(metadata.ContextData["_plan_artifact_id"]))
	if existing != "" {
		if !run.AutonomousPlanning || !autonomousGeneratedPlan(metadata.ContextData) {
			return nil, errors.New("this task already has a reviewed plan; autonomous revision cannot change it")
		}
		if sourceEventID < int64(numberValue(metadata.ContextData["_plan_source_event_id"])) {
			return nil, errors.New("plan revision was superseded by a newer working plan")
		}
		var unchanged bool
		document, normalized, encoded, unchanged, err = reconcileGeneratedPlanRevision(mapValue(metadata.ContextData["_plan_json"]), document, toolCallID)
		if err != nil {
			return nil, err
		}
		if unchanged {
			metadata.ContextData["_plan_source_event_id"] = sourceEventID
			if _, err := s.workspaceStore.SetClaimedFrameRuntimeMetadata(ctx, claim, metadata); err != nil {
				return nil, err
			}
			return generatedWorkingPlanReceipt(document, metadata.ContextData, true), nil
		}
	}

	artifactDigest := sha256.Sum256([]byte("generate-plan:v3\x00" + stream.UID + "\x00" + toolCallID))
	artifactID := "plan-" + hex.EncodeToString(artifactDigest[:16])
	filename := "plan_" + hex.EncodeToString(artifactDigest[:8]) + ".json"
	if existing != "" {
		artifactID = existing
		filename = stringValue(metadata.ContextData["_plan_file_path"])
	}
	mutationID := "generate-plan-" + hex.EncodeToString(artifactDigest[:])
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(ctx, mutationID),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: filename,
			ContentType: "application/json", Content: bytes.NewReader(encoded), MaxBytes: maxGeneratedPlanBytes,
			CreatedBy: claim.RunnerID, RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: sourceEventID, Relation: "produced",
				ReuseCurrentVersionIfUnchanged: true,
			},
		},
		stream.OwnerID,
	)
	if err != nil {
		return nil, err
	}
	// The plan is durable runtime state, not a user deliverable. Keep it
	// available to the plan review API while excluding it from the project file
	// shelf and final scientific-deliverable validation.
	if err := s.workspaceStore.SetArtifactRetentionMode(
		ctx, artifact.ID, stream.ProjectID, stream.OwnerID, "working_data",
	); err != nil {
		return nil, fmt.Errorf("classify generated plan as internal runtime state: %w", err)
	}
	planDigest := sha256.Sum256(encoded)
	requestID := "plan-request:v3:" + hex.EncodeToString(planDigest[:16])
	responseLanguage := strings.ToLower(strings.TrimSpace(run.ResponseLanguage))
	if responseLanguage == "" {
		responseLanguage = sessionRunnerResponseLanguage(run.TaskIntent)
	}
	if run.AutonomousPlanning {
		contextData := copyMapAny(metadata.ContextData)
		// Event cursors are scoped to one immutable plan version. A revised plan
		// must not reconcile an unconsumed source event from the superseded
		// control graph into its first module.
		resetGeneratedPlanResearchSourceCursors(contextData)
		contextData["_plan_artifact_id"] = artifact.ID
		contextData["_plan_version_id"] = version.ID
		contextData["_plan_generated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		contextData["_plan_file_path"] = artifact.Name
		contextData["_plan_size_bytes"] = len(encoded)
		contextData["_plan_sha256"] = hex.EncodeToString(planDigest[:])
		contextData["_plan_json"] = copyMapAny(normalized)
		contextData["_plan_schema_version"] = generatePlanSchemaVersion
		contextData["_plan_tool_call_id"] = toolCallID
		contextData["_plan_request_id"] = requestID
		contextData["_plan_task_intent_id"] = strings.TrimSpace(run.TaskIntentID)
		contextData["_plan_task_intent_revision"] = run.TaskIntentRevision
		contextData["_plan_task_intent_sha256"] = generatedPlanTaskIntentSHA(run.TaskIntent)
		contextData["_plan_control_mode"] = "autonomous"
		contextData["_plan_execution_authorized"] = true
		contextData["_plan_source_event_id"] = sourceEventID
		if contextData["_step_statuses"] == nil {
			contextData["_step_statuses"] = map[string]any{}
		}
		for _, stale := range []string{
			"_plan_approved", "_plan_approved_at", "_plan_approval_id", "_plan_approval_fingerprint", "_plan_edited_sha256",
		} {
			delete(contextData, stale)
		}
		metadata.ContextData = contextData
		if _, err := s.workspaceStore.SetClaimedFrameRuntimeMetadata(ctx, claim, metadata); err != nil {
			return nil, fmt.Errorf("persist autonomous working plan: %w", err)
		}
		run.planProgressAvailable.Store(true)
		message := "Execution plan recorded."
		if responseLanguage == "zh" {
			message = "执行计划已记录。"
		}
		receipt := generatedWorkingPlanReceipt(document, contextData, false)
		receipt["message"] = message
		return receipt, nil
	}
	approvalMessage := "The plan is ready for review."
	if responseLanguage == "zh" {
		approvalMessage = "计划已准备好，请审阅。"
	}
	return nil, &agentruntime.PauseError{
		Status:  "awaiting_approval",
		Message: approvalMessage,
		Data: map[string]any{
			"approval_kind":        "plan",
			"frame_id":             sessionID,
			"tool_call_id":         toolCallID,
			"request_id":           requestID,
			"plan_artifact_id":     artifact.ID,
			"plan_version_id":      version.ID,
			"plan_filename":        artifact.Name,
			"plan_sha256":          hex.EncodeToString(planDigest[:]),
			"plan_size_bytes":      len(encoded),
			"plan_json":            normalized,
			"task_intent_id":       strings.TrimSpace(run.TaskIntentID),
			"task_intent_revision": run.TaskIntentRevision,
			"task_intent_sha256":   generatedPlanTaskIntentSHA(run.TaskIntent),
			"task_summary":         document.TaskSummary,
		},
	}
}

func normalizeGeneratedPlan(input map[string]any) (generatedPlanDocument, map[string]any, []byte, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) == 0 || len(raw) > maxGeneratedPlanBytes {
		return generatedPlanDocument{}, nil, nil, errors.New("generate_plan input exceeds the bounded v3 plan contract")
	}
	if _, legacyDocument := input["version"]; !legacyDocument {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var request generatedPlanRequest
		if err := decoder.Decode(&request); err != nil {
			return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan input does not match the Synon plan schema: %w", err)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return generatedPlanDocument{}, nil, nil, err
		}
		request.HumanDescription = strings.TrimSpace(request.HumanDescription)
		request.TaskSummary = strings.TrimSpace(request.TaskSummary)
		if request.Approve {
			return generatedPlanDocument{}, nil, nil, errors.New("generate_plan approve cannot be combined with plan normalization")
		}
		if request.HumanDescription != "" {
			if err := validateGeneratedPlanText("human_description", request.HumanDescription, 1, 256); err != nil {
				return generatedPlanDocument{}, nil, nil, err
			}
		}
		// human_description and task_summary are both natural-language summaries
		// of a content plan. Providers commonly populate only the presentation
		// field even when they supplied a complete nested plan. Normalize that
		// bounded equivalent at this one schema boundary instead of spending a
		// failed tool round on repeating the same text.
		if request.TaskSummary == "" && request.HumanDescription != "" && len(request.Phases) > 0 {
			request.TaskSummary = request.HumanDescription
		}
		if request.TaskSummary == "" || len(request.Phases) == 0 {
			return generatedPlanDocument{}, nil, nil, errors.New("generate_plan task_summary and phases are required")
		}
		return normalizeGeneratedPlan(map[string]any{
			"version": generatePlanSchemaVersion, "task_summary": request.TaskSummary,
			"phases": request.Phases, "desired_outputs": request.DesiredOutputs,
			"feasibility": request.Feasibility,
		})
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document generatedPlanDocument
	if err := decoder.Decode(&document); err != nil {
		return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan input does not match the Synon plan artifact schema: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return generatedPlanDocument{}, nil, nil, err
	}
	if document.Version != generatePlanSchemaVersion {
		return generatedPlanDocument{}, nil, nil, errors.New("generate_plan.version must be 3")
	}
	document.TaskSummary = strings.TrimSpace(document.TaskSummary)
	if err := validateGeneratedPlanText("task_summary", document.TaskSummary, 1, 4096); err != nil {
		return generatedPlanDocument{}, nil, nil, err
	}
	if len(document.DesiredOutputs) > 256 {
		return generatedPlanDocument{}, nil, nil, errors.New("generate_plan.desired_outputs exceeds 256 items")
	}
	for index := range document.DesiredOutputs {
		document.DesiredOutputs[index] = strings.TrimSpace(document.DesiredOutputs[index])
		if err := validateGeneratedPlanText("desired_outputs", document.DesiredOutputs[index], 1, 1024); err != nil {
			return generatedPlanDocument{}, nil, nil, err
		}
	}
	if len(document.Phases) == 0 || len(document.Phases) > maxGeneratedPlanPhases {
		return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan.phases must contain 1-%d phases", maxGeneratedPlanPhases)
	}
	usedIDs := map[string]bool{}
	researchModules := map[string]bool{}
	delegationCount, stepCount := 0, 0
	for phaseIndex := range document.Phases {
		phase := &document.Phases[phaseIndex]
		phase.Name = strings.TrimSpace(phase.Name)
		if err := validateGeneratedPlanText("phase.name", phase.Name, 1, 512); err != nil {
			return generatedPlanDocument{}, nil, nil, err
		}
		phase.ID, err = normalizeGeneratedPlanID(phase.ID, fmt.Sprintf("phase-%d", phaseIndex+1), usedIDs)
		if err != nil {
			return generatedPlanDocument{}, nil, nil, err
		}
		if len(phase.Delegations) == 0 {
			return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan phase %q has no delegations", phase.Name)
		}
		delegationCount += len(phase.Delegations)
		if delegationCount > maxGeneratedDelegations {
			return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan exceeds %d delegations", maxGeneratedDelegations)
		}
		for delegationIndex := range phase.Delegations {
			delegation := &phase.Delegations[delegationIndex]
			delegation.Name = strings.TrimSpace(delegation.Name)
			delegation.AgentName = strings.TrimSpace(delegation.AgentName)
			if err := validateGeneratedPlanText("delegation.name", delegation.Name, 1, 512); err != nil {
				return generatedPlanDocument{}, nil, nil, err
			}
			if delegation.AgentName != "" {
				if err := validateGeneratedPlanText("delegation.agent_name", delegation.AgentName, 1, 128); err != nil {
					return generatedPlanDocument{}, nil, nil, err
				}
			}
			fallbackID := fmt.Sprintf("%s-delegation-%d", phase.ID, delegationIndex+1)
			delegation.ID, err = normalizeGeneratedPlanID(delegation.ID, fallbackID, usedIDs)
			if err != nil {
				return generatedPlanDocument{}, nil, nil, err
			}
			if len(delegation.Steps) == 0 {
				return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan delegation %q has no steps", delegation.Name)
			}
			stepCount += len(delegation.Steps)
			if stepCount > maxGeneratedPlanSteps {
				return generatedPlanDocument{}, nil, nil, fmt.Errorf("generate_plan exceeds %d steps", maxGeneratedPlanSteps)
			}
			for stepIndex := range delegation.Steps {
				step := &delegation.Steps[stepIndex]
				step.Title = strings.TrimSpace(step.Title)
				step.Description = strings.TrimSpace(step.Description)
				step.Kind = strings.ToLower(strings.TrimSpace(step.Kind))
				step.OutputModule = strings.TrimSpace(step.OutputModule)
				step.ResearchQuestion = strings.TrimSpace(step.ResearchQuestion)
				step.ResearchDepth = strings.ToLower(strings.TrimSpace(step.ResearchDepth))
				// Existing v3 plan artifacts predate typed steps. Keep those
				// resumable as generic work while requiring every newly generated
				// model call (through the live tool schema) to choose a kind.
				if step.Kind == "" {
					step.Kind = generatedPlanStepKindWork
				}
				if err := validateGeneratedPlanText("step.title", step.Title, 1, 512); err != nil {
					return generatedPlanDocument{}, nil, nil, err
				}
				if err := validateGeneratedPlanText("step.description", step.Description, 1, 8192); err != nil {
					return generatedPlanDocument{}, nil, nil, err
				}
				switch step.Kind {
				case generatedPlanStepKindWork, generatedPlanStepKindResearch,
					generatedPlanStepKindSynthesis, generatedPlanStepKindDelivery:
				default:
					return generatedPlanDocument{}, nil, nil, errors.New(
						"generate_plan step.kind must be work, research, synthesis, or delivery",
					)
				}
				if step.Kind == generatedPlanStepKindResearch {
					if step.OutputModule == "" || step.ResearchQuestion == "" {
						return generatedPlanDocument{}, nil, nil, errors.New(
							"each research step must identify one substantive output_module and research_question",
						)
					}
					if err := validateGeneratedPlanText("step.output_module", step.OutputModule, 1, 512); err != nil {
						return generatedPlanDocument{}, nil, nil, err
					}
					if err := validateGeneratedPlanText("step.research_question", step.ResearchQuestion, 2, 2048); err != nil {
						return generatedPlanDocument{}, nil, nil, err
					}
					moduleKey := strings.ToLower(step.OutputModule)
					if researchModules[moduleKey] {
						return generatedPlanDocument{}, nil, nil, fmt.Errorf(
							"research output_module %q must have one authoritative investigation step",
							step.OutputModule,
						)
					}
					researchModules[moduleKey] = true
					// Depth is an internal execution hint, not scientific content.
					// Providers that otherwise supply a valid research module may omit
					// this optional field; preserve the plan and choose the neutral deep
					// mode instead of rejecting the whole workflow before execution.
					if step.ResearchDepth == "" {
						step.ResearchDepth = "deep"
					}
					if step.ResearchDepth != "focused" && step.ResearchDepth != "deep" && step.ResearchDepth != "systematic" {
						return generatedPlanDocument{}, nil, nil, errors.New(
							"research step.research_depth must be focused, deep, or systematic",
						)
					}
					if len(step.DiscoveryQueries) > 0 && (len(step.DiscoveryQueries) < 2 || len(step.DiscoveryQueries) > 6) {
						return generatedPlanDocument{}, nil, nil, errors.New(
							"research step.discovery_queries must be omitted or contain the module's Chinese and English queries",
						)
					}
					languages := map[string]bool{}
					for queryIndex := range step.DiscoveryQueries {
						query := &step.DiscoveryQueries[queryIndex]
						query.Language = strings.ToLower(strings.TrimSpace(query.Language))
						query.Query = strings.TrimSpace(query.Query)
						if query.Language != "zh" && query.Language != "en" {
							return generatedPlanDocument{}, nil, nil, errors.New(
								"research discovery query language must be zh or en",
							)
						}
						if err := validateGeneratedPlanText("step.discovery_queries.query", query.Query, 2, 2048); err != nil {
							return generatedPlanDocument{}, nil, nil, err
						}
						languages[query.Language] = true
					}
					if len(step.DiscoveryQueries) > 0 && (!languages["zh"] || !languages["en"]) {
						return generatedPlanDocument{}, nil, nil, errors.New(
							"each research module must carry Chinese and English discovery queries",
						)
					}
				}
				fallbackID := fmt.Sprintf("%s-step-%d", delegation.ID, stepIndex+1)
				step.ID, err = normalizeGeneratedPlanID(step.ID, fallbackID, usedIDs)
				if err != nil {
					return generatedPlanDocument{}, nil, nil, err
				}
			}
		}
	}
	document.Feasibility.Confidence = strings.ToLower(strings.TrimSpace(document.Feasibility.Confidence))
	document.Feasibility.Rationale = strings.TrimSpace(document.Feasibility.Rationale)
	if document.Feasibility.Confidence != "high" && document.Feasibility.Confidence != "medium" && document.Feasibility.Confidence != "low" {
		return generatedPlanDocument{}, nil, nil, errors.New("generate_plan.feasibility.confidence must be high, medium, or low")
	}
	if err := validateGeneratedPlanText("feasibility.rationale", document.Feasibility.Rationale, 1, 8192); err != nil {
		return generatedPlanDocument{}, nil, nil, err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil || len(encoded) > maxGeneratedPlanBytes {
		return generatedPlanDocument{}, nil, nil, errors.New("normalized generate_plan artifact exceeds the bounded v3 plan contract")
	}
	normalized := map[string]any{}
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return generatedPlanDocument{}, nil, nil, err
	}
	return document, normalized, encoded, nil
}

func (s *Server) approveAgentGeneratedPlan(ctx context.Context, sessionID, toolCallID string, input map[string]any) (any, error) {
	for key, value := range input {
		if key == "approve" || key == "human_description" {
			continue
		}
		if value != nil {
			return nil, errors.New("generate_plan approve cannot be combined with plan content")
		}
	}
	s.compatRequestMu.Lock()
	defer s.compatRequestMu.Unlock()
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadataWithContext(ctx, sessionID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("no current plan exists to approve")
		}
		return nil, err
	}
	contextData := copyMapAny(metadata.ContextData)
	artifactID := strings.TrimSpace(stringValue(contextData["_plan_artifact_id"]))
	versionID := strings.TrimSpace(stringValue(contextData["_plan_version_id"]))
	if artifactID == "" || versionID == "" {
		return map[string]any{"status": "no_plan", "message": "No current plan exists to approve."}, nil
	}
	if boolValue(contextData["_plan_approved"], false) {
		return map[string]any{
			"status": "plan_approved", "artifact_id": artifactID, "version_id": versionID, "idempotent": true,
			"effect": agentruntime.ToolEffectValue(agentruntime.ToolEffectUnchanged, "control-state", "plan-approval"),
		}, nil
	}
	now := time.Now().UTC()
	approvalDigest := sha256.Sum256([]byte("generate-plan-inline-approval:v1\x00" + sessionID + "\x00" + artifactID + "\x00" + versionID + "\x00" + toolCallID))
	contextData["_plan_approved"] = true
	contextData["_plan_approved_at"] = now.Format(time.RFC3339Nano)
	contextData["_plan_approval_id"] = "plan-approval-inline:v1:" + hex.EncodeToString(approvalDigest[:16])
	contextData["_plan_approval_fingerprint"] = hex.EncodeToString(approvalDigest[:])
	metadata.ContextData = contextData
	if _, err := s.workspaceStore.SetFrameRuntimeMetadata(sessionID, metadata); err != nil {
		return nil, fmt.Errorf("persist inline plan approval: %w", err)
	}
	return map[string]any{
		"status": "plan_approved", "artifact_id": artifactID, "version_id": versionID,
		"message": "Plan marked approved. Proceed with execution and report every step status.", "idempotent": false,
		"effect": agentruntime.ToolEffectValue(agentruntime.ToolEffectChanged, "control-state", "plan-approval"),
	}, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("generate_plan input contains trailing JSON")
	}
	return nil
}

func validateGeneratedPlanText(field, value string, minRunes, maxRunes int) error {
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes {
		return fmt.Errorf("generate_plan.%s must contain %d-%d characters", field, minRunes, maxRunes)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("generate_plan.%s contains an invalid null byte", field)
	}
	return nil
}

func normalizeGeneratedPlanID(value, fallback string, used map[string]bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > 128 {
		return "", errors.New("generate_plan ids must contain at most 128 ASCII characters")
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", rune(char)) {
			continue
		}
		return "", fmt.Errorf("generate_plan id %q contains an unsupported character", value)
	}
	if used[value] {
		return "", fmt.Errorf("generate_plan id %q is duplicated", value)
	}
	used[value] = true
	return value, nil
}

func generatedPlanTaskIntentSHA(taskIntent string) string {
	taskIntent = strings.TrimSpace(taskIntent)
	if taskIntent == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(taskIntent))
	return hex.EncodeToString(digest[:])
}

func (s *Server) generatedPlanDesiredOutputsContext(frameID string) string {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(frameID) == "" {
		return ""
	}
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(strings.TrimSpace(frameID))
	if err != nil || !found || (!boolValue(metadata.ContextData["_plan_approved"], false) &&
		!boolValue(metadata.ContextData["_plan_execution_authorized"], false)) {
		return ""
	}
	document, err := generatedPlanDocumentFromMap(mapValue(metadata.ContextData["_plan_json"]))
	if err != nil {
		return ""
	}
	// This projection contains state only. Research strategy remains model
	// controlled, while prior observations and open follow-ups survive resume.
	state, err := json.Marshal(generatedPlanResearchModelNavigation(document, metadata.ContextData))
	if err != nil {
		return ""
	}
	return string(state)
}

package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const askUserCurrentTaskEvidenceReference = "user-input:current-task"
const askUserReadinessAttestationSchemaV1 = "synon.ask_user.readiness-attestation.v1"

type askUserEvidenceAuthorityClass string

const (
	askUserEvidenceUserObjective        askUserEvidenceAuthorityClass = "user_objective"
	askUserEvidenceCompletedTool        askUserEvidenceAuthorityClass = "completed_tool"
	askUserEvidenceConfiguredProvider   askUserEvidenceAuthorityClass = "configured_provider"
	askUserEvidenceReadinessAttestation askUserEvidenceAuthorityClass = "readiness_attestation"
)

type askUserEvidenceAuthority struct {
	Class           askUserEvidenceAuthorityClass
	ReadinessStatus string
	Scope           string
	ResultSHA256    string
	Schema          string
	Implementation  string
	Resources       *askUserResourceProfile
	PreflightStatus string
	SetupState      string
	Feasible        *bool
	Blockers        []string
	Ordinal         int
}

var askUserInternalPathPattern = regexp.MustCompile(`(?i)(?:^|[\s("'\x60])(?:/home/|/tmp/|[a-z]:\\|\\\\wsl)`)
var askUserMCPTransportLabelPattern = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]_])mcp(?:$|[^[:alnum:]_-])`)

// normalizeAgentAskUserDecisionEvidence keeps the model responsible for the
// scientific alternatives and their trade-offs while the service owns its
// opaque evidence identities. A model may cite a stale file name, confuse a
// completed preflight with its typed readiness attestation, or overstate a
// readiness status. Those are protocol-shape mistakes, not reasons to reject a
// valid user decision. Normalize them to the strongest authority the current
// task actually has and park the question once.
func (s *Server) normalizeAgentAskUserDecisionEvidence(
	ctx context.Context,
	run *sessionRunnerChatRun,
	result map[string]any,
) error {
	if run == nil || run.Transcript == nil {
		return nil
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok || len(questions) == 0 {
		return errors.New("ask_user normalized questions are unavailable for evidence validation")
	}
	allowed, err := s.availableAgentAskUserEvidenceAuthorities(ctx, run)
	if err != nil {
		return err
	}
	questions = s.bindAskUserRegisteredOptionIdentities(questions)
	questions = normalizeAskUserEvidenceAuthorities(questions, allowed)
	result["questions"] = questions
	// Return exact current-task identities as structured repair data, rather
	// than making the model reconstruct them from prose or opaque call IDs.
	result["available_preflights"] = askUserAvailablePreflightIdentities(allowed)
	publicProseIssues := askUserPublicProseAuthorityIssues(questions, run.executedSkillNamesSnapshot())
	if len(publicProseIssues) == 0 {
		return nil
	}
	return fmt.Errorf(
		"decision options contain internal execution details that cannot be shown to the user: %s; describe the scientific capability or public software instead",
		strings.Join(publicProseIssues, "; "),
	)
}

// A correction is owned by the runner until a distinct user decision has
// current-task authority. Rephrasing a failed or unexecuted tool operation as
// two setup options cannot turn it into a user-owned choice. This gate uses
// normalized evidence identities, never the model's question wording.
func askUserRecoveryDecisionCorrection(run *sessionRunnerChatRun, result map[string]any) map[string]any {
	if run == nil || strings.TrimSpace(run.CorrectionReason) == "" {
		return nil
	}
	questions, ok := result["questions"].([]askUserQuestion)
	if !ok || len(questions) == 0 {
		return nil
	}
	for _, question := range questions {
		grounded := false
		for _, option := range question.Options {
			metadata := option.Metadata
			if strings.TrimSpace(stringValue(metadata["implementation"])) != "" ||
				len(mapValue(metadata["evidence_resolver"])) > 0 {
				grounded = true
			}
			if parameters, valid := askUserExecutionParameterValuesMetadata(metadata["execution_parameter_values"]); valid && len(parameters) > 0 {
				grounded = true
			}
			for _, reference := range append(
				stringArrayValue(metadata["decision_evidence"]),
				stringArrayValue(metadata["readiness_evidence"])...,
			) {
				if reference != askUserCurrentTaskEvidenceReference {
					grounded = true
				}
			}
		}
		if grounded {
			continue
		}
		return map[string]any{
			"ok": false, "executed": false, "code": "agent_owned_decision",
			"status": "agent_owned_decision", "decision_required": false, "retryable": true,
			"message":  "The active recovery has no new verified user-owned choice. Its tool outcome remains an agent-owned condition, not a user decision.",
			"recovery": "Inspect the latest exact tool receipt and the current validated state. Continue the affected step through a materially different governed action; ask the user only when a new scientific, parameter, cost, resource, or permission choice has current-task authority.",
		}
	}
	return nil
}

func (s *Server) availableAgentAskUserEvidenceAuthorities(
	ctx context.Context,
	run *sessionRunnerChatRun,
) (map[string]askUserEvidenceAuthority, error) {
	allowed := map[string]askUserEvidenceAuthority{
		askUserCurrentTaskEvidenceReference: {Class: askUserEvidenceUserObjective, Scope: "current task objective"},
	}
	messages, err := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("load durable ask_user evidence receipts: %w", err)
	}
	ordinal := 0
	for messageIndex, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID != "" && call.VerifiedEvidence {
				ordinal++
				authority := askUserEvidenceAuthority{
					Class: askUserEvidenceCompletedTool, Scope: strings.TrimSpace(call.Name), Ordinal: ordinal,
				}
				if messageIndex+1 < len(messages) {
					result := messages[messageIndex+1]
					if result.Role == "tool" && strings.TrimSpace(result.ToolCallID) == callID {
						if !askUserCompletedResultSupportsDecision(result.Content) {
							continue
						}
						authority = askUserCompletedToolEvidenceAuthority(call, result.Content, authority)
					}
				}
				allowed["tool-call:"+callID] = authority
			}
		}
	}
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(run.SessionID) == "" {
		return allowed, nil
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, strings.TrimSpace(run.SessionID))
	if err != nil {
		return nil, fmt.Errorf("load ask_user compute-provider authority: %w", err)
	}
	if !found {
		return allowed, nil
	}
	providers, err := s.listAgentComputeProviders(access)
	if err != nil {
		return nil, fmt.Errorf("list ask_user compute-provider authority: %w", err)
	}
	for _, raw := range anySliceValue(providers["providers"]) {
		name := strings.TrimSpace(stringValue(mapValue(raw)["name"]))
		if name != "" {
			allowed["compute-provider:"+name] = askUserEvidenceAuthority{
				Class: askUserEvidenceConfiguredProvider, ReadinessStatus: "configured", Scope: name,
			}
		}
	}
	return allowed, nil
}

func normalizeAskUserEvidenceAuthorities(
	questions []askUserQuestion,
	authorities map[string]askUserEvidenceAuthority,
) []askUserQuestion {
	normalized := append([]askUserQuestion(nil), questions...)
	for questionIndex := range normalized {
		question := &normalized[questionIndex]
		question.Options = append([]askUserQuestionOption(nil), question.Options...)
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			metadata := copyMapAny(option.Metadata)
			// These fields are derived from the currently matching receipt, not
			// model input or a previously normalized proposal.
			for _, key := range []string{"preflight_feasible", "preflight_status", "preflight_setup_state", "preflight_blockers"} {
				delete(metadata, key)
			}
			implementationAuthorityReference, implementationAuthority :=
				askUserImplementationEvidenceAuthority(stringValue(metadata["implementation"]), authorities)

			decisionReferences := filterAskUserEvidenceReferences(
				stringArrayValue(metadata["decision_evidence"]), authorities, false,
			)
			// An explicit citation does not make another implementation's
			// preflight applicable to this option (including a different version).
			decisionReferences = filterAskUserMatchingPreflightReferences(decisionReferences, stringValue(metadata["implementation"]), authorities)
			if implementationAuthorityReference != "" && !containsAskUserEvidenceReference(decisionReferences, implementationAuthorityReference) {
				decisionReferences = append(decisionReferences, implementationAuthorityReference)
			}
			hasUserObjective, hasScientificReceipt := askUserDecisionAuthorityClasses(decisionReferences, authorities)
			readinessReferences := filterAskUserEvidenceReferences(
				stringArrayValue(metadata["readiness_evidence"]), authorities, true,
			)
			hasConfiguredAuthority, hasVerifiedAuthority := askUserReadinessAuthorityClasses(readinessReferences, authorities)

			status := strings.ToLower(strings.TrimSpace(stringValue(metadata["readiness_status"])))
			switch status {
			case "verified_ready":
				if !hasVerifiedAuthority {
					if hasConfiguredAuthority {
						status = "configured"
					} else {
						status = "unverified"
					}
				}
			case "configured":
				if !hasConfiguredAuthority {
					status = "unverified"
				}
			case "not_applicable":
				readinessReferences = nil
			default:
				status = "unverified"
				readinessReferences = nil
			}

			reportedSelectionBasis := strings.ToLower(strings.TrimSpace(stringValue(metadata["selection_basis"])))
			reportedRecommended := boolValue(metadata["recommended"], false)
			recommendationSupported := askUserRecommendationSupported(
				reportedSelectionBasis, hasUserObjective, hasScientificReceipt, hasConfiguredAuthority, hasVerifiedAuthority,
			)
			selectionBasis := reportedSelectionBasis
			switch selectionBasis {
			case "scientific_evidence":
				if !hasScientificReceipt {
					selectionBasis = "user_objective"
				}
			case "execution_readiness":
				if !hasConfiguredAuthority && !hasVerifiedAuthority {
					selectionBasis = askUserFallbackDecisionBasis(hasScientificReceipt)
				}
			case "balanced_tradeoff":
				if !hasScientificReceipt || (!hasConfiguredAuthority && !hasVerifiedAuthority) {
					selectionBasis = askUserFallbackDecisionBasis(hasScientificReceipt)
				}
			case "user_objective":
			default:
				selectionBasis = "user_objective"
			}
			if selectionBasis == "user_objective" && !hasUserObjective {
				decisionReferences = append(decisionReferences, askUserCurrentTaskEvidenceReference)
			}
			if len(decisionReferences) == 0 {
				decisionReferences = []string{askUserCurrentTaskEvidenceReference}
				selectionBasis = "user_objective"
			}

			readinessDisplay := askUserReadinessDisplayText(status, sessionRunnerResponseLanguage(question.Question) == "zh")
			metadata["readiness"] = readinessDisplay
			metadata["readiness_status"] = status
			metadata["decision_evidence"] = decisionReferences
			metadata["readiness_evidence"] = readinessReferences
			metadata["reported_selection_basis"] = reportedSelectionBasis
			metadata["selection_basis"] = selectionBasis
			metadata["reported_recommended"] = reportedRecommended
			metadata["recommended"] = reportedRecommended && recommendationSupported
			metadata["recommendation_supported"] = recommendationSupported
			if implementationAuthority.Resources != nil {
				metadata["resources"] = map[string]any{
					"cpu":    implementationAuthority.Resources.CPU,
					"memory": implementationAuthority.Resources.Memory,
					"gpu":    implementationAuthority.Resources.GPU,
				}
			}
			// These fields are server-derived continuity evidence. They are not
			// rendered as option prose, but let the AskUser boundary distinguish a
			// genuine machine-capacity blocker from an ordinary repairable install
			// failure before it parks the task or offers another implementation.
			if implementationAuthority.Feasible != nil {
				metadata["preflight_feasible"] = *implementationAuthority.Feasible
			}
			if implementationAuthority.PreflightStatus != "" {
				metadata["preflight_status"] = implementationAuthority.PreflightStatus
			}
			if implementationAuthority.SetupState != "" {
				metadata["preflight_setup_state"] = implementationAuthority.SetupState
			}
			if len(implementationAuthority.Blockers) > 0 {
				metadata["preflight_blockers"] = append([]string(nil), implementationAuthority.Blockers...)
			}
			option.Metadata = metadata
			resources := askUserResourceProfileMetadataValue(metadata["resources"])
			option.Description = askUserDecisionDescription(
				question.Question,
				strings.TrimSpace(stringValue(metadata["route_description"])),
				status,
				resources,
			)
		}
	}
	return normalized
}

func askUserResourceProfileFromPreflightRequirements(requirements map[string]any) *askUserResourceProfile {
	cpu := int(numberValue(requirements["min_cpu_cores"]))
	memoryMB := int64(numberValue(requirements["min_memory_mb"]))
	accelerator := strings.ToLower(strings.TrimSpace(stringValue(requirements["accelerator"])))
	acceleratorMemoryMB := int64(numberValue(requirements["min_accelerator_memory_mb"]))
	if cpu <= 0 || memoryMB <= 0 {
		return nil
	}
	gpu := "not required"
	switch accelerator {
	case "required":
		gpu = "required"
	case "optional":
		gpu = "optional"
	case "none":
	default:
		return nil
	}
	if acceleratorMemoryMB > 0 {
		gpu += ", " + askUserHumanMemorySize(acceleratorMemoryMB) + " VRAM"
	}
	return &askUserResourceProfile{
		CPU:    strconv.Itoa(cpu) + " cores",
		Memory: askUserHumanMemorySize(memoryMB),
		GPU:    gpu,
	}
}

func askUserHumanMemorySize(megabytes int64) string {
	if megabytes > 0 && megabytes%1024 == 0 {
		return strconv.FormatInt(megabytes/1024, 10) + " GB"
	}
	return strconv.FormatInt(megabytes, 10) + " MB"
}

func containsAskUserEvidenceReference(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func askUserRecommendationSupported(
	basis string,
	hasUserObjective, hasScientificReceipt, hasConfiguredAuthority, hasVerifiedAuthority bool,
) bool {
	switch basis {
	case "user_objective":
		return hasUserObjective
	case "scientific_evidence":
		return hasScientificReceipt
	case "execution_readiness":
		return hasConfiguredAuthority || hasVerifiedAuthority
	case "balanced_tradeoff":
		return hasScientificReceipt && (hasConfiguredAuthority || hasVerifiedAuthority)
	default:
		return false
	}
}

func filterAskUserEvidenceReferences(
	references []string,
	authorities map[string]askUserEvidenceAuthority,
	readinessOnly bool,
) []string {
	filtered := make([]string, 0, len(references))
	seen := map[string]bool{}
	for _, reference := range references {
		reference = strings.TrimSpace(reference)
		authority, found := authorities[reference]
		if !found || seen[reference] {
			continue
		}
		if readinessOnly {
			validReadiness := authority.Class == askUserEvidenceConfiguredProvider ||
				(authority.Class == askUserEvidenceReadinessAttestation && validAskUserReadinessAttestation(authority))
			if !validReadiness {
				continue
			}
		}
		seen[reference] = true
		filtered = append(filtered, reference)
	}
	return filtered
}

func askUserDecisionAuthorityClasses(
	references []string,
	authorities map[string]askUserEvidenceAuthority,
) (hasUserObjective, hasScientificReceipt bool) {
	for _, reference := range references {
		authority, found := authorities[reference]
		if !found {
			continue
		}
		hasUserObjective = hasUserObjective || authority.Class == askUserEvidenceUserObjective
		hasScientificReceipt = hasScientificReceipt || authority.Class == askUserEvidenceCompletedTool
	}
	return hasUserObjective, hasScientificReceipt
}

func askUserReadinessAuthorityClasses(
	references []string,
	authorities map[string]askUserEvidenceAuthority,
) (hasConfiguredAuthority, hasVerifiedAuthority bool) {
	for _, reference := range references {
		authority, found := authorities[reference]
		if !found {
			continue
		}
		switch authority.Class {
		case askUserEvidenceConfiguredProvider:
			hasConfiguredAuthority = true
		case askUserEvidenceReadinessAttestation:
			if !validAskUserReadinessAttestation(authority) {
				continue
			}
			hasConfiguredAuthority = authority.ReadinessStatus == "configured" || authority.ReadinessStatus == "verified_ready"
			hasVerifiedAuthority = hasVerifiedAuthority || authority.ReadinessStatus == "verified_ready"
		}
	}
	return hasConfiguredAuthority, hasVerifiedAuthority
}

func askUserFallbackDecisionBasis(hasScientificReceipt bool) string {
	if hasScientificReceipt {
		return "scientific_evidence"
	}
	return "user_objective"
}

func validAskUserReadinessAttestation(authority askUserEvidenceAuthority) bool {
	if authority.Class != askUserEvidenceReadinessAttestation ||
		authority.Schema != askUserReadinessAttestationSchemaV1 ||
		strings.TrimSpace(authority.Scope) == "" || len(authority.ResultSHA256) != 64 {
		return false
	}
	for _, char := range strings.ToLower(authority.ResultSHA256) {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return authority.ReadinessStatus == "configured" || authority.ReadinessStatus == "verified_ready"
}

func askUserPublicProseAuthorityIssues(questions []askUserQuestion, loadedSkillNames []string) []string {
	issues := []string{}
	for _, question := range questions {
		for _, option := range question.Options {
			fields := []string{
				option.Label,
				stringValue(option.Metadata["route_description"]),
				option.Pros,
				option.Cons,
				stringValue(option.Metadata["readiness"]),
				stringValue(option.Metadata["reported_readiness"]),
				stringValue(option.Metadata["expected_outcome"]),
				stringValue(option.Metadata["selection_rationale"]),
			}
			fields = append(fields, askUserResourceProfileTexts(option.Metadata["resources"])...)
			for _, field := range fields {
				if askUserPublicProseExposesInternalIdentifier(field, loadedSkillNames) {
					issues = append(issues, fmt.Sprintf("option %q exposes an internal execution identifier in user-facing text; use the scientific capability or public product name instead", option.Label))
					break
				}
			}
		}
	}
	return issues
}

func askUserPublicProseExposesInternalIdentifier(text string, loadedSkillNames []string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if sessionRunnerProgressNarrationExposesProductInternals(text) ||
		askUserMCPTransportLabelPattern.MatchString(text) ||
		askUserInternalPathPattern.MatchString(text) || strings.Contains(text, "```") {
		return true
	}
	lower := strings.ToLower(text)
	for _, skillName := range loadedSkillNames {
		skillName = strings.ToLower(strings.TrimSpace(skillName))
		if len(skillName) >= 4 && (strings.Contains(skillName, "-") || strings.Contains(skillName, "_")) &&
			strings.Contains(lower, skillName) {
			return true
		}
	}
	return false
}

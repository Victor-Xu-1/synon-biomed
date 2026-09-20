package server

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/toolcontract"
)

const sessionRunnerDurableCorrectionContextMarker = "Synon durable correction state from a prior bounded execution unit"

// sessionRunnerCorrectionStillRequiresAction keeps one stop-hook acceptance
// condition active across model rounds. A rejected, failed, or non-executing
// preflight does not satisfy a correction that requires real tool evidence.
// For a validator-owned artifact repair, inspection and editing are necessary
// intermediate actions but the correction is not complete until the changed
// affected artifact has a successful save receipt.
func sessionRunnerCorrectionStillRequiresAction(run *sessionRunnerChatRun, messages []agentruntime.Message) bool {
	if run == nil || (!sessionRunnerCorrectionReasonRequiresTool(run.CorrectionReason, run.CorrectionDetail) &&
		!runnerCorrectionRequiresSourceLocatorRepair(run.CorrectionReason, run.CorrectionDetail)) {
		return false
	}
	boundary := -1
	for index, message := range messages {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			boundary = index
		}
	}
	if boundary < 0 {
		return true
	}
	if runnerCorrectionRequiresRecordDepthRepair(run.CorrectionReason, run.CorrectionDetail) {
		// A source-depth correction cannot be discharged by rewriting and saving
		// the same unsupported claim. First require a substantive record receipt,
		// or a changed artifact that removes the unsupported reference entirely.
		if runnerUnsupportedSourceRemovalSavedSinceCorrection(messages) {
			return false
		}
		if len(runnerCorrectionMissingRecordClasses(run, messages)) > 0 {
			if strings.Contains(strings.ToLower(run.CorrectionDetail), "evidence_record_class_missing:") {
				// A changed publication may have added or removed the class-level row.
				// Return to the immutable validator after one real mutation instead
				// of forcing a source call from stale pre-edit requirements.
				edited, saved := runnerSourceRepairMutationState(messages)
				return !edited || !saved
			}
			return true
		}
		// A server-attested exact record may have been acquired before the
		// validator opened this correction window. Reuse that durable receipt; a
		// second source read adds no information and turns a normal artifact repair
		// into a source/edit/save loop. The mutation still has to be real and the
		// changed artifact still has to be published before control returns to the
		// immutable validator.
		edited, saved := runnerSourceRepairMutationState(messages)
		return !edited || !saved
	}
	calls := runnerToolCallsByCallID(messages[boundary+1:])
	if runnerCorrectionRequiresSourceLocatorRepair(run.CorrectionReason, run.CorrectionDetail) {
		if runnerUnsupportedSourceRemovalSavedSinceCorrection(messages) {
			return false
		}
		// The validator has already reconstructed the complete logical-task source
		// corpus before opening this correction. Requiring another source read in
		// the correction window discards that durable evidence and traps weaker
		// providers in edit/save loops. A changed publication is enough to return
		// control to the immutable validator; unsupported or fabricated locators
		// will reopen the correction against the new bytes.
		edited, saved := runnerSourceRepairMutationState(messages)
		return !edited || !saved
	}
	for _, message := range messages[boundary+1:] {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call := calls[message.ToolCallID]
		if runnerCorrectionReadCall(call) {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		if agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			if runnerCorrectionIsAgentOwnedArtifactRepair(run.CorrectionReason, run.CorrectionDetail) &&
				!strings.EqualFold(call.Name, "save_artifacts") {
				continue
			}
			if runnerCorrectionRequiresValidatedPublicDownload(run.CorrectionReason, run.CorrectionDetail) &&
				!strings.EqualFold(call.Name, "download_public_scientific_file") {
				continue
			}
			return false
		}
	}
	return true
}

// sessionRunnerCorrectionReadyForRevalidation identifies the exact state in
// which a correction-owned execution unit has produced its required durable
// action and must return to the immutable stop-hook validator. Continuing with
// provider-auto tool choice here lets semantically identical save calls evade
// argument-level no-progress detection by changing only their descriptions.
func sessionRunnerCorrectionReadyForRevalidation(
	run *sessionRunnerChatRun,
	messages []agentruntime.Message,
) bool {
	if run == nil || (strings.TrimSpace(run.CorrectionReason) == "") ||
		sessionRunnerCorrectionStillRequiresAction(run, messages) {
		return false
	}
	boundary := -1
	for index, message := range messages {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			boundary = index
		}
	}
	if boundary < 0 {
		return false
	}
	calls := runnerToolCallsByCallID(messages[boundary+1:])
	for _, message := range messages[boundary+1:] {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) ||
			agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
			continue
		}
		if call, found := calls[message.ToolCallID]; found && !runnerCorrectionReadCall(call) {
			return true
		}
	}
	return false
}

func sessionRunnerImmediateArtifactRepairRequired(
	run *sessionRunnerChatRun,
	messages []agentruntime.Message,
) bool {
	if len(runnerPendingArtifactSaveEvidenceClasses(run, messages)) > 0 {
		return true
	}
	if runnerLatestArtifactSaveRequiresCorrection(messages) {
		return true
	}
	paths, _ := runnerPendingArtifactFileRepair(messages)
	return len(paths) > 0
}

func runnerLatestArtifactSaveRequiresCorrection(messages []agentruntime.Message) bool {
	calls := runnerToolCallsByCallID(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "saveartifacts" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil {
			return false
		}
		return runnerArtifactSaveRequiresCorrection(result) && len(anySliceValue(result["errors"])) > 0
	}
	return false
}

func runnerRequiredMCPSourceClassForChoice(
	run *sessionRunnerChatRun,
	messages []agentruntime.Message,
	choice any,
	tools []agentruntime.ToolSchema,
) string {
	object, ok := choice.(map[string]any)
	if !ok {
		return ""
	}
	chosen := strings.TrimSpace(stringValue(object["name"]))
	chosenCapabilities := agentRuntimeToolCapabilities(tools, chosen)
	if !runtimeCapabilitiesContain(chosenCapabilities, "mcp-bridge") {
		return ""
	}
	if pending := runnerPendingArtifactSaveEvidenceClasses(run, messages); len(pending) > 0 {
		return pending[0]
	}
	if run != nil && runnerCorrectionRequiresRecordDepthRepair(run.CorrectionReason, run.CorrectionDetail) {
		if missing := runnerCorrectionMissingRecordClasses(run, messages); len(missing) > 0 {
			return missing[0]
		}
	}
	return ""
}

// sessionRunnerCorrectionRequiredToolChoice gives weak providers a bounded,
// provider-neutral repair protocol only after an immutable validator has named
// an agent-owned artifact defect. Arguments and scientific content remain
// model-selected; the Harness constrains only the necessary state transition.
func sessionRunnerCorrectionRequiredToolChoice(
	run *sessionRunnerChatRun,
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) (choice any) {
	defer func() {
		choice = runnerCorrectionFailedEditChoice(choice, messages, tools)
	}()
	if choice := runnerPendingAskUserRequiredSkillChoice(runnerPendingAskUserRequiredSkillNames(messages), tools); choice != nil {
		return choice
	}
	// A connector call rejected by the live-schema boundary must yield control
	// to the connector Skill before another REPL call is forced. Otherwise a
	// weak provider is trapped in a schema-guessing loop because the record-depth
	// state machine keeps selecting REPL while hiding the only corrective action.
	if runnerLatestToolPreflightStatus(messages) == "mcp_schema_preflight_required" {
		if choice := runnerCorrectionCapabilityToolChoice(
			tools, "skill-invocation", runnerSuccessfulToolNamesSinceCorrection(messages),
		); choice != nil && !runnerCorrectionHasLoadedSourceConnector(run) {
			return choice
		}
	}
	if pending := runnerPendingArtifactSaveEvidenceClasses(run, messages); len(pending) > 0 {
		if choice := runnerCorrectionSourceClassToolChoice(run, pending[0], messages, tools); choice != nil {
			return choice
		}
		// Every authoritative route for the currently unsupported reference has
		// now been attempted. Close the repair set by changing the artifact named
		// by the validator, then publish that changed version. Do not return to an
		// unconstrained tool round where a weak provider can add fresh unverified
		// references and turn a finite repair into an open-ended search loop.
		paths, edited := runnerPendingArtifactEvidenceRepairMutation(messages)
		if len(paths) > 0 && !edited {
			return runnerCorrectionCapabilityToolChoice(tools, "artifact-edit", nil)
		}
		if len(paths) > 0 && edited {
			return runnerCorrectionCapabilityToolChoice(tools, "artifact-publication", nil)
		}
	}
	if paths, edited := runnerPendingArtifactFileRepair(messages); len(paths) > 0 {
		if !edited {
			return runnerCorrectionCapabilityToolChoice(tools, "artifact-edit", nil)
		}
		if edited {
			return runnerCorrectionCapabilityToolChoice(tools, "artifact-publication", nil)
		}
	}
	if choice, handled := runnerSourceLocatorRepairRequiredToolChoice(run, messages, tools); handled {
		return choice
	}
	if run != nil && runnerCorrectionRequiresSourceLocatorRepair(run.CorrectionReason, run.CorrectionDetail) {
		if runnerCorrectionRequiresRecordDepthRepair(run.CorrectionReason, run.CorrectionDetail) {
			if missing := runnerCorrectionMissingRecordClasses(run, messages); len(missing) > 0 {
				if choice := runnerCorrectionSourceClassToolChoice(run, missing[0], messages, tools); choice != nil {
					return choice
				}
			}
			edited, saved := runnerSourceRepairMutationState(messages)
			if !edited {
				return runnerCorrectionCapabilityToolChoice(tools, "artifact-edit", nil)
			}
			if edited && !saved {
				return runnerCorrectionCapabilityToolChoice(tools, "artifact-publication", nil)
			}
		}
		// The reference Harness requires a real action at a stop-hook boundary
		// but does not prescribe a fixed source or tool sequence.  Returning nil
		// lets the caller use provider-neutral tool_choice=required while this
		// module keeps the semantic evidence/edit/save transition active.
		return nil
	}
	if run == nil || !runnerCorrectionIsAgentOwnedArtifactRepair(run.CorrectionReason, run.CorrectionDetail) {
		if run != nil && runnerCorrectionRequiresValidatedPublicDownload(run.CorrectionReason, run.CorrectionDetail) {
			succeeded := runnerSuccessfulToolNamesSinceCorrection(messages)
			if choice := runnerCorrectionCapabilityToolChoice(
				tools, "source-locator-read", succeeded,
			); choice != nil {
				return choice
			}
			if choice := runnerCorrectionCapabilityToolChoice(tools, "source-download", nil); choice != nil {
				return choice
			}
		}
		return nil
	}
	succeeded := runnerSuccessfulToolNamesSinceCorrection(messages)
	for _, capability := range []string{"artifact-read", "artifact-edit", "artifact-publication"} {
		if choice := runnerCorrectionCapabilityToolChoice(tools, capability, succeeded); choice != nil {
			return choice
		}
	}
	return nil
}

func runnerLatestToolPreflightStatus(messages []agentruntime.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil {
			return ""
		}
		if nested := mapValue(result["result"]); len(nested) > 0 {
			result = nested
		}
		if executed, recorded := result["executed"]; !recorded || boolValue(executed, true) {
			return ""
		}
		return strings.TrimSpace(stringValue(result["status"]))
	}
	return ""
}

// runnerPendingArtifactFileRepair keeps structured artifact repair on one
// deterministic state transition: a validator rejection must be followed by
// a successful edit of the affected file before save_artifacts can run again.
// This avoids charging a second real tool failure for an unchanged malformed
// CSV/TSV/JSON payload while leaving the content and repair itself model-owned.
func runnerPendingArtifactFileRepair(messages []agentruntime.Message) ([]string, bool) {
	calls := runnerToolCallsByCallID(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "saveartifacts" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil ||
			strings.TrimSpace(stringValue(result["code"])) != "artifact_save_requires_correction" {
			return nil, false
		}
		paths := runnerArtifactSaveFileRepairPaths(result)
		if len(paths) == 0 {
			return nil, false
		}
		pathSet := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			pathSet[strings.ToLower(filepath.Clean(path))] = struct{}{}
		}
		for _, candidate := range messages[index+1:] {
			if candidate.Role != "tool" || strings.TrimSpace(candidate.Content) == "" {
				continue
			}
			candidateCall, ok := calls[candidate.ToolCallID]
			if !ok {
				continue
			}
			var value any
			if json.Unmarshal([]byte(candidate.Content), &value) != nil ||
				agentruntime.ClassifyToolResult(value) != agentruntime.ToolResultSucceeded {
				continue
			}
			if runnerArtifactRepairToolTouchedPath(candidateCall, value, pathSet) {
				return paths, true
			}
		}
		return paths, false
	}
	return nil, false
}

func runnerArtifactRepairToolTouchedPath(
	call agentruntime.ToolCall,
	result any,
	paths map[string]struct{},
) bool {
	if len(paths) == 0 || !runnerCorrectionResultMadeMutation(result) {
		return false
	}
	normalizedTool := normalizeAgentToolName(call.Name)
	if normalizedTool == "editfile" {
		var input map[string]any
		if json.Unmarshal(call.Arguments, &input) != nil {
			return false
		}
		path := strings.TrimSpace(firstNonEmpty(stringValue(input["file_path"]), stringValue(input["path"])))
		_, found := paths[strings.ToLower(filepath.Clean(path))]
		return found
	}
	if normalizedTool != "repl" && normalizedTool != "python" && normalizedTool != "r" && normalizedTool != "bash" {
		return false
	}
	return runnerArtifactRepairResultContainsPath(result, paths, 0, new(int))
}

func runnerArtifactRepairResultContainsPath(
	value any,
	paths map[string]struct{},
	depth int,
	visited *int,
) bool {
	if visited == nil || depth > 8 || *visited >= 2048 {
		return false
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if normalizeAgentToolName(key) == "fileswritten" {
				for _, raw := range anySliceValue(child) {
					path := strings.TrimSpace(stringValue(raw))
					if record := mapValue(raw); len(record) > 0 {
						path = strings.TrimSpace(firstNonEmpty(
							stringValue(record["path"]),
							stringValue(record["file_path"]),
							stringValue(record["filename"]),
						))
					}
					path = strings.ToLower(filepath.Clean(path))
					if _, found := paths[path]; found {
						return true
					}
				}
			}
			if runnerArtifactRepairResultContainsPath(child, paths, depth+1, visited) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if runnerArtifactRepairResultContainsPath(child, paths, depth+1, visited) {
				return true
			}
		}
	}
	return false
}

func runnerArtifactSaveFileRepairPaths(result map[string]any) []string {
	paths := make([]string, 0, 2)
	for _, raw := range anySliceValue(result["errors"]) {
		failure := mapValue(raw)
		switch strings.TrimSpace(stringValue(failure["code"])) {
		case "invalid_delimited_artifact", "invalid_json_artifact", "unresolved_template_marker":
			if path := strings.TrimSpace(stringValue(failure["path"])); path != "" {
				paths = append(paths, path)
			}
		}
	}
	return uniqueSortedFolded(paths)
}

func runnerPendingArtifactSaveEvidenceClasses(run *sessionRunnerChatRun, messages []agentruntime.Message) []string {
	calls := runnerToolCallsByCallID(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "saveartifacts" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil {
			return nil
		}
		if !runnerArtifactSaveRequiresCorrection(result) {
			return nil
		}
		unsupported, _, _ := artifactSaveEvidenceRepairReferences(result)
		if len(unsupported) == 0 {
			return nil
		}
		// The save verifier has already evaluated every durable receipt that
		// existed before this warning. Do not fold task-wide historical signals
		// back into the repair window: doing so can falsely mark the exact
		// unsupported URL/DOI/NCT as resolved merely because an earlier receipt
		// from the same source class exists. Only evidence acquired after this
		// warning can discharge it; otherwise the reference must be replaced or
		// removed before the next save.
		depth := runnerEvidenceRecordDepthIndexFromMessages(messages[index+1:])
		classes := make([]string, 0)
		for _, reference := range unsupported {
			class, identifier := runnerEvidenceReferenceRequirement(reference)
			if class != "" && identifier != "" && !depth.contains(class, identifier) {
				classes = append(classes, class)
			}
		}
		return uniqueSortedFolded(classes)
	}
	return nil
}

func runnerPendingArtifactEvidenceRepairMutation(messages []agentruntime.Message) ([]string, bool) {
	calls := runnerToolCallsByCallID(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "saveartifacts" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &result) != nil {
			return nil, false
		}
		if !runnerArtifactSaveRequiresCorrection(result) {
			return nil, false
		}
		unsupported, _, _ := artifactSaveEvidenceRepairReferences(result)
		if len(unsupported) == 0 {
			return nil, false
		}
		paths := runnerArtifactSaveEvidenceRepairPaths(result)
		if len(paths) == 0 {
			var input map[string]any
			if json.Unmarshal(call.Arguments, &input) == nil {
				for _, rawPath := range anySliceValue(input["files"]) {
					if path := strings.TrimSpace(stringValue(rawPath)); path != "" {
						paths = append(paths, path)
					}
				}
			}
			paths = uniqueSortedFolded(paths)
		}
		pathSet := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			pathSet[strings.ToLower(filepath.Clean(path))] = struct{}{}
		}
		for _, candidate := range messages[index+1:] {
			if candidate.Role != "tool" || strings.TrimSpace(candidate.Content) == "" {
				continue
			}
			candidateCall, ok := calls[candidate.ToolCallID]
			if !ok || normalizeAgentToolName(candidateCall.Name) != "editfile" {
				continue
			}
			var value any
			if json.Unmarshal([]byte(candidate.Content), &value) != nil ||
				agentruntime.ClassifyToolResult(value) != agentruntime.ToolResultSucceeded ||
				!runnerCorrectionResultMadeMutation(value) {
				continue
			}
			var input map[string]any
			if json.Unmarshal(candidateCall.Arguments, &input) != nil {
				continue
			}
			path := strings.TrimSpace(firstNonEmpty(stringValue(input["file_path"]), stringValue(input["path"])))
			if _, ok := pathSet[strings.ToLower(filepath.Clean(path))]; ok {
				return paths, true
			}
		}
		return paths, false
	}
	return nil, false
}

func runnerArtifactSaveEvidenceRepairPaths(result map[string]any) []string {
	paths := make([]string, 0, 2)
	for _, collection := range []any{result["errors"], result["warnings"]} {
		for _, raw := range anySliceValue(collection) {
			failure := mapValue(raw)
			if strings.TrimSpace(stringValue(failure["code"])) != "unsupported_evidence_references" {
				continue
			}
			if path := strings.TrimSpace(stringValue(failure["path"])); path != "" {
				paths = append(paths, path)
			}
		}
	}
	return uniqueSortedFolded(paths)
}

func runnerEvidenceReferenceRequirement(reference string) (class, identifier string) {
	value := strings.TrimSpace(reference)
	lower := strings.ToLower(value)
	if marker := strings.LastIndex(lower, "identifier="); marker >= 0 {
		value = strings.TrimSpace(value[marker+len("identifier="):])
		if boundary := strings.IndexAny(value, " ,;\r\n"); boundary >= 0 {
			value = value[:boundary]
		}
		lower = strings.ToLower(value)
	}
	switch {
	case strings.HasPrefix(lower, "nct:") || sessionRunnerNCTPattern.MatchString(value):
		return "trial", strings.ToUpper(sessionRunnerNCTPattern.FindString(value))
	case strings.HasPrefix(lower, "doi:") || strings.HasPrefix(lower, "pmid:") || strings.HasPrefix(lower, "pmcid:"):
		return "publication", runnerEvidenceCanonicalPublication(value)
	case strings.HasPrefix(lower, "patent:"):
		return "patent", runnerEvidenceCanonicalPatent(value)
	case !strings.Contains(lower, "://") && runnerEvidenceCanonicalPatent(value) != "":
		return "patent", runnerEvidenceCanonicalPatent(value)
	case strings.HasPrefix(lower, "url:"):
		url := strings.TrimSpace(value[len("url:"):])
		return "web", runnerEvidenceCanonicalWeb(url)
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return "web", runnerEvidenceCanonicalWeb(value)
	default:
		return "", ""
	}
}

func runnerCorrectionRequiresRecordDepthRepair(reason, detail string) bool {
	if strings.TrimSpace(reason) != "artifact_reference_correction_required" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(detail))
	return strings.Contains(lower, "evidence_record_depth_missing:") ||
		strings.Contains(lower, "evidence_record_depth_required:") ||
		strings.Contains(lower, "evidence_record_class_missing:")
}

func runnerCorrectionMissingRecordClasses(run *sessionRunnerChatRun, messages []agentruntime.Message) []string {
	if run == nil {
		return nil
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	for _, signal := range run.trustedScientificReviewSignalsSnapshot() {
		class, identifier, found := runnerEvidenceRecordSignal(signal)
		if !found {
			continue
		}
		switch class {
		case "patent":
			index.patents[identifier] = struct{}{}
		case "trial":
			index.trials[identifier] = struct{}{}
		case "publication":
			index.publications[identifier] = struct{}{}
		case "web":
			index.web[identifier] = struct{}{}
		}
	}
	requirements := runnerEvidenceRecordRequirementsFromCorrection(run.CorrectionDetail)
	// A mixed legacy finding may contain advisory class-coverage gaps together
	// with one represented class that lacks depth. Once a route for those
	// advisory gaps is durably exhausted, they must not keep the represented
	// class's correction alive. Identifier-specific depth findings remain
	// blocking until their row is repaired.
	advisoryClassCoverageMixedWithDepth := strings.Contains(
		strings.ToLower(run.CorrectionDetail), "evidence_record_depth_required:",
	)
	missing := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement.identifier != "" {
			if !index.contains(requirement.class, requirement.identifier) {
				missing = append(missing, requirement.class)
			}
			continue
		}
		satisfied := requirement.class == "patent" && len(index.patents) > 0 ||
			requirement.class == "trial" && len(index.trials) > 0 ||
			requirement.class == "publication" && len(index.publications) > 0 ||
			requirement.class == "web" && len(index.web) > 0
		if !satisfied {
			if advisoryClassCoverageMixedWithDepth && runnerEvidenceRouteExhaustedForClass(requirement.class, run.trustedScientificReviewSignalsSnapshot()) {
				continue
			}
			missing = append(missing, requirement.class)
		}
	}
	return uniqueSortedFolded(missing)
}

func runnerEvidenceRecordSignal(signal string) (class, identifier string, found bool) {
	signal = strings.TrimSpace(signal)
	if !strings.HasPrefix(signal, trustedScientificEvidenceRecordSignalPrefix) {
		return "", "", false
	}
	remainder := strings.TrimPrefix(signal, trustedScientificEvidenceRecordSignalPrefix)
	separator := strings.Index(remainder, ":")
	if separator <= 0 || separator >= len(remainder)-1 {
		return "", "", false
	}
	class, identifier = remainder[:separator], remainder[separator+1:]
	if class != "patent" && class != "trial" && class != "publication" && class != "web" {
		return "", "", false
	}
	switch class {
	case "patent":
		identifier = runnerEvidenceCanonicalPatent(identifier)
	case "trial":
		identifier = strings.ToUpper(sessionRunnerNCTPattern.FindString(identifier))
	case "publication":
		identifier = runnerEvidenceCanonicalPublication(identifier)
	case "web":
		if len(identifier) != sha256.Size*2 {
			identifier = ""
		} else {
			for _, character := range identifier {
				if !strings.ContainsRune("0123456789abcdef", character) {
					identifier = ""
					break
				}
			}
		}
	}
	if identifier == "" {
		return "", "", false
	}
	return class, identifier, true
}

func runnerCorrectionSourceClassToolChoice(
	run *sessionRunnerChatRun,
	class string,
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) any {
	exhausted := runnerCorrectionExhaustedSourceToolsSinceBoundary(messages, class)
	for name, exhaustedPreviously := range runnerCorrectionPreviouslyExhaustedSourceTools(messages, class) {
		if exhaustedPreviously {
			exhausted[name] = true
		}
	}
	// A missing Skill name is a recoverable routing mistake, not a reason to
	// discard the task or replay the same request. Carry that exact failure over
	// the next validator boundary so the repair advances to the already
	// advertised connector transport. Scope it to the immediately preceding
	// correction window: a later, unrelated source class may still load a valid
	// Skill selected from the catalog.
	if runnerCorrectionPreviousWindowMissingSkill(messages) {
		exhausted[normalizeAgentToolName("skill")] = true
	}
	succeeded := runnerSuccessfulToolNamesSinceCorrection(messages)
	if run != nil {
		prefix := trustedScientificEvidenceRouteExhaustedSignalPrefix + class + ":"
		for _, signal := range run.trustedScientificReviewSignalsSnapshot() {
			normalized := strings.ToLower(strings.TrimSpace(signal))
			if strings.HasPrefix(normalized, prefix) {
				exhausted[strings.TrimPrefix(normalized, prefix)] = true
			}
		}
	}
	// Prefer one exact record reader for the missing source class. The route is
	// selected only from the immutable Tool metadata captured for this turn;
	// neither task prose nor a tool-name preference list participates.
	if choice := runnerCorrectionCapabilityToolChoiceAll(
		tools, []string{"source-class:" + class, "source-record-read"}, exhausted,
	); choice != nil {
		return choice
	}
	// When a completed route returned an exact URL, continue that returned
	// frontier before opening another discovery or connector route. This is a
	// generic state transition shared by publications, trials, patents,
	// datasets, structures, and future source classes.
	if runnerCorrectionHasReturnedSourceURL(messages) {
		if choice := runnerCorrectionCapabilityToolChoice(
			tools, "source-locator-read", exhausted,
		); choice != nil {
			return choice
		}
	}
	// Connector schemas are load-on-demand Skills. Load one once at the
	// correction boundary before exposing the generic MCP bridge. Both are
	// selected by capability, not by scientific source type or tool identity.
	if !runnerCorrectionHasLoadedSourceConnector(run) {
		if choice := runnerCorrectionCapabilityToolChoice(
			tools, "skill-invocation", exhausted,
		); choice != nil {
			choiceObject, _ := choice.(map[string]any)
			if !succeeded[strings.ToLower(strings.TrimSpace(stringValue(choiceObject["name"])))] {
				return choice
			}
		}
	}
	if choice := runnerCorrectionCapabilityToolChoice(tools, "mcp-bridge", exhausted); choice != nil {
		return choice
	}
	for _, capability := range []string{
		"source-locator-read", "source-investigation", "source-discovery", "evidence-read",
	} {
		if choice := runnerCorrectionCapabilityToolChoice(tools, capability, exhausted); choice != nil {
			return choice
		}
	}
	return nil
}

func runnerCorrectionCapabilityToolChoice(
	tools []agentruntime.ToolSchema,
	capability string,
	exhausted map[string]bool,
) any {
	return runnerCorrectionCapabilityToolChoiceAll(tools, []string{capability}, exhausted)
}

func runnerCorrectionCapabilityToolChoiceAll(
	tools []agentruntime.ToolSchema,
	capabilities []string,
	exhausted map[string]bool,
) any {
	candidates := make([]string, 0, 1)
	for _, schema := range tools {
		name := strings.TrimSpace(schema.Name)
		if name == "" || exhausted[normalizeAgentToolName(name)] ||
			exhausted[strings.ToLower(name)] ||
			effectiveAgentRuntimeToolExposure(schema) == agentruntime.ToolExposureHidden {
			continue
		}
		matched := true
		for _, capability := range capabilities {
			if !agentRuntimeToolSchemaHasCapability(schema, capability) {
				matched = false
				break
			}
		}
		if matched {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) != 1 {
		return nil
	}
	return map[string]any{"type": "tool", "name": candidates[0]}
}

func runnerCorrectionHasReturnedSourceURL(messages []agentruntime.Message) bool {
	boundary := runnerCorrectionToolBoundaryIndex(messages)
	if boundary < 0 {
		return false
	}
	for _, message := range messages[boundary+1:] {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil ||
			agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		if runnerCorrectionValueHasSourceURL(result, 0, new(int)) {
			return true
		}
	}
	return false
}

func runnerCorrectionValueHasSourceURL(value any, depth int, visited *int) bool {
	if depth > 8 || visited == nil || *visited >= 512 {
		return false
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizeAgentToolName(key)
			if normalized == "url" || normalized == "sourceurl" {
				candidate := strings.TrimSpace(stringValue(child))
				if strings.HasPrefix(candidate, "https://") || strings.HasPrefix(candidate, "http://") {
					return true
				}
			}
			if runnerCorrectionValueHasSourceURL(child, depth+1, visited) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if runnerCorrectionValueHasSourceURL(child, depth+1, visited) {
				return true
			}
		}
	}
	return false
}

func runnerCorrectionPreviousWindowMissingSkill(messages []agentruntime.Message) bool {
	currentBoundary := runnerCorrectionToolBoundaryIndex(messages)
	if currentBoundary <= 0 {
		return false
	}
	previousBoundary := -1
	for index := currentBoundary - 1; index >= 0; index-- {
		if strings.Contains(messages[index].Content, sessionRunnerDurableCorrectionContextMarker) {
			previousBoundary = index
			break
		}
	}
	calls := runnerToolCallsByCallID(messages[previousBoundary+1 : currentBoundary])
	for _, message := range messages[previousBoundary+1 : currentBoundary] {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "skill" {
			continue
		}
		var result map[string]any
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			continue
		}
		if nested := mapValue(result["result"]); len(nested) > 0 {
			result = nested
		}
		if strings.EqualFold(strings.TrimSpace(stringValue(result["status"])), "skill_not_found") {
			return true
		}
	}
	return false
}

// runnerCorrectionPreviouslyExhaustedSourceTools carries only a truthful,
// no-progress source observation across validator correction boundaries. A
// later correction may still use a source route when it produced a usable
// record; an unavailable or complete-empty route is not replayed merely
// because the validator opened a new repair window.
func runnerCorrectionPreviouslyExhaustedSourceTools(
	messages []agentruntime.Message,
	class string,
) map[string]bool {
	exhausted := make(map[string]bool)
	boundary := runnerCorrectionToolBoundaryIndex(messages)
	if boundary <= 0 {
		return exhausted
	}
	window := messages[:boundary]
	calls := runnerToolCallsByCallID(window)
	for _, message := range window {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		normalized := normalizeAgentToolName(call.Name)
		// Generic web routes are source locators shared by every evidence class.
		// A successful article read in an earlier repair scope says nothing about
		// whether a later patent, trial, or publication locator has been tried.
		// Carry only source-class-specific exhaustion here. Within one correction
		// boundary the normal bounded route tracker still prevents repetition.
		if runnerCorrectionSourceAttemptExhausted(call, result) && !runnerCorrectionSourceCallUsable(call, result) &&
			runnerCorrectionSourceClassForTool(call.Name) == class {
			exhausted[normalized] = true
		}
	}
	return exhausted
}

func runnerCorrectionSourceClassForTool(name string) string {
	if class, _ := runnerCorrectionMCPToolClass(name); class != "" {
		return class
	}
	switch normalizeAgentToolName(name) {
	case "patentsearch":
		return "patent"
	case "fetcharticlefulltext":
		return "publication"
	case "websearch", "webfetch", "webresearch":
		return "web"
	default:
		return ""
	}
}

func runnerCorrectionMCPToolClass(name string) (class string, exact bool) {
	parts := strings.Split(strings.TrimSpace(name), "__")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "mcp") {
		return "", false
	}
	method := strings.Join(parts[2:], "_")
	return runnerCorrectionMCPMethodClass(parts[1], method)
}

func runnerCorrectionHasLoadedSourceConnector(run *sessionRunnerChatRun) bool {
	if run == nil {
		return false
	}
	for _, name := range run.executedSkillNamesSnapshot() {
		if strings.HasPrefix(normalizeRuntimeSkillDependencyName(name), "mcp-") {
			return true
		}
	}
	return false
}

func runnerCorrectionExhaustedSourceToolsSinceBoundary(
	messages []agentruntime.Message,
	class string,
) map[string]bool {
	exhausted := make(map[string]bool)
	boundary := runnerCorrectionToolBoundaryIndex(messages)
	if boundary < 0 {
		return exhausted
	}
	calls := runnerToolCallsByCallID(messages[boundary+1:])
	for _, message := range messages[boundary+1:] {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			continue
		}
		normalized := normalizeAgentToolName(call.Name)
		resultObject := runnerCorrectionResultObject(result)
		nonExecutingPreflight := agentruntime.IsNonExecutingPreflight(result) ||
			(resultObject != nil && agentruntime.IsNonExecutingPreflight(resultObject))
		if normalized == "skill" && !nonExecutingPreflight &&
			agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			// A newly materialized Skill is a real contract revision. It may expose
			// the exact connector schema that the previous REPL call lacked, so one
			// new REPL selection is valid without erasing any executed source facts.
			delete(exhausted, normalizeAgentToolName("repl"))
			continue
		}
		if normalized == "repl" && nonExecutingPreflight {
			// Any pre-execution rejection proves that this transport cannot run
			// under the current loaded contract. It is not an execution attempt or
			// evidence, but selecting it again without a Skill revision creates the
			// exact no-progress loop this route ledger is meant to prevent.
			exhausted[normalized] = true
			continue
		}
		if runnerCorrectionSourceAttemptExhausted(call, result) &&
			!runnerCorrectionSourceCallUsable(call, result) {
			exhausted[normalized] = true
			continue
		}
		if normalized == "repl" && !nonExecutingPreflight &&
			agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
			// The connector transport actually ran and ended without a usable
			// record. Preserve that execution outcome, then move to another source
			// route instead of inviting argument variations against the same broken
			// transport.
			exhausted[normalized] = true
			continue
		}
		if normalized == "repl" && !nonExecutingPreflight &&
			agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			// REPL is exposed here solely as the transport for the connector record
			// named by the current repair. A successful arbitrary calculation or
			// file rewrite is not source evidence and must not keep REPL selected.
			// An exact MCP detail read that remains insufficient is exhausted too;
			// a substantive record removes the pending class before this selector
			// is consulted again.
			mcpClass := runnerCorrectionMCPSourceClass(call, result)
			exactClass := runnerCorrectionExactMCPRecordClass(call, result)
			if mcpClass == "" || mcpClass != class || exactClass == class {
				exhausted[normalized] = true
			}
		}
		if normalized == "webfetch" && agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			exhausted[normalized] = true
		}
		if normalized == "websearch" && agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			exhausted[normalized] = true
		}
		if normalized == "webresearch" && !agentruntime.IsNonExecutingPreflight(result) {
			// One bounded research call may inspect many ranked pages. Repeating
			// the identical transport inside the same validator correction adds
			// no authoritative state and can loop indefinitely when the remaining
			// action is to update the affected ledger row. A later validator
			// correction is a new boundary and may legitimately research again.
			exhausted[normalized] = true
		}
	}
	return exhausted
}

func runnerCorrectionExactMCPRecordClass(call agentruntime.ToolCall, result any) string {
	class, exact := runnerCorrectionMCPSourceCall(call, result)
	if !exact {
		return ""
	}
	return class
}

func runnerCorrectionMCPSourceClass(call agentruntime.ToolCall, result any) string {
	class, _ := runnerCorrectionMCPSourceCall(call, result)
	return class
}

func runnerCorrectionMCPSourceCall(call agentruntime.ToolCall, result any) (class string, exact bool) {
	if normalizeAgentToolName(call.Name) != "repl" ||
		agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return "", false
	}
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil {
		return "", false
	}
	calls := parseLiteralMCPCalls(stringValue(input["code"]))
	if len(calls) != 1 {
		return "", false
	}
	return runnerCorrectionMCPMethodClass(calls[0].server, calls[0].method)
}

func runnerCorrectionMCPMethodClass(serverName, methodName string) (class string, exact bool) {
	method := normalizeAgentToolName(methodName)
	server := normalizeAgentToolName(serverName)
	switch {
	case strings.Contains(server, "clinicaltrial"):
		return "trial", strings.Contains(method, "gettrialdetails")
	case strings.Contains(server, "literature") || strings.Contains(server, "pubmed") || strings.Contains(server, "openalex"):
		return "publication", strings.Contains(method, "getwork") || strings.Contains(method, "metadata") ||
			strings.Contains(method, "fulltext") || strings.Contains(method, "lookuparticle")
	case strings.Contains(server, "patent"):
		return "patent", strings.Contains(method, "getpatent") || strings.Contains(method, "lookup") ||
			strings.Contains(method, "details")
	default:
		return "", false
	}
}

func runnerSourceRepairMutationState(messages []agentruntime.Message) (edited, saved bool) {
	boundary := -1
	for index, message := range messages {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			boundary = index
		}
	}
	if boundary < 0 {
		return false, false
	}
	window := messages[boundary+1:]
	calls := runnerToolCallsByCallID(window)
	for _, message := range window {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) ||
			agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found {
			continue
		}
		switch normalizeAgentToolName(call.Name) {
		case "edit", "editfile", "filepatch", "filereplace", "patch", "write", "filewrite":
			if runnerCorrectionResultMadeMutation(result) {
				edited = true
			}
		case "saveartifacts", "artifactregister":
			if edited && !runnerCorrectionResultUnchanged(result) {
				saved = true
			}
		}
	}
	return edited, saved
}

func runnerUnsupportedSourceRemovalSavedSinceCorrection(messages []agentruntime.Message) bool {
	boundary := -1
	for index, message := range messages {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			boundary = index
		}
	}
	if boundary < 0 {
		return false
	}
	window := messages[boundary+1:]
	calls := runnerToolCallsByCallID(window)
	sourceAttempted, edited := false, false
	for _, message := range window {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found {
			continue
		}
		normalized := normalizeAgentToolName(call.Name)
		if runnerCorrectionSourceCallUsable(call, result) ||
			runnerCorrectionSourceAttemptExhausted(call, result) ||
			runnerCorrectionIsSuccessfulMCPRepl(call, result) {
			sourceAttempted = true
			continue
		}
		if !sourceAttempted || agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
			continue
		}
		switch normalized {
		case "edit", "editfile", "filepatch", "filereplace", "patch", "write", "filewrite":
			if runnerCorrectionResultMadeMutation(result) {
				edited = true
			}
		case "saveartifacts", "artifactregister":
			if edited && !runnerCorrectionResultUnchanged(result) {
				return true
			}
		}
	}
	return false
}

func runnerCorrectionSourceCallUsable(call agentruntime.ToolCall, result any) bool {
	normalized := normalizeAgentToolName(call.Name)
	if normalized == "webresearch" {
		object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
		if object == nil {
			return false
		}
		quality := mapValue(object["quality"])
		return boolValue(quality["meetsTarget"], false) &&
			int(numberValue(quality["deepReadSources"])) > 0 && len(anySliceValue(object["documents"])) > 0
	}
	if normalized == "webfetch" {
		object := runnerCorrectionResultObject(result)
		if object == nil || boolValue(object["partial"], false) || boolValue(object["truncated"], false) ||
			boolValue(object["binary"], false) || boolValue(object["sourceUnavailable"], false) {
			return false
		}
		return len([]rune(strings.TrimSpace(firstNonEmpty(
			stringValue(object["body"]), stringValue(object["content"]), stringValue(object["text"]),
		)))) >= webResearchMinimumSubstantiveCharacters
	}
	if normalized == "fetcharticlefulltext" {
		object := runnerCorrectionResultObject(result)
		if object == nil {
			return false
		}
		if available, recorded := object["available"].(bool); recorded && !available &&
			!boolValue(object["recordAvailable"], false) {
			return false
		}
		return runnerEvidenceResultHasSubstantiveField(object, runnerPublicationSubstantiveFields, 0, new(int))
	}
	if normalized != "patentsearch" {
		if !sessionRunnerEvidenceTool(call.Name) {
			return false
		}
		object := runnerCorrectionResultObject(result)
		if object == nil {
			return false
		}
		stage := strings.ToLower(strings.TrimSpace(stringValue(mapValue(object["evidence"])["stage"])))
		if stage == "discovery" || stage == "derived_summary" {
			return false
		}
		if stage == "structured_record_read" || stage == "full_text_read" {
			return runnerCorrectionSourceResultUsable(result)
		}
		return runnerEvidenceResultHasSubstantiveField(object, runnerPublicationSubstantiveFields, 0, new(int)) ||
			runnerEvidenceResultHasSubstantiveField(object, runnerTrialSubstantiveFields, 0, new(int))
	}
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil ||
		!strings.EqualFold(strings.TrimSpace(stringValue(input["operation"])), "lookup") {
		return false
	}
	object, ok := result.(map[string]any)
	if !ok {
		return false
	}
	if nested, nestedOK := object["result"].(map[string]any); nestedOK {
		object = nested
	}
	return strings.EqualFold(strings.TrimSpace(stringValue(object["evidenceDepth"])), "full_record") &&
		runnerPatentLookupContainsFullRecord(object) && runnerCorrectionSourceResultUsable(result)
}

// runnerCorrectionSourceAttemptExhausted admits a truthful unsupported-row
// removal after a source-specific detail route completed but could not provide
// the requested record depth. Discovery searches do not qualify: a title list
// is neither a record read nor evidence that the record is unavailable.
func runnerCorrectionSourceAttemptExhausted(call agentruntime.ToolCall, result any) bool {
	normalized := normalizeAgentToolName(call.Name)
	object := runnerCorrectionResultObject(result)
	if object == nil {
		return false
	}
	switch normalized {
	case "patentsearch":
		var input map[string]any
		if json.Unmarshal(call.Arguments, &input) != nil {
			return false
		}
		operation := strings.ToLower(strings.TrimSpace(stringValue(input["operation"])))
		if operation == "lookup" {
			return true
		}
		// A completed discovery search with no candidates is an exhausted
		// source route for this bounded correction. Keeping it selectable
		// would replay the same empty search forever instead of advancing to
		// the next governed evidence path (Skill, web, or MCP). Non-empty
		// discovery remains selectable so the model can follow the tool's
		// normal search-to-record transition.
		if operation != "search" {
			return false
		}
		retrieval := mapValue(object["retrieval"])
		return boolValue(retrieval["complete"], false) &&
			len(anySliceValue(object["records"])) == 0
	case "websearch":
		// A completed generic search is one bounded fallback attempt. Its
		// records may inform the report, but it cannot stand in for a missing
		// source-class record and should not be replayed forever.
		return agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded
	case "webfetch":
		// A terminal fetch that returns an access challenge, a non-2xx response,
		// or an otherwise unusable page is still a truthful completed attempt. It
		// may justify removing the unsupported row, but never counts as evidence.
		return agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded
	case "webresearch":
		// Bounded research may externalize an unavailable or partial outcome. Once
		// execution occurred, recovery should move to another route or remove the
		// unsupported claim instead of replaying the same research call.
		return !agentruntime.IsNonExecutingPreflight(result)
	case "fetcharticlefulltext":
		_, recorded := object["available"].(bool)
		return recorded
	default:
		if !sessionRunnerEvidenceTool(call.Name) {
			return false
		}
		stage := strings.ToLower(strings.TrimSpace(stringValue(mapValue(object["evidence"])["stage"])))
		return stage == "structured_record_read" || stage == "full_text_read"
	}
}

func runnerCorrectionIsSuccessfulMCPRepl(call agentruntime.ToolCall, result any) bool {
	if normalizeAgentToolName(call.Name) != "repl" ||
		!strings.Contains(strings.ToLower(string(call.Arguments)), "host.mcp") ||
		agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return false
	}
	argumentsLower := strings.ToLower(string(call.Arguments))
	if strings.Contains(argumentsLower, "host.mcp.search") || strings.Contains(argumentsLower, "host.mcp.collect") ||
		!strings.Contains(argumentsLower, ".evidence") {
		return false
	}
	// host.mcp calls are admitted and durably attested by the server-side
	// connector gateway. Their full records intentionally remain outside the
	// outer REPL envelope, which contains only stdout and materialized files.
	// A clean REPL completion therefore proves the governed source call ran;
	// completion validation still checks that the published row carries a real
	// identifier and locator from that evidence.
	object, ok := result.(map[string]any)
	if !ok {
		return false
	}
	if nested, nestedOK := object["result"].(map[string]any); nestedOK {
		object = nested
	}
	return object["ok"] == true || strings.EqualFold(strings.TrimSpace(stringValue(object["exit_status"])), "ok")
}

func runnerCorrectionResultUnchanged(result any) bool {
	return !runnerCorrectionResultMadeMutation(result)
}

func runnerCorrectionResultObject(result any) map[string]any {
	object, ok := result.(map[string]any)
	if !ok {
		return nil
	}
	for depth := 0; depth < 4; depth++ {
		nested, found := object["result"]
		if !found {
			return object
		}
		next, ok := nested.(map[string]any)
		if !ok {
			return object
		}
		object = next
	}
	return object
}

func mustMarshalRunnerCorrectionResult(result any) string {
	raw, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(raw)
}

func runnerCorrectionSourceResultUsable(result any) bool {
	object, ok := result.(map[string]any)
	if !ok {
		return false
	}
	if nested, nestedOK := object["result"].(map[string]any); nestedOK {
		object = nested
	}
	if value, found := object["available"].(bool); found && !value {
		return false
	}
	if value, found := object["found"].(bool); found && !value {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(object["status"])))
	switch status {
	case "not_available", "not_found", "empty", "failed", "error", "unavailable":
		return false
	}
	for _, key := range []string{"records", "sources", "results", "hits", "items", "documents", "publications", "patents", "datasets"} {
		if values, found := object[key].([]any); found && len(values) > 0 {
			return true
		}
	}
	for _, key := range []string{"body", "content", "text", "full_text", "fullText", "abstract"} {
		if strings.TrimSpace(stringValue(object[key])) != "" {
			return true
		}
	}
	if available, found := object["available"].(bool); found && available {
		return true
	}
	// Structured MCP/detail responses may be a single record rather than a
	// list. Require both an identity/locator and substantive metadata so an
	// echoed query or an empty 404 envelope cannot satisfy the repair.
	identity := false
	for _, key := range []string{"url", "source_url", "sourceUrl", "doi", "pmid", "pmcid", "patent_id", "patentId", "nct_id", "nctId", "accession", "id"} {
		if strings.TrimSpace(stringValue(object[key])) != "" {
			identity = true
			break
		}
	}
	if !identity {
		return false
	}
	for _, key := range []string{"title", "authors", "year", "publication_date", "publicationDate", "summary", "description", "metadata"} {
		if value, found := object[key]; found && value != nil && strings.TrimSpace(stringValue(value)) != "" {
			return true
		}
	}
	return false
}

func runnerSuccessfulToolNamesSinceCorrection(messages []agentruntime.Message) map[string]bool {
	boundary := runnerCorrectionToolBoundaryIndex(messages)
	if boundary < 0 {
		return map[string]bool{}
	}
	window := messages[boundary+1:]
	calls := runnerToolCallsByCallID(window)
	succeeded := make(map[string]bool)
	for _, message := range window {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		call := calls[message.ToolCallID]
		if runnerCorrectionReadCall(call) {
			continue
		}
		toolName := strings.ToLower(strings.TrimSpace(call.Name))
		if agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded ||
			runnerCorrectionToolResultProvidesDownloadHandoff(toolName, result) {
			succeeded[toolName] = true
		}
	}
	return succeeded
}

// runnerCorrectionToolBoundaryIndex identifies the beginning of the current
// bounded repair window. Durable stop-hook corrections carry an explicit
// context marker. Draft artifact validation happens inline, before a new
// execution unit exists, so its save receipt is the equivalent boundary. Both
// paths must share one tool-attempt ledger; otherwise an unavailable source can
// be selected repeatedly until the no-progress guard terminates the task.
func runnerCorrectionToolBoundaryIndex(messages []agentruntime.Message) int {
	boundary := -1
	calls := runnerToolCallsByCallID(messages)
	for index, message := range messages {
		if strings.Contains(message.Content, sessionRunnerDurableCorrectionContextMarker) {
			boundary = index
			continue
		}
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "saveartifacts" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			continue
		}
		object, ok := result.(map[string]any)
		if !ok || !runnerArtifactSaveRequiresCorrection(object) {
			continue
		}
		unsupported, _, _ := artifactSaveEvidenceRepairReferences(object)
		if len(unsupported) > 0 {
			boundary = index
		}
	}
	return boundary
}

// runnerCorrectionToolResultProvidesDownloadHandoff recognizes the bounded
// inspect-to-transfer boundary shared by every public binary/scientific file.
// web_fetch intentionally returns a partial outcome because it omits binary
// bytes from model context; that partial result is nevertheless a completed
// source-discovery step when it carries an exact URL and the explicit
// dedicated-download recovery contract. It must advance tool choice to the
// downloader without satisfying the final validated-download requirement.
func runnerCorrectionToolResultProvidesDownloadHandoff(toolName string, result any) bool {
	if !strings.EqualFold(strings.TrimSpace(toolName), "web_fetch") {
		return false
	}
	object, ok := result.(map[string]any)
	if !ok {
		return false
	}
	if nested, nestedOK := object["result"].(map[string]any); nestedOK {
		object = nested
	}
	if object["binary"] != true ||
		strings.TrimSpace(stringValue(object["url"])) == "" ||
		!strings.EqualFold(strings.TrimSpace(stringValue(object["recovery"])), "use_dedicated_download_or_fulltext_tool") {
		return false
	}
	statusCode := 0
	switch value := object["statusCode"].(type) {
	case float64:
		statusCode = int(value)
	case int:
		statusCode = value
	case int64:
		statusCode = int(value)
	}
	return statusCode >= 200 && statusCode < 300
}

func runnerToolNamesByCallID(messages []agentruntime.Message) map[string]string {
	names := make(map[string]string)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				names[id] = strings.TrimSpace(call.Name)
			}
		}
	}
	return names
}

func runnerToolCallsByCallID(messages []agentruntime.Message) map[string]agentruntime.ToolCall {
	calls := make(map[string]agentruntime.ToolCall)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				calls[id] = call
			}
		}
	}
	return calls
}

// sessionRunnerCorrectionToolSchemas keeps user clarification available for
// genuine user-owned choices, but removes it from correction turns whose
// immutable validator finding explicitly assigns the repair to the agent. In
// those turns AskUser cannot change the failed artifact bytes and previously
// consumed a required tool round without advancing the correction.
func sessionRunnerCorrectionToolSchemas(
	tools []agentruntime.ToolSchema,
	reason, detail string,
) []agentruntime.ToolSchema {
	if !runnerCorrectionIsAgentOwnedArtifactRepair(reason, detail) &&
		!runnerCorrectionRequiresSourceLocatorRepair(reason, detail) &&
		!runnerCorrectionRequiresValidatedPublicDownload(reason, detail) {
		return tools
	}
	filtered := make([]agentruntime.ToolSchema, 0, len(tools))
	for _, tool := range tools {
		if canonical, ok := toolcontract.CanonicalAskUser(tool.Name); ok && canonical == toolcontract.AskUser {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

func runnerCorrectionRequiresSourceLocatorRepair(reason, detail string) bool {
	return strings.TrimSpace(reason) == "artifact_reference_correction_required" &&
		(strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "evidence_source_locator_missing:") ||
			strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "evidence_record_depth_missing:") ||
			strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "evidence_record_depth_required:") ||
			strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "evidence_record_class_missing:"))
}

func runnerCorrectionRequiresValidatedPublicDownload(reason, detail string) bool {
	return strings.TrimSpace(reason) == sessionRunnerRealScientificEvidenceRequiredReasonCode &&
		strings.Contains(strings.ToLower(strings.TrimSpace(detail)), "no validated public scientific download receipt")
}

func runnerCorrectionIsAgentOwnedArtifactRepair(reason, detail string) bool {
	if strings.TrimSpace(reason) != "artifact_reference_correction_required" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(detail))
	return strings.Contains(lower, "machine_validation_") ||
		strings.Contains(lower, "malformed_artifact_") ||
		strings.Contains(lower, "unresolved_template_marker:") ||
		strings.Contains(lower, sessionRunnerArtifactPublicationStaleMarker)
}

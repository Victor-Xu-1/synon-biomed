package server

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"synon-go/internal/agentruntime"
)

const runnerArtifactRepairContractSchema = "synon.artifact-repair.v1"

var runnerMissingSourceLocatorPattern = regexp.MustCompile(
	`(?i)evidence_source_locator_missing:(.+?)\s+row=([1-9][0-9]*)\s+source_type=([^,;\r\n]+)`,
)

// runnerArtifactRepairRequirements converts validator diagnostics into a
// complete machine-readable repair contract. The validator remains the sole
// authority for deciding whether bytes pass; this projection only tells the
// outer agent which semantic state must change and prevents formatting-only
// mutations from being mistaken for a repair.
func runnerArtifactRepairRequirements(detail string) []map[string]any {
	matches := runnerMissingSourceLocatorPattern.FindAllStringSubmatch(strings.TrimSpace(detail), -1)
	if len(matches) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) != 4 {
			continue
		}
		path := strings.TrimSpace(match[1])
		sourceType := strings.TrimSpace(match[3])
		row, err := strconv.Atoi(match[2])
		// Coordinates here describe a finding, not a read/mutation grant. Its
		// later file operation validates actual scope and resources. Preview
		// limits must not silently erase a valid target from repair authority.
		if err != nil || row <= 0 || path == "" || sourceType == "" {
			continue
		}
		key := path + "\x00" + strconv.Itoa(row) + "\x00" + strings.ToLower(sourceType)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, map[string]any{
			"schema":                     runnerArtifactRepairContractSchema,
			"code":                       "evidence_source_locator_missing",
			"artifact_path":              path,
			"row_number":                 row,
			"source_type":                sourceType,
			"required_state_change":      "add_attested_stable_locator_or_remove_unsupported_row",
			"semantic_change_required":   true,
			"formatting_only_satisfies":  false,
			"unchanged_resave_satisfies": false,
			"accepted_locator_kinds": []string{
				"url", "doi", "pmid", "pmcid", "clinical_trial_id", "accession", "regulatory_application_id",
			},
		})
	}
	sort.Slice(result, func(left, right int) bool {
		leftPath, rightPath := stringValue(result[left]["artifact_path"]), stringValue(result[right]["artifact_path"])
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return numberValue(result[left]["row_number"]) < numberValue(result[right]["row_number"])
	})
	return result
}

func runnerCorrectionRequiresMissingSourceLocator(reason, detail string) bool {
	return strings.TrimSpace(reason) == "artifact_reference_correction_required" &&
		len(runnerArtifactRepairRequirements(detail)) > 0
}

// runnerSourceLocatorRepairRequiredToolChoice keeps a missing-locator repair
// on the existing authoritative outer loop. It sequences atomic capabilities,
// while the model continues to choose the query, source, locator and exact
// artifact mutation. If a source route is unavailable or returns nothing, the
// sequence advances to artifact editing so an unsupported row can be removed.
func runnerSourceLocatorRepairRequiredToolChoice(
	run *sessionRunnerChatRun,
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
) (any, bool) {
	if run == nil || !runnerCorrectionRequiresMissingSourceLocator(run.CorrectionReason, run.CorrectionDetail) {
		return nil, false
	}
	if runnerCorrectionToolBoundaryIndex(messages) < 0 {
		return nil, true
	}
	if !runnerCorrectionCapabilityAttemptedSinceBoundary(messages, tools, "artifact-read") {
		if runnerCorrectionCapabilityAvailable(tools, "artifact-read") {
			return runnerCorrectionCapabilityToolChoice(tools, "artifact-read", nil), true
		}
	}
	if !runnerCorrectionCapabilityAttemptedSinceBoundary(messages, tools, "source-discovery") {
		if runnerCorrectionCapabilityAvailable(tools, "source-discovery") {
			return runnerCorrectionCapabilityToolChoice(tools, "source-discovery", nil), true
		}
	}
	if runnerCorrectionHasReturnedSourceURL(messages) &&
		!runnerCorrectionCapabilityAttemptedSinceBoundary(messages, tools, "source-locator-read") &&
		runnerCorrectionCapabilityAvailable(tools, "source-locator-read") {
		return runnerCorrectionCapabilityToolChoice(tools, "source-locator-read", nil), true
	}
	edited, saved := runnerSourceRepairMutationState(messages)
	if !edited && runnerCorrectionCapabilityAvailable(tools, "artifact-edit") {
		return runnerCorrectionCapabilityToolChoice(tools, "artifact-edit", nil), true
	}
	if edited && !saved && runnerCorrectionCapabilityAvailable(tools, "artifact-publication") {
		return runnerCorrectionCapabilityToolChoice(tools, "artifact-publication", nil), true
	}
	return nil, true
}

func runnerCorrectionCapabilityAvailable(tools []agentruntime.ToolSchema, capability string) bool {
	for _, tool := range tools {
		if effectiveAgentRuntimeToolExposure(tool) != agentruntime.ToolExposureHidden &&
			agentRuntimeToolSchemaHasCapability(tool, capability) {
			return true
		}
	}
	return false
}

func runnerCorrectionCapabilityAttemptedSinceBoundary(
	messages []agentruntime.Message,
	tools []agentruntime.ToolSchema,
	capability string,
) bool {
	boundary := runnerCorrectionToolBoundaryIndex(messages)
	if boundary < 0 {
		return false
	}
	window := messages[boundary+1:]
	calls := runnerToolCallsByCallID(window)
	schemas := make(map[string]agentruntime.ToolSchema, len(tools))
	for _, tool := range tools {
		schemas[normalizeAgentToolName(tool.Name)] = tool
	}
	for _, message := range window {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || runnerCorrectionReadCall(call) {
			continue
		}
		schema, found := schemas[normalizeAgentToolName(call.Name)]
		if !found || !agentRuntimeToolSchemaHasCapability(schema, capability) {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			return true
		}
		if !agentruntime.ToolResultDidNotExecute(result) {
			return true
		}
	}
	return false
}

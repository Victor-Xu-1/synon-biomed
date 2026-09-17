package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/sciencecapability"
)

type answeredAskUserContinuation struct {
	eventID           int64
	status            string
	provenance        string
	parameterEvidence map[string]string
	implementations   map[string]string
	evidenceResolvers map[string]transcriptstore.AskUserEvidenceResolverSelection
}

func answeredAskUserContinuationsFromRunnerEntries(entries []eventjournal.Entry) []answeredAskUserContinuation {
	seenCalls := map[string]struct{}{}
	result := make([]answeredAskUserContinuation, 0)
	for _, entry := range entries {
		if !runnerEntryIsInputResponse(entry) {
			continue
		}
		for lineIndex, line := range strings.Split(runnerModelMessageText(entry.Message), "\n") {
			start := strings.Index(line, "{")
			if start < 0 {
				continue
			}
			callID := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[:start]), ":"))
			if callID == "" || strings.EqualFold(callID, "Resolved input requests") {
				callID = fmt.Sprintf("event-%d-line-%d", entry.EventID, lineIndex)
			}
			if _, duplicate := seenCalls[callID]; duplicate {
				continue
			}
			var continuation struct {
				Status            string                                                      `json:"status"`
				Provenance        string                                                      `json:"provenance"`
				ParameterEvidence map[string]string                                           `json:"parameter_evidence"`
				Implementations   map[string]string                                           `json:"implementations"`
				EvidenceResolvers map[string]transcriptstore.AskUserEvidenceResolverSelection `json:"evidence_resolvers"`
			}
			if json.Unmarshal([]byte(line[start:]), &continuation) != nil ||
				(continuation.Status != "answered" && continuation.Status != "delegated") {
				continue
			}
			if continuation.Status == "delegated" && continuation.Provenance != "model-delegated-choice" {
				continue
			}
			seenCalls[callID] = struct{}{}
			result = append(result, answeredAskUserContinuation{
				eventID: entry.EventID, status: continuation.Status, provenance: continuation.Provenance,
				parameterEvidence: continuation.ParameterEvidence,
				implementations:   continuation.Implementations,
				evidenceResolvers: continuation.EvidenceResolvers,
			})
		}
	}
	return result
}

func selectedAskUserEvidenceResolversFromRunnerEntries(entries []eventjournal.Entry) []sciencecapability.ExecutionEvidenceResolver {
	selected := map[string]sciencecapability.ExecutionEvidenceResolver{}
	for _, continuation := range answeredAskUserContinuationsFromRunnerEntries(entries) {
		if continuation.status != "answered" {
			continue
		}
		for _, resolver := range continuation.evidenceResolvers {
			group := strings.TrimSpace(resolver.EvidenceGroup)
			skill := strings.TrimSpace(resolver.Skill)
			implementation := strings.TrimSpace(resolver.Implementation)
			if group != "" && skill != "" && implementation != "" {
				selected[strings.ToLower(group)] = sciencecapability.ExecutionEvidenceResolver{
					EvidenceGroup: group, Skill: skill, Implementation: implementation,
				}
			}
		}
	}
	groups := make([]string, 0, len(selected))
	for group := range selected {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	result := make([]sciencecapability.ExecutionEvidenceResolver, 0, len(groups))
	for _, group := range groups {
		result = append(result, selected[group])
	}
	return result
}

// selectedAskUserImplementationsFromRunnerEntries recovers the latest exact
// user-owned implementation identity from durable input-response messages.
// Input-response messages contain the cumulative set of resolved AskUser calls,
// so simply unioning every implementation makes retired choices remain active
// forever. Track each AskUser call identity once and replace the active set only
// when a new answered call appears. Historical answers remain in the journal as
// audit evidence but no longer authorize a later environment mutation.
func selectedAskUserImplementationsFromRunnerEntries(entries []eventjournal.Entry) []string {
	continuations := answeredAskUserContinuationsFromRunnerEntries(entries)
	resolverImplementations := make([]string, 0)
	for _, continuation := range continuations {
		for _, resolver := range continuation.evidenceResolvers {
			if implementation := strings.TrimSpace(resolver.Implementation); implementation != "" {
				resolverImplementations = append(resolverImplementations, implementation)
			}
		}
	}
	resolverImplementations = uniqueSortedFolded(resolverImplementations)
	selected := []string(nil)
	currentEventID := int64(-1)
	answeredInEvent := make([]string, 0, 1)
	flush := func() {
		if len(answeredInEvent) > 0 {
			selected = uniqueSortedFolded(answeredInEvent)
		}
		answeredInEvent = nil
	}
	for _, continuation := range continuations {
		implementations := make([]string, 0, len(continuation.implementations))
		for _, implementation := range continuation.implementations {
			if implementation = strings.TrimSpace(implementation); implementation != "" {
				isAuxiliaryResolver := false
				for _, resolverImplementation := range resolverImplementations {
					if taskImplementationMatchesRegistered(implementation, resolverImplementation) {
						isAuxiliaryResolver = true
						break
					}
				}
				if isAuxiliaryResolver {
					continue
				}
				implementations = append(implementations, implementation)
			}
		}
		// An AskUser continuation can resolve an auxiliary evidence source or
		// another task parameter without selecting a new primary implementation.
		// Such a decision must not retire the earlier registry-owned primary
		// implementation merely because its event ID is newer.
		if len(implementations) == 0 {
			continue
		}
		if currentEventID != -1 && continuation.eventID != currentEventID {
			flush()
		}
		currentEventID = continuation.eventID
		answeredInEvent = append(answeredInEvent, implementations...)
	}
	flush()
	if eventID, registered, found := registrySelectedImplementationsFromRunnerEntries(entries); found && eventID >= currentEventID {
		selected = registered
	}
	return selected
}

func registrySelectedImplementationsFromRunnerEntries(entries []eventjournal.Entry) (int64, []string, bool) {
	latestEventID := int64(-1)
	var selected []string
	for _, entry := range entries {
		message := entry.Message
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" ||
			strings.TrimSpace(stringValue(message["status"])) != "completed" ||
			strings.TrimSpace(stringValue(message["toolPhase"])) != "completed" {
			continue
		}
		toolResult, ok := softwareRuntimeObjectReceipt(message["toolResult"])
		if !ok || agentruntime.ClassifyToolResult(toolResult) != agentruntime.ToolResultSucceeded {
			continue
		}
		receipt, ok := softwareRuntimeObjectReceipt(toolResult["implementation_selection"])
		if !ok || stringValue(receipt["provenance"]) != "registry-unique-local-pack" {
			continue
		}
		implementations := uniqueSortedFolded(stringArrayValue(receipt["implementations"]))
		if len(implementations) != 1 || strings.TrimSpace(implementations[0]) == "" {
			continue
		}
		if entry.EventID >= latestEventID {
			latestEventID = entry.EventID
			selected = implementations
		}
	}
	return latestEventID, selected, latestEventID >= 0
}

func resolvedAskUserEvidenceFromRunnerEntries(entries []eventjournal.Entry) map[string]string {
	evidence := map[string]string{}
	for _, continuation := range answeredAskUserContinuationsFromRunnerEntries(entries) {
		if continuation.status != "answered" {
			continue
		}
		for question, value := range continuation.parameterEvidence {
			question, value = strings.TrimSpace(question), strings.TrimSpace(value)
			if question != "" && value != "" {
				evidence[question] = value
			}
		}
	}
	return evidence
}

func implementationSelectionRequiredFromRunnerEntries(entries []eventjournal.Entry) bool {
	required := false
	for _, entry := range entries {
		if runnerEntryIsInputResponse(entry) && len(selectedAskUserImplementationsFromRunnerEntries([]eventjournal.Entry{entry})) > 0 {
			required = false
			continue
		}
		if strings.TrimSpace(stringValue(entry.Message["type"])) != "runner_checkpoint" {
			continue
		}
		status := strings.TrimSpace(stringValue(mapValue(entry.Message["toolResult"])["status"]))
		switch status {
		case "implementation_identity_required", "implementation_selection_required", "selected_implementation_mismatch":
			required = true
		}
	}
	return required
}

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
	type groupSelection struct {
		eventID int64
		choices map[string]sciencecapability.ExecutionEvidenceResolver
	}
	selected := map[string]groupSelection{}
	_, scopeEventID := askUserPrimarySelectionScopeFromRunnerEntries(entries)
	for _, continuation := range answeredAskUserContinuationsFromRunnerEntries(entries) {
		if continuation.status != "answered" || continuation.eventID < scopeEventID {
			continue
		}
		for _, resolver := range continuation.evidenceResolvers {
			group := strings.TrimSpace(resolver.EvidenceGroup)
			skill := strings.TrimSpace(resolver.Skill)
			implementation := strings.TrimSpace(resolver.Implementation)
			if group != "" && skill != "" && implementation != "" {
				groupKey := strings.ToLower(group)
				current, found := selected[groupKey]
				if found && continuation.eventID < current.eventID {
					continue
				}
				if !found || continuation.eventID > current.eventID {
					current = groupSelection{eventID: continuation.eventID, choices: map[string]sciencecapability.ExecutionEvidenceResolver{}}
				}
				// A later answer replaces the old group choice. Simultaneous
				// choices must all survive for conflict validation, rather than
				// letting map iteration pick a scientific route arbitrarily.
				key := group + "\x00" + skill + "\x00" + implementation
				current.choices[key] = sciencecapability.ExecutionEvidenceResolver{
					EvidenceGroup: group, Skill: skill, Implementation: implementation,
				}
				selected[groupKey] = current
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
		choices := selected[group].choices
		keys := make([]string, 0, len(choices))
		for key := range choices {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			result = append(result, choices[key])
		}
	}
	return result
}

func selectedAskUserImplementationsFromRunnerEntries(entries []eventjournal.Entry) []string {
	selected, _ := askUserPrimarySelectionScopeFromRunnerEntries(entries)
	return selected
}

// askUserPrimarySelectionScopeFromRunnerEntries is the shared authority for
// primary selection and its dependent resolver scope. Cumulative answer replay
// is deduplicated by call identity; an actual primary change retires prior
// auxiliary selections, while re-confirming the same primary preserves them.
// Historical events are never removed or rewritten by this active projection.
func askUserPrimarySelectionScopeFromRunnerEntries(entries []eventjournal.Entry) ([]string, int64) {
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
	choices := map[int64][]string{}
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
		choices[continuation.eventID] = append(choices[continuation.eventID], implementations...)
	}
	for _, entry := range entries {
		if registered := registrySelectedImplementationsFromRunnerEntry(entry); len(registered) > 0 {
			choices[entry.EventID] = registered
		}
	}
	eventIDs := make([]int64, 0, len(choices))
	for eventID := range choices {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Slice(eventIDs, func(left, right int) bool { return eventIDs[left] < eventIDs[right] })
	var selected []string
	scopeEventID := int64(-1)
	for _, eventID := range eventIDs {
		choice := uniqueSortedFolded(choices[eventID])
		same := len(choice) == len(selected)
		if same {
			for index := range choice {
				same = same && askUserImplementationIdentityMatches(choice[index], selected[index])
			}
		}
		if !same {
			scopeEventID = eventID
		}
		selected = choice
	}
	return selected, scopeEventID
}

func registrySelectedImplementationsFromRunnerEntry(entry eventjournal.Entry) []string {
	message := entry.Message
	if entry.EventID < 0 || strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" ||
		strings.TrimSpace(stringValue(message["status"])) != "completed" ||
		strings.TrimSpace(stringValue(message["toolPhase"])) != "completed" {
		return nil
	}
	toolResult, ok := softwareRuntimeObjectReceipt(message["toolResult"])
	if !ok || agentruntime.ClassifyToolResult(toolResult) != agentruntime.ToolResultSucceeded {
		return nil
	}
	receipt, ok := softwareRuntimeObjectReceipt(toolResult["implementation_selection"])
	if !ok || stringValue(receipt["provenance"]) != "registry-unique-local-pack" {
		return nil
	}
	implementations := uniqueSortedFolded(stringArrayValue(receipt["implementations"]))
	if len(implementations) != 1 || strings.TrimSpace(implementations[0]) == "" {
		return nil
	}
	return implementations
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

package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	generatedPlanResearchSynthesisSchema          = "synon.research_synthesis.v1"
	maxGeneratedPlanResearchSynthesisSources      = 128
	maxGeneratedPlanResearchSynthesisContextBytes = int(runnerLargeToolResultInlineLimitBytes / 2)
)

// generatedPlanResearchModelNavigation is the bounded model projection of the
// durable plan state. The full qualified receipts and immutable source bytes
// remain in the transcript and large-result stores; the model receives stable
// identities plus a fair evidence digest instead of the complete state on
// every execution unit.
func generatedPlanResearchModelNavigation(
	document generatedPlanDocument,
	data map[string]any,
) map[string]any {
	navigation := generatedPlanResearchNavigation(document, data)
	result := copyMapAny(navigation)
	for _, field := range []string{"current_investigations", "retired_research"} {
		items := make([]any, 0, len(anySliceValue(navigation[field])))
		for _, raw := range anySliceValue(navigation[field]) {
			item := mapValue(raw)
			if len(item) == 0 {
				continue
			}
			items = append(items, compactGeneratedPlanInvestigation(item))
		}
		result[field] = items
	}
	if synthesis := generatedPlanResearchSynthesisContext(document, data); len(synthesis) > 0 {
		result["synthesis_context"] = synthesis
	}
	return result
}

// generatedPlanResearchSynthesisContext joins every qualified research module
// into one source-indexed writing input. It preserves complete result handles,
// deduplicates source identities across modules, and keeps distinct
// source/module evidence passages when the bounded model-context budget
// permits. Full source material remains addressable through receipt handles.
func generatedPlanResearchSynthesisContext(
	document generatedPlanDocument,
	data map[string]any,
) map[string]any {
	statuses := mapValue(data["_step_statuses"])
	modules := make([]researchSourceContextModule, 0)

	for _, phase := range document.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				if step.Kind != generatedPlanStepKindResearch {
					continue
				}
				state := mapValue(statuses[step.ID])
				qualified := generatedPlanQualifiedReceiptValues(state["source_receipts"])
				if len(qualified) == 0 {
					continue
				}
				module := map[string]any{
					"id": step.ID, "title": step.Title, "status": stringValue(state["status"]),
				}
				if step.OutputModule != "" {
					module["output_module"] = step.OutputModule
				}
				if step.ResearchQuestion != "" {
					module["research_question"] = step.ResearchQuestion
				}
				if observations := stringValueSlice(state["observations"]); len(observations) > 0 {
					module["observations"] = observations
				}
				modules = append(modules, researchSourceContextModule{value: module, receipts: qualified})
			}
		}
	}
	return buildResearchSourceContext(
		generatedPlanResearchSynthesisSchema, modules, nil, maxGeneratedPlanResearchSynthesisContextBytes,
	)
}

func generatedPlanQualifiedReceiptValues(value any) []map[string]any {
	result := make([]map[string]any, 0)
	for _, raw := range anySliceValue(value) {
		receipt := mapValue(raw)
		if strings.TrimSpace(stringValue(receipt["material_role"])) != "evidence" {
			continue
		}
		result = append(result, receipt)
	}
	return result
}

func generatedPlanResearchReceiptID(receipt map[string]any) string {
	callID := strings.TrimSpace(stringValue(receipt["tool_call_id"]))
	eventID := int64(numberValue(receipt["event_id"]))
	if callID == "" && eventID <= 0 {
		return ""
	}
	return fmt.Sprintf("receipt-%d-%s", eventID, callID)
}

func boundGeneratedPlanResearchSynthesisContext(
	value map[string]any,
	fullPassages []string,
	maxBytes int,
) map[string]any {
	if maxBytes <= 0 {
		maxBytes = maxGeneratedPlanResearchSynthesisContextBytes
	}
	sources := anySliceValue(value["sources"])
	passages := anySliceValue(value["passages"])
	encoded, err := json.Marshal(value)
	if err != nil || len(sources) == 0 || len(passages) == 0 {
		return value
	}
	for len(encoded) >= maxBytes {
		trimmed := false
		modules := anySliceValue(value["modules"])
		for index := len(modules) - 1; index >= 0; index-- {
			module := mapValue(modules[index])
			observations := stringValueSlice(module["observations"])
			if len(observations) == 0 {
				continue
			}
			observations = observations[:len(observations)-1]
			if len(observations) == 0 {
				delete(module, "observations")
			} else {
				module["observations"] = observations
			}
			trimmed = true
			break
		}
		if !trimmed {
			for index := len(anySliceValue(value["receipts"])) - 1; index >= 0; index-- {
				receipt := mapValue(anySliceValue(value["receipts"])[index])
				reference := mapValue(receipt["result_reference"])
				if _, present := reference["read_with"]; !present {
					continue
				}
				delete(reference, "read_with")
				trimmed = true
				break
			}
		}
		if !trimmed {
			candidates := anySliceValue(value["read_candidates"])
			if len(candidates) > 1 {
				value["read_candidates"] = candidates[:len(candidates)-1]
				value["read_candidates_truncated"] = true
				trimmed = true
			}
		}
		if !trimmed && len(passages) > 1 {
			moduleCounts := make(map[string]int)
			for _, raw := range passages {
				moduleCounts[stringValue(mapValue(raw)["module_id"])]++
			}
			removeAt := -1
			for index := len(passages) - 1; index >= 0; index-- {
				moduleID := stringValue(mapValue(passages[index])["module_id"])
				if moduleCounts[moduleID] > 1 {
					removeAt = index
					break
				}
			}
			if removeAt < 0 {
				removeAt = len(passages) - 1
			}
			passages = append(passages[:removeAt], passages[removeAt+1:]...)
			if removeAt < len(fullPassages) {
				fullPassages = append(fullPassages[:removeAt], fullPassages[removeAt+1:]...)
			}
			value["passages"] = passages
			value["passage_index_truncated"] = true
			trimmed = true
		}
		if !trimmed && len(sources) > 1 {
			sources = sources[:len(sources)-1]
			value["sources"] = sources
			value["source_index_truncated"] = true
			trimmed = true
		}
		if !trimmed {
			return value
		}
		encoded, err = json.Marshal(value)
		if err != nil {
			return value
		}
	}
	available := maxBytes - len(encoded)
	perPassage := available / len(passages)
	if perPassage <= 48 {
		return value
	}
	for index, raw := range passages {
		if index >= len(fullPassages) || fullPassages[index] == "" {
			continue
		}
		passage := mapValue(raw)
		excerpt := truncateUTF8ByBytes(fullPassages[index], int64(perPassage-32))
		if excerpt != "" {
			passage["evidence_excerpt"] = excerpt
		}
	}
	for {
		encoded, err = json.Marshal(value)
		if err != nil || len(encoded) < maxBytes {
			return value
		}
		longest := -1
		longestBytes := 0
		for index, raw := range passages {
			passage := mapValue(raw)
			if size := len([]byte(stringValue(passage["evidence_excerpt"]))); size > longestBytes {
				longest = index
				longestBytes = size
			}
		}
		if longest < 0 {
			return value
		}
		passage := mapValue(passages[longest])
		targetBytes := longestBytes - (len(encoded) - maxBytes) - 32
		if targetBytes < 48 {
			delete(passage, "evidence_excerpt")
			continue
		}
		current := stringValue(passage["evidence_excerpt"])
		shorter := truncateUTF8ByBytes(current, int64(targetBytes))
		if shorter == current {
			delete(passage, "evidence_excerpt")
			continue
		}
		passage["evidence_excerpt"] = shorter
	}
}

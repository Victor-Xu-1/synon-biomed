package server

import "strings"

const (
	maxGeneratedPlanResearchReadTargets = 12
	maxGeneratedPlanResearchSourceRefs  = 32
)

func generatedPlanResearchDepthRequiresSourceRead(depth string) bool {
	switch strings.ToLower(strings.TrimSpace(depth)) {
	case "deep", "systematic":
		return true
	default:
		return false
	}
}

// generatedPlanRequiredSourceReadContinuation keeps a deep investigation on
// its current plan step until at least one qualified source-reader receipt is
// bound. Search results remain useful discovery state and provide bounded read
// targets, but cannot by themselves advance a deep module to completed.
func generatedPlanRequiredSourceReadContinuation(
	step generatedPlanStepIdentity,
	receipts []sessionRunnerResearchSourceReceipt,
	pending map[string]any,
) map[string]any {
	continuation := copyMapAny(pending)
	if continuation == nil {
		continuation = map[string]any{}
	}
	continuation["schema"] = "synon.research-source-read.v1"
	continuation["reason"] = "research_source_read_required"
	continuation["detail"] = "deep or systematic research requires a qualified source-read receipt; search-returned titles, snippets, and abstract records remain discovery until a source reader is executed"
	continuation["required"] = true
	continuation["blocking"] = true
	continuation["quality_advisory"] = false
	continuation["required_capability"] = "evidence-read"
	availableSourceRefs := researchEvidenceReceiptCallIDs(receipts)
	if len(availableSourceRefs) > maxGeneratedPlanResearchSourceRefs {
		availableSourceRefs = availableSourceRefs[:maxGeneratedPlanResearchSourceRefs]
	}
	continuation["available_source_refs"] = availableSourceRefs
	continuation["discovery_source_refs"] = researchDiscoveryReceiptCallIDs(receipts, step.ID)
	if targets := researchDiscoveryReadTargets(receipts, step.ID); len(targets) > 0 {
		continuation["available_read_targets"] = targets
	}
	continuation["resolution_options"] = []string{
		"bind an existing qualified source receipt",
		"read a task-relevant returned locator with an advertised evidence-read capability",
		"mark the step blocked or skipped when authoritative material is unavailable",
	}
	return continuation
}

func researchDiscoveryReceiptCallIDs(receipts []sessionRunnerResearchSourceReceipt, stepID string) []string {
	result := make([]string, 0)
	for _, receipt := range researchReceiptsForStep(receipts, stepID, nil, true) {
		if receipt.MaterialRole != "discovery" {
			continue
		}
		if id := strings.TrimSpace(receipt.ToolCallID); id != "" {
			result = append(result, id)
			if len(result) >= maxGeneratedPlanResearchSourceRefs {
				break
			}
		}
	}
	return uniqueStrings(result)
}

func researchDiscoveryReadTargets(receipts []sessionRunnerResearchSourceReceipt, stepID string) []any {
	result := make([]any, 0, maxGeneratedPlanResearchReadTargets)
	seen := make(map[string]struct{})
	for _, receipt := range researchReceiptsForStep(receipts, stepID, nil, true) {
		if receipt.MaterialRole != "discovery" {
			continue
		}
		for _, card := range receipt.EvidenceCards {
			url := strings.TrimSpace(stringValue(card["url"]))
			identity := canonicalWebResearchURL(url)
			if identity == "" {
				continue
			}
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			target := map[string]any{"url": url}
			for _, key := range []string{"title", "citation_handle", "record_depth", "published_at"} {
				if value := strings.TrimSpace(stringValue(card[key])); value != "" {
					target[key] = value
				}
			}
			result = append(result, target)
			if len(result) >= maxGeneratedPlanResearchReadTargets {
				return result
			}
		}
	}
	return result
}

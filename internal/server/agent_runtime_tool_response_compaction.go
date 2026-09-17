package server

import "encoding/json"

// compactAgentRuntimeToolResponse keeps control-plane receipts directly usable
// by the next model turn while leaving large evidence payloads on their
// existing durable read path. It changes only the model/checkpoint projection;
// authoritative plan state and source bytes were already persisted by the
// executing tool.
func compactAgentRuntimeToolResponse(name string, response any) any {
	switch normalizeAgentToolName(name) {
	case "runtimelist":
		return compactAgentRuntimeListToolResponse(response)
	case normalizeAgentToolName(updateStepStatusToolName):
		return compactGeneratedPlanStatusToolResponse(response)
	default:
		return response
	}
}

func compactGeneratedPlanStatusToolResponse(response any) any {
	payload, ok := response.(map[string]any)
	if !ok || !boolValue(payload["ok"], false) || stringValue(payload["step"]) == "" || stringValue(payload["status"]) == "" {
		return response
	}
	result := generatedPlanControlFields(payload,
		"ok", "status", "step", "title", "notes", "observations", "source_refs", "follow_ups",
		"plan_artifact_id", "plan_version_id", "query_languages", "idempotent", "applied", "requested_status",
		"research_continuation", "effect",
	)
	if _, present := payload["source_receipts"]; present {
		result["source_receipts"] = compactGeneratedPlanSourceReceiptIdentities(payload["source_receipts"])
	}
	if transition := mapValue(payload["research_transition"]); len(transition) > 0 {
		result["research_transition"] = compactGeneratedPlanResearchTransition(transition)
		retainGeneratedPlanNewSourceExcerpts(result, transition["new_source_receipts"])
	}
	return result
}

// retainGeneratedPlanNewSourceExcerpts uses the existing inline tool-result
// boundary as a presentation budget. Full evidence remains in immutable
// artifacts; this fair, round-robin projection prevents a same-unit synthesis
// step from receiving only titles and URLs while also keeping control state
// inline instead of externalizing it behind another opaque handle.
func retainGeneratedPlanNewSourceExcerpts(result map[string]any, sourceValue any) {
	transition := mapValue(result["research_transition"])
	compactReceipts := anySliceValue(transition["new_source_receipts"])
	sourceReceipts := anySliceValue(sourceValue)
	if len(compactReceipts) == 0 || len(sourceReceipts) == 0 {
		return
	}
	for cardIndex := 0; ; cardIndex++ {
		visited := false
		for receiptIndex := 0; receiptIndex < len(sourceReceipts) && receiptIndex < len(compactReceipts); receiptIndex++ {
			sourceCards := anySliceValue(mapValue(sourceReceipts[receiptIndex])["evidence_cards"])
			compactCards := anySliceValue(mapValue(compactReceipts[receiptIndex])["evidence_cards"])
			if cardIndex >= len(sourceCards) || cardIndex >= len(compactCards) {
				continue
			}
			visited = true
			excerpt := stringValue(mapValue(sourceCards[cardIndex])["excerpt"])
			if excerpt == "" {
				continue
			}
			card := mapValue(compactCards[cardIndex])
			card["excerpt"] = excerpt
			encoded, err := json.Marshal(result)
			if err != nil || int64(len(encoded)) >= runnerLargeToolResultInlineLimitBytes {
				delete(card, "excerpt")
			}
		}
		if !visited {
			return
		}
	}
}

func compactGeneratedPlanResearchTransition(transition map[string]any) map[string]any {
	result := generatedPlanControlFields(transition,
		"schema", "navigation_state_only", "open_follow_ups",
	)
	if synthesis := mapValue(transition["synthesis_context"]); len(synthesis) > 0 {
		result["synthesis_context"] = synthesis
	}
	if updated := mapValue(transition["updated_investigation"]); len(updated) > 0 {
		result["updated_investigation"] = compactGeneratedPlanInvestigation(updated)
	}
	if _, present := transition["new_source_receipts"]; present {
		result["new_source_receipts"] = compactGeneratedPlanSourceReceipts(transition["new_source_receipts"])
	}
	if next := anySliceValue(transition["next_investigations"]); len(next) > 0 {
		items := make([]any, 0, len(next))
		for _, raw := range next {
			item := mapValue(raw)
			if len(item) == 0 {
				continue
			}
			items = append(items, compactGeneratedPlanInvestigation(item))
		}
		result["next_investigations"] = items
	}
	return result
}

func compactGeneratedPlanInvestigation(investigation map[string]any) map[string]any {
	result := copyMapAny(investigation)
	if receipts, present := investigation["source_receipts"]; present {
		delete(result, "source_receipts")
		result["source_receipt_count"] = len(anySliceValue(receipts))
	}
	return result
}

func compactGeneratedPlanSourceReceipts(value any) []any {
	receipts := anySliceValue(value)
	result := make([]any, 0, len(receipts))
	for _, raw := range receipts {
		receipt := mapValue(raw)
		if len(receipt) == 0 {
			continue
		}
		item := generatedPlanControlFields(receipt,
			"event_id", "tool_call_id", "tool_name", "material_role", "material_state", "investigation_ids",
		)
		if request := mapValue(receipt["request"]); len(request) > 0 {
			item["request"] = generatedPlanControlFields(request, "operation", "query", "url", "research_session")
		}
		if reference := mapValue(receipt["result_reference"]); len(reference) > 0 {
			item["result_reference"] = generatedPlanControlFields(reference,
				"artifact_id", "version_id", "sha256", "size_bytes", "content_url", "read_with", "truncated",
			)
		}
		if cards := anySliceValue(receipt["evidence_cards"]); len(cards) > 0 {
			compactCards := make([]any, 0, len(cards))
			for _, rawCard := range cards {
				card := mapValue(rawCard)
				if len(card) == 0 {
					continue
				}
				compactCards = append(compactCards, generatedPlanControlFields(
					card, "title", "url", "citation_handle", "citation_text", "record_depth", "published_at",
					"source_locator", "excerpt_scope", "excerpt_sha256",
				))
			}
			item["evidence_cards"] = compactCards
		}
		result = append(result, item)
	}
	return result
}

func compactGeneratedPlanSourceReceiptIdentities(value any) []any {
	receipts := anySliceValue(value)
	result := make([]any, 0, len(receipts))
	for _, raw := range receipts {
		receipt := mapValue(raw)
		if len(receipt) == 0 {
			continue
		}
		result = append(result, generatedPlanControlFields(receipt,
			"event_id", "tool_call_id", "tool_name", "material_role", "material_state", "investigation_ids",
		))
	}
	return result
}

func generatedPlanControlFields(source map[string]any, fields ...string) map[string]any {
	result := make(map[string]any, len(fields))
	for _, field := range fields {
		if value, present := source[field]; present {
			result[field] = value
		}
	}
	return result
}

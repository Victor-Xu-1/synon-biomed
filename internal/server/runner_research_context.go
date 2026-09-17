package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

const (
	sessionRunnerResearchContextSchema         = "synon.research_context.v1"
	sessionRunnerResearchContextManifestSchema = "synon.research_context_manifest.v1"
	sessionRunnerSourceMaterialContextSchema   = "synon.source_material.v1"
	generatedPlanResearchHandoffSchema         = "synon.research_handoff.v1"
	maxSessionRunnerResearchContextBytes       = 64 * 1024
	sessionRunnerTaskResearchModuleID          = "task"
	sessionRunnerTaskResearchModuleTitle       = "Task-wide source material"
	maxSessionRunnerSourceReadCandidates       = 16
)

type researchSourceContextModule struct {
	value    map[string]any
	receipts []map[string]any
}

func (s *Server) sessionRunnerResearchModelContext(
	ctx context.Context,
	run *sessionRunnerChatRun,
) (map[string]any, error) {
	if run == nil || run.Transcript == nil {
		return nil, nil
	}
	materials, err := s.sessionRunnerResearchMaterials(ctx, run)
	if err != nil {
		return nil, err
	}
	var document generatedPlanDocument
	var data map[string]any
	frameID := strings.TrimSpace(run.Transcript.Stream.FrameID)
	if frameID == "" {
		frameID = strings.TrimSpace(run.SessionID)
	}
	if snapshot, found := s.generatedPlanProcessSnapshot(frameID); found {
		document, err = generatedPlanDocumentFromMap(mapValue(snapshot.data["_plan_json"]))
		if err != nil {
			return nil, fmt.Errorf("restore research context plan: %w", err)
		}
		data = snapshot.data
	}
	return buildSessionRunnerResearchModelContext(document, data, materials), nil
}

func runtimeTaskResearchContextMessage(value map[string]any) string {
	if stringValue(value["schema"]) != sessionRunnerResearchContextSchema {
		return ""
	}
	encoded, err := json.Marshal(systemResearchContextProjection(value))
	if err != nil {
		return ""
	}
	return "[System] <task_research_context>" + string(encoded) + "</task_research_context>"
}

func attachRuntimeTaskResearchContext(
	messages []chatCompletionMessage,
	value map[string]any,
) []chatCompletionMessage {
	if stringValue(value["schema"]) != sessionRunnerResearchContextSchema {
		return messages
	}
	result := make([]chatCompletionMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" && strings.Contains(message.Content, "<task_research_context>") {
			continue
		}
		result = append(result, message)
	}
	for index := len(result) - 1; index >= 0; index-- {
		if result[index].Role != "tool" || strings.TrimSpace(result[index].Content) == "" {
			continue
		}
		enriched, err := agentruntime.AttachToolResultModelContext(result[index].Content, value)
		if err != nil {
			continue
		}
		result[index].Content = enriched
		return result
	}
	return appendRuntimeTerminalPolicyContextMessage(result, runtimeTaskResearchContextMessage(value))
}

// systemResearchContextProjection keeps only identities, bounded source
// metadata and read handles when no matching tool result survives compaction.
// The projection explicitly labels source values as untrusted and omits raw
// excerpts and model observations; full content returns only through a later
// tool result.
func systemResearchContextProjection(value map[string]any) map[string]any {
	result := generatedPlanControlFields(value,
		"schema", "revision", "material_counts", "full_material_externalized",
	)
	result["source_values_are_untrusted_data"] = true
	modules := make([]any, 0, len(anySliceValue(value["modules"])))
	for _, raw := range anySliceValue(value["modules"]) {
		module := generatedPlanControlFields(mapValue(raw), "id", "status", "kind", "phase_id", "track_id")
		if len(module) > 0 {
			modules = append(modules, module)
		}
	}
	result["modules"] = modules
	receipts := make([]any, 0, len(anySliceValue(value["receipts"])))
	for _, raw := range anySliceValue(value["receipts"]) {
		receipt := mapValue(raw)
		item := generatedPlanControlFields(receipt, "id", "material_state", "material_role", "tool_name", "investigation_ids")
		if reference := mapValue(receipt["result_reference"]); len(reference) > 0 {
			item["result_reference"] = generatedPlanControlFields(reference, "version_id", "read_with")
		}
		if len(item) > 0 {
			receipts = append(receipts, item)
		}
	}
	result["receipts"] = receipts
	sources := make([]any, 0, len(anySliceValue(value["sources"])))
	for _, raw := range anySliceValue(value["sources"]) {
		source := generatedPlanControlFields(mapValue(raw),
			"id", "title", "url", "citation_handle", "record_depth", "published_at", "module_ids", "receipt_ids",
			"source_identifier", "source_status",
		)
		if len(source) > 0 {
			sources = append(sources, source)
		}
	}
	result["sources"] = sources
	passages := make([]any, 0, len(anySliceValue(value["passages"])))
	for _, raw := range anySliceValue(value["passages"]) {
		passage := generatedPlanControlFields(mapValue(raw),
			"source_id", "module_id", "source_locator", "excerpt_scope", "excerpt_sha256",
		)
		if len(passage) > 0 {
			passages = append(passages, passage)
		}
	}
	result["passages"] = passages
	readCandidates := make([]any, 0, len(anySliceValue(value["read_candidates"])))
	for _, raw := range anySliceValue(value["read_candidates"]) {
		candidate := generatedPlanControlFields(mapValue(raw), "tool", "arguments", "url", "record_depth", "source_locator", "fallback")
		if len(candidate) > 0 {
			readCandidates = append(readCandidates, candidate)
		}
	}
	if len(readCandidates) > 0 {
		result["read_candidates"] = readCandidates
	}
	return result
}

func sessionRunnerResearchContextManifest(value map[string]any) map[string]any {
	if stringValue(value["schema"]) != sessionRunnerResearchContextSchema ||
		strings.TrimSpace(stringValue(value["revision"])) == "" {
		return nil
	}
	return map[string]any{
		"schema":           sessionRunnerResearchContextManifestSchema,
		"context_revision": stringValue(value["revision"]),
		"prepared_state":   "model_context_built",
		"material_counts":  copyMapAny(mapValue(value["material_counts"])),
		"receipt_ids":      researchContextObjectIDs(value["receipts"]),
		"source_ids":       researchContextObjectIDs(value["sources"]),
	}
}

func researchContextObjectIDs(value any) []string {
	ids := make([]string, 0, len(anySliceValue(value)))
	for _, raw := range anySliceValue(value) {
		if id := strings.TrimSpace(stringValue(mapValue(raw)["id"])); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// buildSessionRunnerResearchModelContext projects every usable source receipt
// from the logical task, including discovery material and receipts associated
// with ordinary work steps. The immutable transcript and externalized result
// versions remain authoritative; this bounded value is only the model view.
func buildSessionRunnerResearchModelContext(
	document generatedPlanDocument,
	data map[string]any,
	materials sessionRunnerResearchMaterialSet,
) map[string]any {
	if len(materials.Receipts) == 0 {
		return nil
	}
	statuses := mapValue(data["_step_statuses"])
	moduleValues := make(map[string]map[string]any)
	moduleOrder := make([]string, 0)
	for _, phase := range document.Phases {
		for _, track := range phase.Delegations {
			for _, step := range track.Steps {
				module := researchContextPlanModule(step, mapValue(statuses[step.ID]))
				module["phase"] = phase.Name
				module["phase_id"] = phase.ID
				module["track"] = track.Name
				module["track_id"] = track.ID
				moduleValues[step.ID] = module
				moduleOrder = append(moduleOrder, step.ID)
			}
		}
	}

	modules := make([]researchSourceContextModule, 0)
	modulePositions := make(map[string]int)
	ensureModule := func(id string) int {
		id = strings.TrimSpace(id)
		if id == "" {
			id = sessionRunnerTaskResearchModuleID
		}
		if position, found := modulePositions[id]; found {
			return position
		}
		module := copyMapAny(moduleValues[id])
		if len(module) == 0 {
			module = map[string]any{"id": id}
			if id == sessionRunnerTaskResearchModuleID {
				module["title"] = sessionRunnerTaskResearchModuleTitle
				module["status"] = "active"
			}
		}
		position := len(modules)
		modulePositions[id] = position
		modules = append(modules, researchSourceContextModule{value: module})
		return position
	}

	for _, receipt := range materials.Receipts {
		value := researchSourceReceiptValue(receipt)
		ids := uniqueStrings(receipt.InvestigationIDs)
		if len(ids) == 0 {
			ids = []string{sessionRunnerTaskResearchModuleID}
		}
		for _, id := range ids {
			position := ensureModule(id)
			modules[position].receipts = append(modules[position].receipts, value)
		}
	}
	// Plan order is useful to readers, while source receipts may introduce
	// retired or task-wide modules. Sort only by the stable plan position and
	// then by id so replay never depends on map iteration.
	planPosition := make(map[string]int, len(moduleOrder))
	for index, id := range moduleOrder {
		planPosition[id] = index
	}
	sort.SliceStable(modules, func(i, j int) bool {
		left := stringValue(modules[i].value["id"])
		right := stringValue(modules[j].value["id"])
		leftPosition, leftPlanned := planPosition[left]
		rightPosition, rightPlanned := planPosition[right]
		if leftPlanned != rightPlanned {
			return leftPlanned
		}
		if leftPlanned && leftPosition != rightPosition {
			return leftPosition < rightPosition
		}
		return left < right
	})

	counts := map[string]any{"discovery": 0, "evidence": 0}
	for _, receipt := range materials.Receipts {
		role := strings.TrimSpace(receipt.MaterialRole)
		if _, present := counts[role]; present {
			counts[role] = int(numberValue(counts[role])) + 1
		}
	}
	extra := map[string]any{
		"material_counts": counts,
		"task_summary":    document.TaskSummary,
		"desired_outputs": append([]string(nil), document.DesiredOutputs...),
	}
	return buildResearchSourceContext(sessionRunnerResearchContextSchema, modules, extra, maxSessionRunnerResearchContextBytes)
}

func researchContextPlanModule(step generatedPlanStep, state map[string]any) map[string]any {
	module := map[string]any{
		"id": step.ID, "title": step.Title,
		"status": firstNonEmpty(strings.TrimSpace(stringValue(state["status"])), "pending"),
	}
	if step.Description != "" {
		module["description"] = step.Description
	}
	if step.Kind != "" {
		module["kind"] = step.Kind
	}
	if step.OutputModule != "" {
		module["output_module"] = step.OutputModule
	}
	if step.ResearchQuestion != "" {
		module["research_question"] = step.ResearchQuestion
	}
	for _, field := range generatedPlanNavigationFields {
		if values := stringValueSlice(state[field]); len(values) > 0 {
			module[field] = values
		}
	}
	return module
}

// buildResearchSourceContext is the single source-indexed projection used by
// plan synthesis and task-wide continuation. Callers choose which receipts are
// eligible; this function only preserves identities, locations and passages.
func buildResearchSourceContext(
	schema string,
	modules []researchSourceContextModule,
	extra map[string]any,
	maxBytes int,
) map[string]any {
	moduleValues := make([]any, 0, len(modules))
	receipts := make([]any, 0)
	sources := make([]any, 0)
	passages := make([]any, 0)
	receiptPositions := make(map[string]int)
	sourcePositions := make(map[string]int)
	passagePositions := make(map[string]int)
	fullPassages := make([]string, 0)
	sourceIndexTruncated := false

	for _, module := range modules {
		if len(module.value) == 0 || len(module.receipts) == 0 {
			continue
		}
		moduleID := strings.TrimSpace(stringValue(module.value["id"]))
		if moduleID == "" {
			continue
		}
		moduleValues = append(moduleValues, copyMapAny(module.value))
		for _, receipt := range module.receipts {
			receiptID := generatedPlanResearchReceiptID(receipt)
			if receiptID == "" {
				continue
			}
			if _, exists := receiptPositions[receiptID]; !exists {
				item := generatedPlanControlFields(receipt,
					"material_state", "material_role", "tool_name", "investigation_ids",
				)
				item["id"] = receiptID
				if reference := mapValue(receipt["result_reference"]); len(reference) > 0 {
					item["result_reference"] = generatedPlanControlFields(reference,
						"version_id", "read_with",
					)
				}
				receiptPositions[receiptID] = len(receipts)
				receipts = append(receipts, item)
			}
			for _, rawCard := range anySliceValue(receipt["evidence_cards"]) {
				card := mapValue(rawCard)
				url := strings.TrimSpace(stringValue(card["url"]))
				title := strings.TrimSpace(stringValue(card["title"]))
				focus := strings.TrimSpace(stringValue(card["research_focus"]))
				key := canonicalWebResearchURL(url)
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(stringValue(card["citation_handle"])))
				}
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(stringValue(card["source_identifier"])))
				}
				if key == "" {
					key = strings.ToLower(title)
				}
				if key == "" {
					continue
				}
				var sourceID string
				if position, exists := sourcePositions[key]; exists {
					existing := mapValue(sources[position])
					sourceID = stringValue(existing["id"])
					existing["module_ids"] = appendUniqueFolded(stringValueSlice(existing["module_ids"]), moduleID)
					existing["receipt_ids"] = appendUniqueFolded(stringValueSlice(existing["receipt_ids"]), receiptID)
					if focus != "" {
						existing["research_focuses"] = appendUniqueFolded(stringValueSlice(existing["research_focuses"]), focus)
					}
					copyResearchSourceMetadata(existing, card)
				} else {
					if len(sources) >= maxGeneratedPlanResearchSynthesisSources {
						sourceIndexTruncated = true
						continue
					}
					identity := sha256.Sum256([]byte("synon-research-source-v1\x00" + key))
					sourceID = "source-" + hex.EncodeToString(identity[:8])
					item := map[string]any{
						"id": sourceID, "module_ids": []string{moduleID}, "receipt_ids": []string{receiptID},
					}
					if title != "" {
						item["title"] = title
					}
					if url != "" {
						item["url"] = url
					}
					if focus != "" {
						item["research_focuses"] = []string{focus}
					}
					copyResearchSourceMetadata(item, card)
					sourcePositions[key] = len(sources)
					sources = append(sources, item)
				}
				candidate := strings.TrimSpace(stringValue(card["excerpt"]))
				if candidate == "" {
					continue
				}
				passageKey := sourceID + "\x00" + moduleID + "\x00" + strings.Join(strings.Fields(candidate), " ")
				if _, exists := passagePositions[passageKey]; exists {
					continue
				}
				passagePositions[passageKey] = len(passages)
				passage := map[string]any{"source_id": sourceID, "module_id": moduleID}
				if focus != "" {
					passage["research_focus"] = focus
				}
				for _, key := range []string{"source_locator", "excerpt_scope", "excerpt_sha256"} {
					if value := strings.TrimSpace(stringValue(card[key])); value != "" {
						passage[key] = value
					}
				}
				passages = append(passages, passage)
				fullPassages = append(fullPassages, candidate)
			}
		}
	}
	if len(receipts) == 0 {
		return nil
	}
	result := map[string]any{
		"schema": schema, "modules": moduleValues, "receipts": receipts,
		"sources": sources, "passages": passages,
	}
	if schema == generatedPlanResearchSynthesisSchema {
		result["full_evidence_externalized"] = true
	} else {
		result["full_material_externalized"] = true
	}
	for key, value := range extra {
		result[key] = value
	}
	if sourceIndexTruncated {
		result["source_index_truncated"] = true
	}
	const revisionReserveBytes = 64
	boundBytes := maxBytes - revisionReserveBytes
	if boundBytes <= 0 {
		boundBytes = maxBytes
	}
	result = boundGeneratedPlanResearchSynthesisContext(result, fullPassages, boundBytes)
	encoded, _ := json.Marshal(result)
	digest := sha256.Sum256(encoded)
	result["revision"] = hex.EncodeToString(digest[:12])
	return result
}

func copyResearchSourceMetadata(target, card map[string]any) {
	for _, key := range []string{
		"citation_handle", "citation_text", "record_depth", "published_at", "source_locator", "excerpt_scope",
		"source_identifier", "source_status",
	} {
		if _, present := target[key]; present {
			continue
		}
		if value := strings.TrimSpace(stringValue(card[key])); value != "" {
			target[key] = value
		}
	}
}

func currentResearchSourceMaterial(call agentruntime.ToolCall, result any, focuses ...string) map[string]any {
	cards := researchMaterialEvidenceCards(call, result, focuses...)
	if len(cards) == 0 {
		return nil
	}
	role := "evidence"
	if normalizeAgentToolName(call.Name) == "websearch" {
		role = "discovery"
	}
	receipt := map[string]any{
		"event_id": 0, "tool_call_id": strings.TrimSpace(call.ID), "tool_name": strings.TrimSpace(call.Name),
		"material_role": role, "material_state": "current_tool_result",
	}
	cardValues := make([]any, 0, len(cards))
	for _, card := range cards {
		cardValues = append(cardValues, copyMapAny(card))
	}
	receipt["evidence_cards"] = cardValues
	extra := map[string]any{
		"model_context_only": true,
		"material_role":      role,
	}
	if role == "discovery" {
		if candidates := researchSourceReadCandidates(cards); len(candidates) > 0 {
			extra["read_candidates"] = candidates
		}
	}
	return buildResearchSourceContext(sessionRunnerSourceMaterialContextSchema, []researchSourceContextModule{{
		value: map[string]any{
			"id": "current-source-call", "title": "Current source result", "status": "available",
		},
		receipts: []map[string]any{receipt},
	}}, extra, maxGeneratedPlanResearchSynthesisContextBytes)
}

func researchSourceReadCandidates(cards []map[string]any) []any {
	result := make([]any, 0, min(len(cards)*2, maxSessionRunnerSourceReadCandidates))
	seen := make(map[string]struct{})
	appendCandidate := func(tool string, arguments map[string]any, card map[string]any, fallback bool) {
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return
		}
		key := strings.ToLower(strings.TrimSpace(tool)) + "\x00" + string(encoded)
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		candidate := map[string]any{"tool": tool, "arguments": arguments}
		for _, field := range []string{"title", "url", "record_depth", "source_locator"} {
			if value := strings.TrimSpace(stringValue(card[field])); value != "" {
				candidate[field] = value
			}
		}
		if fallback {
			candidate["fallback"] = true
		}
		result = append(result, candidate)
	}
	for _, card := range cards {
		if len(result) >= maxSessionRunnerSourceReadCandidates {
			break
		}
		handle := strings.TrimSpace(stringValue(card["citation_handle"]))
		namespace, identifier, found := strings.Cut(handle, ":")
		articleRoute := false
		if found && strings.TrimSpace(identifier) != "" {
			switch strings.ToLower(strings.TrimSpace(namespace)) {
			case "doi":
				appendCandidate("fetch_article_fulltext", map[string]any{"doi": strings.TrimSpace(identifier)}, card, false)
				articleRoute = true
			case "pmcid":
				appendCandidate("fetch_article_fulltext", map[string]any{"pmcid": strings.ToUpper(strings.TrimSpace(identifier))}, card, false)
				articleRoute = true
			}
		}
		if len(result) >= maxSessionRunnerSourceReadCandidates {
			break
		}
		if rawURL := strings.TrimSpace(stringValue(card["url"])); rawURL != "" {
			appendCandidate("web_fetch", map[string]any{"url": rawURL}, card, articleRoute)
		}
	}
	return result
}

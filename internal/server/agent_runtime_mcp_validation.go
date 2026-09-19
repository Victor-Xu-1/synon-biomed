package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"synon-go/internal/agentruntime"
	"synon-go/internal/failurecontract"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/software"
	"synon-go/internal/toolcontract"
)

func (g serverAgentRuntimeToolGateway) normalizeAdmittedToolArguments(name string, input map[string]any) map[string]any {
	input = normalizeSelectedManagedEnvironment(g.taskRun, name, input)
	input = g.normalizeManagedExecutionRuntimeArguments(name, input)
	if name == "ask_user" {
		if normalized, err := normalizeAskUserToolInput(input); err == nil {
			input = normalized
		}
		input = g.normalizeManagedAskUserExecutionArguments(input)
	}
	if name == generatePlanToolName {
		input = normalizeLegacyGeneratePlanArguments(input)
	}
	if name == toolcontract.Skill && g.taskRun != nil {
		pending := g.taskRun.pendingRequiredSkillNamesSnapshot()
		if len(pending) == 1 {
			input = copyMapAny(input)
			input["skill"] = pending[0]
		}
	}
	if normalizeAgentToolName(name) == "saveartifacts" {
		input = normalizeAgentRuntimeSaveArtifactsArguments(input)
	}
	if name == "save_artifacts" && g.taskRun != nil {
		// User-facing deliverable intent has one normalization authority. Plan
		// navigation cannot silently demote an explicit snapshot to working data.
		input = sessionRunnerNormalizeExplicitDeliverableDestinations(g.taskRun.TaskIntent, input)
	}
	if g.server != nil {
		input = g.server.generatedPlanUpdateStepStatusInput(g.sessionID, name, input)
		capabilities := []string(nil)
		if g.taskRun != nil {
			capabilities = g.taskRun.toolCapabilities(name)
		}
		input = g.server.generatedPlanResearchQueryInput(g.sessionID, name, input, capabilities)
	}
	validator, found := g.toolValidators[name]
	if !found {
		for _, schema := range g.toolSchemas {
			if schema.Name == name {
				validator = compileAgentRuntimeMCPValidator(schema)
				found = true
				break
			}
		}
	}
	if !found || validator.compileErr != nil || len(validator.schema) == 0 {
		return input
	}
	normalized := normalizeAgentRuntimeObject(validator.schema, input)
	normalized = normalizeAgentRuntimePresentationDefaults(validator.schema, normalized)
	normalized = normalizeAgentRuntimeMCPArguments(name, validator.schema, normalized)
	if name == "read_file" {
		normalized = normalizeAgentWorkspaceReadFileArguments(normalized)
	} else if name == "edit_file" {
		normalized = normalizeAgentWorkspaceEditFileArguments(normalized)
	}
	return normalized
}

// normalizeManagedAskUserExecutionArguments applies the same registry-driven
// rewrite to the arguments that actually reach the AskUser executor as to the
// durable model-call checkpoint. Keeping this in the gateway normalization
// stage prevents the UI/persisted decision from losing resolver authority or
// terminal choices while the progress transcript displays a corrected card.
func (g serverAgentRuntimeToolGateway) normalizeManagedAskUserExecutionArguments(input map[string]any) map[string]any {
	if g.server == nil || g.taskRun == nil || input == nil {
		return input
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return input
	}
	call := g.server.sanitizeManagedAskUserToolCallForCheckpoint(g.taskRun, agentruntime.ToolCall{
		Name: "ask_user", Arguments: raw,
	})
	var normalized map[string]any
	if json.Unmarshal(call.Arguments, &normalized) != nil || normalized == nil {
		return input
	}
	return normalized
}

// normalizeAgentRuntimeSaveArtifactsArguments repairs only unambiguous
// provider formatting variants. It keeps the executor schema authoritative,
// while preventing an otherwise valid file publication from failing because a
// model used common file descriptors or omitted the neutral mixed-file
// producer identifier.
func normalizeAgentRuntimeSaveArtifactsArguments(input map[string]any) map[string]any {
	if len(input) == 0 {
		return input
	}
	normalized := copyMapAny(input)
	if rawFiles, found := input["files"]; found {
		values := anySliceValue(rawFiles)
		if len(values) == 0 {
			return input
		}
		files := make([]string, 0, len(values))
		changed := false
		for _, raw := range values {
			if path, ok := raw.(string); ok {
				files = append(files, path)
				continue
			}
			descriptor := mapValue(raw)
			path := firstNonEmpty(
				strings.TrimSpace(stringValue(descriptor["file_path"])),
				strings.TrimSpace(stringValue(descriptor["path"])),
			)
			if path == "" {
				return input
			}
			files = append(files, path)
			changed = true
		}
		if changed {
			normalized["files"] = files
		}
	} else {
		allowed := map[string]bool{
			"files": true, "language": true, "environment": true, "version_of": true,
			"checkpoints": true, "destination": true, "human_description": true,
		}
		files := make([]string, 0, len(input))
		for path, description := range input {
			if allowed[path] {
				return input
			}
			descriptionText, ok := description.(string)
			if !ok || strings.TrimSpace(descriptionText) == "" || len([]byte(descriptionText)) > 256 {
				return input
			}
			normalizedPath, err := normalizeAgentSavedArtifactPath(path)
			if err != nil || !unambiguousAgentArtifactPathMapKey(normalizedPath) {
				return input
			}
			files = append(files, normalizedPath)
		}
		if len(files) == 0 {
			return input
		}
		sort.Strings(files)
		normalized = map[string]any{"files": files}
	}
	if language, present := normalized["language"]; !present || strings.TrimSpace(stringValue(language)) == "" {
		normalized["language"] = "text"
	}
	return normalized
}

// unambiguousAgentArtifactPathMapKey keeps the legacy {path: description}
// compatibility form narrower than the canonical schema. Bare field names
// such as path, encoding, content, or markdown are valid JSON object keys but
// are not unambiguous file declarations. Canonical files may still use any
// valid relative path through the required files array.
func unambiguousAgentArtifactPathMapKey(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	base := filepath.Base(path)
	if strings.Contains(path, "/") {
		return base != "" && base != "." && base != ".."
	}
	if strings.HasPrefix(base, ".") {
		return len(base) > 1
	}
	separator := strings.LastIndexByte(base, '.')
	return separator > 0 && separator < len(base)-1
}

// normalizeAgentRuntimeMCPArguments is the single provider-neutral argument
// compatibility boundary shared by direct MCP tools and host.mcp calls. It
// canonicalizes only live-schema formatting variants and the connector's
// documented legacy aliases; validation remains authoritative afterwards.
func normalizeAgentRuntimeMCPArguments(name string, schema map[string]any, input map[string]any) map[string]any {
	normalized := kernelMCPNormalizeSchemaEnumAliases(input, schema)
	if strings.HasPrefix(name, "mcp__chembl__") {
		normalized = normalizeChEMBLNullSentinels(schema, normalized)
	}
	if name == "mcp__structures-interactions__pdb_search_structures" {
		normalized = normalizePDBSearchNullSentinels(schema, normalized)
	}
	if name == "mcp__clinical-trials__search_trials" {
		normalized = normalizeClinicalTrialsSearchArguments(normalized)
	} else if strings.TrimSpace(name) == "MCPTool" {
		normalized = normalizeClinicalTrialsMCPEnvelope(normalized)
	}
	return normalized
}

// normalizeAgentRuntimePresentationDefaults keeps non-executable stream labels
// out of the tool-failure path. Provider adapters and lower-capability models
// can omit human_description even when a native schema requests it; rejecting
// otherwise complete executable arguments spends a correction round without
// changing the operation. The original model input remains available for the
// audit and public projection, while the admitted execution payload receives a
// bounded neutral label for legacy executors that still decode the field.
func normalizeAgentRuntimePresentationDefaults(schema map[string]any, input map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	if _, admitted := properties["human_description"]; !admitted {
		return input
	}
	required := false
	for _, raw := range anySliceValue(schema["required"]) {
		if strings.EqualFold(strings.TrimSpace(stringValue(raw)), "human_description") {
			required = true
			break
		}
	}
	if !required || strings.TrimSpace(stringValue(input["human_description"])) != "" {
		return input
	}
	normalized := copyMapAny(input)
	normalized["human_description"] = "Working on the requested step"
	return normalized
}

func normalizeLegacyGeneratePlanArguments(input map[string]any) map[string]any {
	normalized := copyMapAny(input)
	if boolValue(normalized["approve"], false) {
		// Approval is a control transition, not a plan draft. Adding fallback
		// feasibility or container fields would turn the valid approve-only call
		// into a mixed mutation that the executor must reject.
		return normalized
	}
	rootName := firstNonEmpty(
		strings.TrimSpace(stringValue(normalized["name"])),
		strings.TrimSpace(stringValue(normalized["title"])),
		strings.TrimSpace(stringValue(normalized["description"])),
	)
	for _, runtimeOwned := range []string{
		"version", "session_id", "frame_id", "tool_call_id", "phase_id", "delegation_id", "step_id",
	} {
		delete(normalized, runtimeOwned)
	}
	for _, presentationAlias := range []string{"name", "title", "description", "kind"} {
		delete(normalized, presentationAlias)
	}
	if strings.TrimSpace(stringValue(normalized["task_summary"])) == "" && rootName != "" {
		normalized["task_summary"] = rootName
	}
	if _, found := normalized["feasibility"]; !found {
		normalized["feasibility"] = map[string]any{
			"confidence": "medium",
			"rationale":  "The proposed steps are feasible subject to runtime verification of their inputs and tools.",
		}
	}
	if rawPhases, hasPhases := normalized["phases"]; hasPhases {
		delete(normalized, "steps")
		phases := anySliceValue(rawPhases)
		for phaseIndex, rawPhase := range phases {
			phase := copyMapAny(mapValue(rawPhase))
			if phase == nil {
				continue
			}
			delete(phase, "id")
			phaseName := firstNonEmpty(
				strings.TrimSpace(stringValue(phase["name"])),
				strings.TrimSpace(stringValue(phase["title"])),
				strings.TrimSpace(stringValue(phase["description"])),
			)
			if phaseName == "" {
				phaseName = fmt.Sprintf("Phase %d", phaseIndex+1)
			}
			phase["name"] = phaseName
			for _, presentationAlias := range []string{"title", "description", "kind", "human_description"} {
				delete(phase, presentationAlias)
			}
			if rawSteps, hasSteps := phase["steps"]; hasSteps {
				delete(phase, "steps")
				phase["delegations"] = []any{map[string]any{
					"name": "Main task", "steps": normalizeLegacyGeneratePlanSteps(rawSteps),
				}}
			}
			delegations := anySliceValue(phase["delegations"])
			for delegationIndex, rawDelegation := range delegations {
				delegation := copyMapAny(mapValue(rawDelegation))
				if delegation == nil {
					continue
				}
				delegationName := firstNonEmpty(
					strings.TrimSpace(stringValue(delegation["name"])),
					strings.TrimSpace(stringValue(delegation["title"])),
					strings.TrimSpace(stringValue(delegation["description"])),
				)
				if delegationName == "" {
					delegationName = fmt.Sprintf("Workstream %d", delegationIndex+1)
				}
				delegation["name"] = delegationName
				delete(delegation, "id")
				delete(delegation, "agent_name")
				for _, presentationAlias := range []string{"title", "description", "kind", "human_description"} {
					delete(delegation, presentationAlias)
				}
				delegation["steps"] = normalizeLegacyGeneratePlanSteps(delegation["steps"])
				delegations[delegationIndex] = delegation
			}
			phase["delegations"] = delegations
			phases[phaseIndex] = phase
		}
		normalized["phases"] = phases
		return normalized
	}
	// Some provider-native tool adapters express a one-phase plan as the phase
	// object itself: {name, delegations}. This shape contains the full nested
	// execution structure and is unambiguous, so adapt it at the single schema
	// admission boundary instead of spending a failed model round asking for an
	// otherwise identical wrapper object.
	if rawDelegations, hasDelegations := normalized["delegations"]; hasDelegations {
		phaseName := rootName
		if phaseName == "" {
			phaseName = "Execution"
		}
		delegations := anySliceValue(rawDelegations)
		for delegationIndex, rawDelegation := range delegations {
			delegation := copyMapAny(mapValue(rawDelegation))
			if delegation == nil {
				continue
			}
			delete(delegation, "id")
			delete(delegation, "agent_name")
			delegation["steps"] = normalizeLegacyGeneratePlanSteps(delegation["steps"])
			delegations[delegationIndex] = delegation
		}
		delete(normalized, "delegations")
		if strings.TrimSpace(stringValue(normalized["task_summary"])) == "" {
			normalized["task_summary"] = phaseName
		}
		normalized["phases"] = []any{map[string]any{
			"name": phaseName, "delegations": delegations,
		}}
		return normalized
	}
	steps, hasSteps := normalized["steps"]
	if !hasSteps {
		return normalized
	}
	delete(normalized, "steps")
	normalized["phases"] = []any{map[string]any{
		"name": "Execution",
		"delegations": []any{map[string]any{
			"name": "Main task", "steps": normalizeLegacyGeneratePlanSteps(steps),
		}},
	}}
	return normalized
}

func normalizeLegacyGeneratePlanSteps(value any) []any {
	steps := anySliceValue(value)
	for index, rawStep := range steps {
		step := copyMapAny(mapValue(rawStep))
		if step == nil {
			continue
		}
		for _, runtimeOwned := range []string{"id", "status", "notes", "step_id"} {
			delete(step, runtimeOwned)
		}
		if strings.TrimSpace(stringValue(step["title"])) == "" {
			step["title"] = firstNonEmpty(
				strings.TrimSpace(stringValue(step["name"])),
				strings.TrimSpace(stringValue(step["description"])),
			)
		}
		delete(step, "name")
		if strings.TrimSpace(stringValue(step["description"])) == "" {
			step["description"] = firstNonEmpty(
				strings.TrimSpace(stringValue(step["human_description"])),
				strings.TrimSpace(stringValue(step["title"])),
			)
		}
		delete(step, "human_description")
		if strings.TrimSpace(stringValue(step["kind"])) == "" {
			step["kind"] = generatedPlanStepKindWork
		}
		steps[index] = step
	}
	return steps
}

// normalizeAgentWorkspaceEditFileArguments closes the contract gap between
// provider-native full-file writes (path/content) and the canonical exact-edit
// schema (file_path/old_string/new_string). The mapping is deliberately scoped
// to edit_file and only accepts unambiguous full replacements. Canonical fields
// always win, while unrelated unknown fields remain for strict schema rejection.
func normalizeAgentWorkspaceEditFileArguments(input map[string]any) map[string]any {
	normalized := copyMapAny(input)
	if _, present := normalized["file_path"]; !present {
		if path, ok := normalized["path"].(string); ok && strings.TrimSpace(path) != "" {
			normalized["file_path"] = path
			delete(normalized, "path")
		}
	} else {
		delete(normalized, "path")
	}

	if newString, present := normalized["new_string"]; present {
		path, _ := normalized["file_path"].(string)
		if strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".json") {
			switch typed := newString.(type) {
			case map[string]any, []any:
				if encoded, err := json.MarshalIndent(typed, "", "  "); err == nil {
					normalized["new_string"] = string(encoded) + "\n"
				}
			}
		}
		delete(normalized, "content")
	} else {
		content, found := normalized["content"]
		if found {
			switch typed := content.(type) {
			case string:
				normalized["new_string"] = typed
				delete(normalized, "content")
			case map[string]any, []any:
				path, _ := normalized["file_path"].(string)
				if strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".json") {
					if encoded, err := json.MarshalIndent(typed, "", "  "); err == nil {
						normalized["new_string"] = string(encoded) + "\n"
						delete(normalized, "content")
					}
				}
			}
		}
	}
	if _, present := normalized["old_string"]; !present {
		if _, fullReplacement := normalized["new_string"]; fullReplacement {
			normalized["old_string"] = ""
		}
	}
	return normalized
}

// normalizePDBSearchNullSentinels closes the same nullable-string ambiguity
// for the captured RCSB search contract. Model providers sometimes serialize
// omitted optional filters as Python's string "None". None is not a valid PDB
// identifier, organism, method, or search filter, and forwarding it produces
// avoidable upstream validation failures instead of an unfiltered search.
func normalizePDBSearchNullSentinels(schema map[string]any, input map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	nullableFields := map[string]struct{}{}
	for _, field := range []string{
		"text", "uniprot_accession", "organism", "taxonomy_id",
		"experimental_method", "max_resolution_angstrom", "ligand_comp_id",
	} {
		nullableFields[field] = struct{}{}
	}
	var normalized map[string]any
	for field, rawValue := range input {
		if _, known := nullableFields[field]; !known {
			continue
		}
		text, ok := rawValue.(string)
		if !ok || !isAgentRuntimeNullSentinel(text) {
			continue
		}
		property, _ := properties[field].(map[string]any)
		if !agentRuntimeSchemaAllowsJSONType(property, "null") {
			continue
		}
		if normalized == nil {
			normalized = copyMapAny(input)
		}
		normalized[field] = nil
	}
	if normalized != nil {
		return normalized
	}
	return input
}

// normalizeChEMBLNullSentinels closes a provider-specific ambiguity in the
// captured ChEMBL schemas: optional identifiers and structures accept both
// string and null, so the generic normalizer must preserve the literal text
// "None" as a potentially valid string. ChEMBL never uses that sentinel as a
// real identifier, structure, unit, or filter; sending it upstream instead
// produces misleading 400s such as /substructure/None.json. Normalize only
// nullable fields on this connector so unrelated free-text tools keep their
// literal-string contract.
func normalizeChEMBLNullSentinels(schema map[string]any, input map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	nullableFields := map[string]struct{}{}
	for _, field := range []string{
		"chembl_id", "smiles", "max_phase", "similarity_threshold",
		"molecule_chembl_id", "target_chembl_id", "activity_type", "unit",
		"min_pchembl", "max_pchembl", "min_value", "max_value", "action_type",
		"gene_symbol", "target_name", "organism", "target_type",
	} {
		nullableFields[field] = struct{}{}
	}
	var normalized map[string]any
	for field, rawValue := range input {
		if _, known := nullableFields[field]; !known {
			continue
		}
		text, ok := rawValue.(string)
		if !ok || !isAgentRuntimeNullSentinel(text) {
			continue
		}
		property, _ := properties[field].(map[string]any)
		if !agentRuntimeSchemaAllowsJSONType(property, "null") {
			continue
		}
		if normalized == nil {
			normalized = copyMapAny(input)
		}
		normalized[field] = nil
	}
	if normalized != nil {
		return normalized
	}
	return input
}

func isAgentRuntimeNullSentinel(value string) bool {
	trimmed := strings.TrimSpace(value)
	return strings.EqualFold(trimmed, "null") || strings.EqualFold(trimmed, "none")
}

func agentRuntimeSchemaAllowsJSONType(schema map[string]any, wanted string) bool {
	for _, current := range agentRuntimeSchemaJSONTypes(schema) {
		if current == wanted {
			return true
		}
	}
	return false
}

// normalizeClinicalTrialsSearchArguments preserves the strict published
// schema while accepting bounded result-limit aliases emitted by different
// provider/model tool descriptions. This is intentionally scoped to this
// exact tool; arbitrary unknown fields must continue to fail validation rather
// than being dropped.
func normalizeClinicalTrialsSearchArguments(input map[string]any) map[string]any {
	maxRows, hasMaxRows := input["max_rows"]
	maxResults, hasMaxResults := input["max_results"]
	if !hasMaxRows && !hasMaxResults {
		return input
	}
	normalized := copyMapAny(input)
	_, hasPageSize := normalized["page_size"]
	if hasPageSize {
		// The canonical field is authoritative whenever it is present. Keeping
		// a stale legacy alias solely because its value differs turns an
		// otherwise valid call into an additionalProperties failure and causes
		// model correction loops. The canonical value still undergoes the full
		// admitted schema validation below.
		delete(normalized, "max_rows")
		delete(normalized, "max_results")
		return normalized
	}
	alias := maxResults
	if hasMaxRows {
		alias = maxRows
	}
	normalized["page_size"] = normalizeLegacyIntegerArgument(alias)
	delete(normalized, "max_rows")
	delete(normalized, "max_results")
	return normalized
}

// normalizeClinicalTrialsMCPEnvelope applies the same narrowly scoped legacy
// alias rule when an older model reaches the tool through the generic MCPTool
// envelope. Both input and arguments are accepted by the runtime contract;
// only the exact clinical-trials/search_trials pair is changed.
func normalizeClinicalTrialsMCPEnvelope(input map[string]any) map[string]any {
	serverName := strings.ToLower(strings.TrimSpace(stringValue(input["server"])))
	serverName = strings.ReplaceAll(serverName, "_", "-")
	toolName := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		stringValue(input["toolName"]), stringValue(input["tool_name"]),
	)))
	if serverName != "clinical-trials" || toolName != "search_trials" {
		return input
	}
	normalized := copyMapAny(input)
	for _, field := range []string{"input", "arguments"} {
		arguments := objectMapValue(input[field])
		if len(arguments) == 0 {
			continue
		}
		normalized[field] = normalizeClinicalTrialsSearchArguments(arguments)
	}
	return normalized
}

func normalizeLegacyIntegerArgument(value any) any {
	if parsed, ok := admittedIntegerArgument(value); ok {
		return parsed
	}
	return value
}

func admittedIntegerArgument(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case float32:
		parsed := int64(typed)
		if float32(parsed) == typed {
			return parsed, true
		}
	case float64:
		parsed := int64(typed)
		if float64(parsed) == typed {
			return parsed, true
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, true
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func normalizeAgentRuntimeObject(schema map[string]any, input map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 || len(input) == 0 {
		return input
	}
	normalized := copyMapAny(input)
	if additionalProperties, explicitlyBounded := schema["additionalProperties"].(bool); explicitlyBounded && !additionalProperties {
		for name, value := range input {
			if _, admitted := properties[name]; admitted {
				continue
			}
			// Human descriptions are presentation metadata used to label a tool
			// step in the stream; they never change execution semantics. Some
			// provider adapters include that metadata even when a strict external
			// tool schema (for example web_fetch) does not declare it. Preserve the
			// original input for audit/UI, but remove the non-executable field from
			// the admitted execution payload instead of publishing a false failure.
			if isAgentRuntimePresentationMetadata(name) {
				delete(normalized, name)
				continue
			}
			// Model providers occasionally emit empty placeholder properties while
			// filling a strict tool schema (for example, an unused alternate status
			// field on every array item). An empty value cannot carry executable
			// intent, so omit it before validation instead of spending another model
			// round on a deterministic correction. Non-empty unknown properties still
			// fail closed so misspelled or unsupported behavior is never hidden.
			if isEmptyAgentRuntimePlaceholder(value) {
				delete(normalized, name)
				continue
			}
			baseName, companion := agentRuntimeLocaleCompanionBase(name)
			if !companion {
				continue
			}
			if _, baseAdmitted := properties[baseName]; !baseAdmitted {
				continue
			}
			baseValue, basePresent := input[baseName]
			if !basePresent || !isRedundantAgentRuntimeLocaleCompanion(value, baseValue) {
				continue
			}
			delete(normalized, name)
		}
	}
	for name, rawSchema := range properties {
		property, _ := rawSchema.(map[string]any)
		value, exists := normalized[name]
		if !exists || len(property) == 0 {
			continue
		}
		normalized[name] = normalizeAgentRuntimeValue(property, value)
	}
	return normalized
}

func isAgentRuntimePresentationMetadata(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "human_description", "humandescription":
		return true
	default:
		return false
	}
}

func isEmptyAgentRuntimePlaceholder(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// agentRuntimeLocaleCompanionBase recognizes only the locale suffixes emitted
// by supported product locales. It deliberately does not accept arbitrary
// underscore suffixes: unknown arguments must continue to fail closed.
func agentRuntimeLocaleCompanionBase(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, suffix := range []string{"_zh_hans", "_zh_hant", "_zh_cn", "_en_us", "_en_gb", "_zh", "_en"} {
		if strings.HasSuffix(lower, suffix) && len(name) > len(suffix) {
			return name[:len(name)-len(suffix)], true
		}
	}
	return "", false
}

func isRedundantAgentRuntimeLocaleCompanion(value any, baseValue any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	if text == "" {
		return true
	}
	baseText, baseIsString := baseValue.(string)
	return baseIsString && text == baseText
}

func normalizeAgentRuntimeArray(schema map[string]any, input []any) []any {
	items, _ := schema["items"].(map[string]any)
	if len(items) == 0 || len(input) == 0 {
		return input
	}
	normalized := make([]any, len(input))
	for index, value := range input {
		normalized[index] = normalizeAgentRuntimeValue(items, value)
	}
	return normalized
}

func normalizeAgentRuntimeValue(schema map[string]any, value any) any {
	text, isString := value.(string)
	if !isString {
		if object, ok := value.(map[string]any); ok {
			return normalizeAgentRuntimeObject(schema, object)
		}
		if array, ok := value.([]any); ok {
			return normalizeAgentRuntimeArray(schema, array)
		}
		return value
	}
	types := agentRuntimeSchemaJSONTypes(schema)
	allows := func(wanted string) bool {
		for _, current := range types {
			if current == wanted {
				return true
			}
		}
		return false
	}
	trimmed := strings.TrimSpace(text)
	nullSentinel := strings.EqualFold(trimmed, "null") || strings.EqualFold(trimmed, "none")
	if nullSentinel && allows("null") && !agentRuntimeSchemaAcceptsStringValue(schema, trimmed) {
		return nil
	}
	if allows("integer") && !allows("string") {
		if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return parsed
		}
	}
	if allows("number") && !allows("string") {
		if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return parsed
		}
	}
	if allows("boolean") && !allows("string") {
		if parsed, err := strconv.ParseBool(trimmed); err == nil {
			return parsed
		}
	}
	if (allows("array") || allows("object")) && !allows("string") {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		if decoder.Decode(&decoded) == nil {
			switch typed := decoded.(type) {
			case []any:
				if allows("array") {
					return normalizeAgentRuntimeArray(schema, typed)
				}
			case map[string]any:
				if allows("object") {
					return normalizeAgentRuntimeObject(schema, typed)
				}
			}
		}
		if allows("array") {
			if items, ok := schema["items"].(map[string]any); ok {
				itemTypes := agentRuntimeSchemaJSONTypes(items)
				if len(itemTypes) == 1 && itemTypes[0] == "string" && trimmed != "" {
					return []any{text}
				}
			}
		}
	}
	return value
}

func agentRuntimeSchemaAcceptsStringValue(schema map[string]any, value string) bool {
	types := agentRuntimeSchemaJSONTypes(schema)
	stringAllowed := false
	for _, current := range types {
		if current == "string" {
			stringAllowed = true
			break
		}
	}
	if !stringAllowed {
		return false
	}
	if constant, ok := schema["const"].(string); ok && constant == value {
		return true
	}
	if _, restricted := schema["const"]; restricted {
		return false
	}
	if _, restricted := schema["enum"]; restricted {
		for _, candidate := range anySliceValue(schema["enum"]) {
			if text, ok := candidate.(string); ok && text == value {
				return true
			}
		}
		return false
	}
	return true
}

const maxAgentRuntimeMCPValidationIssues = 8

type agentRuntimeMCPValidator struct {
	toolName   string
	schema     map[string]any
	compiled   *jsonschema.Schema
	compileErr error
}

func agentRuntimeToolValidators(schemas []agentruntime.ToolSchema) map[string]agentRuntimeMCPValidator {
	validators := make(map[string]agentRuntimeMCPValidator)
	for _, schema := range schemas {
		name := strings.TrimSpace(schema.Name)
		if name != "" {
			validators[name] = compileAgentRuntimeMCPValidator(schema)
		}
	}
	return validators
}

func (g serverAgentRuntimeToolGateway) validateAdmittedToolArguments(name string, input map[string]any) map[string]any {
	if validator, found := g.toolValidators[name]; found {
		return validator.Validate(input)
	}
	for _, schema := range g.toolSchemas {
		if schema.Name == name {
			return compileAgentRuntimeMCPValidator(schema).Validate(input)
		}
	}
	return nil
}

func compileAgentRuntimeMCPValidator(schema agentruntime.ToolSchema) agentRuntimeMCPValidator {
	validator := agentRuntimeMCPValidator{toolName: strings.TrimSpace(schema.Name), schema: schema.Parameters}
	validator.compiled, validator.compileErr = compileKernelDraft7Schema(kernelMCPOracleInputSchema(schema.Parameters))
	return validator
}

func sanitizedAgentRuntimeValidationIssues(raw any) []map[string]any {
	issues := make([]map[string]any, 0, maxAgentRuntimeMCPValidationIssues)
	for _, item := range anySliceValue(raw) {
		entry := objectMapValue(item)
		if len(entry) == 0 {
			continue
		}
		safe := map[string]any{}
		for _, key := range []string{"path", "keyword", "message", "expected", "actual_type"} {
			if value, ok := entry[key]; ok && value != nil {
				safe[key] = value
			}
		}
		if len(safe) != 0 {
			issues = append(issues, safe)
		}
		if len(issues) >= maxAgentRuntimeMCPValidationIssues {
			break
		}
	}
	return issues
}

func (v agentRuntimeMCPValidator) Validate(input map[string]any) map[string]any {
	if v.compileErr != nil || v.compiled == nil {
		return map[string]any{
			"ok": false, "code": "tool_schema_invalid", "tool": v.toolName,
			"message": "The admitted tool schema is invalid.", "retryable": false,
		}
	}
	validator := &kernelMCPInputValidator{schema: v.compiled}
	if err := validator.Validate(input); err != nil {
		return map[string]any{
			"ok": false, "code": "invalid_tool_arguments", "tool": v.toolName,
			"message":   "Tool arguments do not match the admitted JSON Schema.",
			"retryable": true, "issues": agentRuntimeMCPValidationIssues(v.schema, input, err),
			"expectedArguments": agentRuntimeCompactArgumentContract(v.schema),
		}
	}
	return nil
}

func agentRuntimeCompactArgumentContract(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	for _, raw := range anySliceValue(schema["required"]) {
		if name := strings.TrimSpace(fmt.Sprint(raw)); name != "" {
			required[name] = true
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 24 {
		names = names[:24]
	}
	fields := make(map[string]any, len(names))
	for _, name := range names {
		property, _ := properties[name].(map[string]any)
		field := map[string]any{"types": agentRuntimeSchemaJSONTypes(property), "required": required[name]}
		if values, ok := property["enum"].([]any); ok && len(values) > 0 && len(values) <= 12 {
			field["enum"] = values
		}
		fields[name] = field
	}
	return map[string]any{"type": "object", "fields": fields, "additionalProperties": schema["additionalProperties"]}
}

func agentRuntimeMCPValidationIssues(schema map[string]any, input map[string]any, err error) []map[string]any {
	root, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []map[string]any{{"path": "", "keyword": "schema", "actual": agentRuntimeJSONType(input)}}
	}
	leaves := make([]*jsonschema.ValidationError, 0, maxAgentRuntimeMCPValidationIssues)
	var visit func(*jsonschema.ValidationError)
	visit = func(current *jsonschema.ValidationError) {
		if current == nil || len(leaves) >= maxAgentRuntimeMCPValidationIssues {
			return
		}
		if len(current.Causes) == 0 {
			leaves = append(leaves, current)
			return
		}
		for _, cause := range current.Causes {
			visit(cause)
		}
	}
	visit(root)
	issues := make([]map[string]any, 0, len(leaves))
	seen := map[string]struct{}{}
	for _, leaf := range leaves {
		if required, ok := leaf.ErrorKind.(*kind.Required); ok {
			for _, missing := range required.Missing {
				if len(issues) >= maxAgentRuntimeMCPValidationIssues {
					break
				}
				pathParts := append(append([]string(nil), leaf.InstanceLocation...), missing)
				path := agentRuntimeJSONPointer(pathParts)
				expected := agentRuntimeSchemaJSONTypes(agentRuntimeSchemaAt(schema, pathParts))
				key := path + "\x00required\x00" + strings.Join(expected, ",") + "\x00missing"
				if _, found := seen[key]; found {
					continue
				}
				seen[key] = struct{}{}
				issue := map[string]any{"path": path, "keyword": "required", "actual": "missing"}
				if len(expected) > 0 {
					issue["expected"] = expected
				}
				issues = append(issues, issue)
			}
			continue
		}
		path := agentRuntimeJSONPointer(leaf.InstanceLocation)
		keyword := "schema"
		if leaf.ErrorKind != nil {
			keywordPath := leaf.ErrorKind.KeywordPath()
			if len(keywordPath) > 0 {
				keyword = keywordPath[len(keywordPath)-1]
			}
		}
		expected := agentRuntimeSchemaJSONTypes(agentRuntimeSchemaAt(schema, leaf.InstanceLocation))
		actual := agentRuntimeJSONType(agentRuntimeValueAt(input, leaf.InstanceLocation))
		key := path + "\x00" + keyword + "\x00" + strings.Join(expected, ",") + "\x00" + actual
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		issue := map[string]any{"path": path, "keyword": keyword, "actual": actual}
		if len(expected) > 0 {
			issue["expected"] = expected
		}
		issues = append(issues, issue)
	}
	// When an object is missing a required field and also carries a field that
	// belongs to another tool contract, lead with the actionable missing-field
	// repair. Reporting the parent additionalProperties error as a second issue
	// made models oscillate between deleting and adding fields instead of
	// producing the one admitted object.
	requiredParents := map[string]struct{}{}
	for _, issue := range issues {
		if issue["keyword"] != "required" {
			continue
		}
		path := fmt.Sprint(issue["path"])
		parent := ""
		if index := strings.LastIndex(path, "/"); index > 0 {
			parent = path[:index]
		}
		requiredParents[parent] = struct{}{}
	}
	if len(requiredParents) > 0 {
		filtered := issues[:0]
		for _, issue := range issues {
			if issue["keyword"] == "additionalProperties" {
				if _, redundant := requiredParents[fmt.Sprint(issue["path"])]; redundant {
					continue
				}
			}
			filtered = append(filtered, issue)
		}
		issues = filtered
	}
	if len(issues) == 0 {
		issues = append(issues, map[string]any{"path": "", "keyword": "schema", "actual": agentRuntimeJSONType(input)})
	}
	sort.SliceStable(issues, func(i, j int) bool {
		left, right := fmt.Sprint(issues[i]["path"]), fmt.Sprint(issues[j]["path"])
		if left != right {
			return left < right
		}
		return fmt.Sprint(issues[i]["keyword"]) < fmt.Sprint(issues[j]["keyword"])
	})
	return issues
}

func agentRuntimeJSONPointer(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	escaped := make([]string, len(parts))
	for index, part := range parts {
		escaped[index] = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(escaped, "/")
}

func agentRuntimeSchemaAt(schema map[string]any, path []string) map[string]any {
	current := schema
	for _, part := range path {
		if properties, ok := current["properties"].(map[string]any); ok {
			if child, ok := properties[part].(map[string]any); ok {
				current = child
				continue
			}
		}
		if items, ok := current["items"].(map[string]any); ok {
			current = items
			continue
		}
		return nil
	}
	return current
}

func agentRuntimeValueAt(input map[string]any, path []string) any {
	var current any = input
	for _, part := range path {
		switch value := current.(type) {
		case map[string]any:
			current = value[part]
		case []any:
			var index int
			if _, err := fmt.Sscanf(part, "%d", &index); err != nil || index < 0 || index >= len(value) {
				return nil
			}
			current = value[index]
		default:
			return nil
		}
	}
	return current
}

func agentRuntimeSchemaJSONTypes(schema map[string]any) []string {
	if len(schema) == 0 {
		return nil
	}
	types := map[string]struct{}{}
	appendType := func(value any) {
		switch typed := value.(type) {
		case string:
			if typed = strings.TrimSpace(typed); typed != "" {
				types[typed] = struct{}{}
			}
		case []any:
			for _, item := range typed {
				if name, ok := item.(string); ok && strings.TrimSpace(name) != "" {
					types[strings.TrimSpace(name)] = struct{}{}
				}
			}
		}
	}
	appendType(schema["type"])
	for _, unionKey := range []string{"anyOf", "oneOf"} {
		if alternatives, ok := schema[unionKey].([]any); ok {
			for _, raw := range alternatives {
				if alternative, ok := raw.(map[string]any); ok {
					appendType(alternative["type"])
				}
			}
		}
	}
	out := make([]string, 0, len(types))
	for name := range types {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func agentRuntimeJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case []any, []string:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func agentRuntimeMCPToolSuggestion(requested string, schemas []agentruntime.ToolSchema) string {
	requested = strings.TrimSpace(requested)
	if !strings.HasPrefix(strings.ToLower(requested), "mcp__") {
		return ""
	}
	for _, schema := range schemas {
		if strings.EqualFold(strings.TrimSpace(schema.Name), requested) {
			return strings.TrimSpace(schema.Name)
		}
	}
	terminal := agentRuntimeToolTerminalName(requested)
	match := ""
	for _, schema := range schemas {
		candidate := strings.TrimSpace(schema.Name)
		if candidate == "" {
			continue
		}
		candidateTerminal := agentRuntimeToolTerminalName(candidate)
		if !strings.EqualFold(candidateTerminal, terminal) {
			continue
		}
		if match != "" {
			return ""
		}
		match = candidate
	}
	return match
}

func agentRuntimeToolTerminalName(name string) string {
	if separator := strings.LastIndex(name, "__"); separator >= 0 {
		return name[separator+2:]
	}
	return name
}

func agentRuntimeToolErrorValue(err error) map[string]any {
	value := map[string]any{"ok": false, "error": err.Error()}
	// Outbound security denials are deterministic policy boundaries, not
	// transient source outages. Preserve the machine-readable code and make the
	// call non-retryable so the generic failed-call guard can stop prompt-only
	// retries of the same denied destination.
	errorText := strings.TrimSpace(err.Error())
	if strings.HasPrefix(strings.ToLower(errorText), "secure_fetch_") {
		value["code"] = errorText
		failurecontract.ApplyTerminalJobFailure(value, errorText)
		value["recovery"] = "use an allowed same-origin public URL or a source-specific tool; do not retry the denied redirect"
		return value
	}
	var softwareErr *software.OperationError
	if errors.As(err, &softwareErr) && softwareErr != nil {
		value["code"] = strings.TrimSpace(softwareErr.Code)
		value["message"] = strings.TrimSpace(softwareErr.Message)
		value["recovery"] = strings.TrimSpace(softwareErr.Recovery)
		value["repair_scope"] = strings.TrimSpace(softwareErr.RepairScope)
		failurecontract.ApplyTerminalJobFailure(value, softwareErr.Code)
		return value
	}
	var hostErr *kernelruntime.HostCallError
	if errors.As(err, &hostErr) && hostErr != nil {
		value["code"] = strings.TrimSpace(hostErr.Code)
		value["message"] = strings.TrimSpace(hostErr.Message)
		failurecontract.ApplyTerminalJobFailure(value, hostErr.Code)
		switch strings.TrimSpace(hostErr.Code) {
		case "invalid_arguments":
			value["recovery"] = "inspect_the_diagnostic_change_the_input_or_parameters_then_retry_only_the_failed_step"
		case "unavailable", "storage_error", "connector_error":
			value["recovery"] = "inspect_or_repair_the_governed_runtime_then_retry_the_same_task_step"
		case "permission_denied", "policy_blocked", "approval_unavailable":
			value["recovery"] = "obtain_the_required_approval_or_change_the_authorized_plan_before_retrying"
		default:
			value["recovery"] = "classify_the_failure_before_deciding_whether_a_new_task_step_is_safe"
		}
		if strings.Contains(strings.ToLower(hostErr.Message), "not prepared pdbqt") {
			value["recovery"] = "prepare_the_source_structure_as_valid_pdbqt_in_the_governed_scientific_environment_then_start_a_new_execution_with_new_artifact_versions"
			value["repair_scope"] = "same_logical_task"
		}
		return value
	}
	if err.Error() == "edit_file old_string was not found" || err.Error() == "edit_file old_string must occur exactly once" {
		return agentRuntimeEditConflictValue()
	}
	if errors.Is(err, errAgentFileContentTypeMismatch) || errors.Is(err, errAgentFileStructureInvalid) {
		value["code"] = "file_content_type_mismatch"
		if errors.Is(err, errAgentFileStructureInvalid) {
			value["code"] = "invalid_file_structure"
		}
		value["executed"] = false
		value["retryable"] = true
		value["recovery"] = "The invalid content was not written. Read the existing file; if it already contains the requested data, save it directly. Otherwise supply valid content for the declared format or use the correct format extension."
		return value
	}
	if strings.Contains(strings.ToLower(err.Error()), "requires a contact email address") {
		value["code"] = "contact_email_required"
		value["message"] = "A contact email must be configured before this source can be queried."
		value["recovery"] = "configure_contact_email"
		value["settingsPath"] = "/settings/general"
		value["retryable"] = false
	}
	return value
}

func agentRuntimeEditConflictValue() map[string]any {
	return map[string]any{
		"ok":        false,
		"executed":  false,
		"status":    "edit_preflight_required",
		"code":      "edit_conflict",
		"message":   "The targeted edit did not match the current file exactly once.",
		"retryable": true,
		"recovery":  "If the intent is a full-file rewrite, retry once with old_string empty and the complete new content. Otherwise call read_file, then retry once with the smallest current exact substring that is unique. Never copy the whole document into old_string and never repeat the stale call.",
	}
}

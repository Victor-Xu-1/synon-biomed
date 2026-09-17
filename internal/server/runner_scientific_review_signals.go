package server

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/networkpolicy"
	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
	"synon-go/internal/toolcontract"
)

const trustedScientificReviewSignalsField = "trustedScientificReviewSignals"

// This signal is emitted only after download_public_scientific_file has
// verified that the requested URL was authorized by durable source evidence,
// fetched the payload through the governed network boundary, and published a
// durable artifact. Unlike source-host signals, it does not require every
// legitimate scientific repository to be hard-coded into the application.
const trustedScientificValidatedPublicDownloadSignal = "source-authority:validated-public-download"

// A successful WebFetch is already admitted and executed by the server's
// governed fetch boundary. Preserve that durable receipt without turning the
// completion gate into a repository-name allowlist. This signal proves source
// provenance and successful retrieval; scientific quality remains a property
// of the cited content and the user-visible result.
const trustedScientificValidatedPublicWebFetchSignal = "source-authority:validated-public-web-fetch"

// WebResearch may return both discovery candidates and fetched documents in
// one result. Only a public, complete, substantive read receipt authorizes the
// latter as source evidence; titles, snippets, thin shells, and truncated
// prefixes remain discovery evidence.
const trustedScientificValidatedWebResearchReadSignal = "source-authority:validated-web-research-read"

// A governed search result may carry a substantial provider-returned excerpt
// from a public source even when the full text is not open access. This signal
// authorizes only the exact durable excerpt and its source identity; it does
// not upgrade discovery metadata into full-document evidence. The independent
// reviewer remains responsible for rejecting claims that exceed the excerpt.
const trustedScientificValidatedSearchExcerptSignal = "source-authority:validated-search-excerpt"

const trustedScientificRequiredCapabilitySignalPrefix = "required-capability:"

// Exact record-depth signals are compact server-derived witnesses retained
// when a substantive publication, trial, or patent result is externalized.
// They let later correction rounds distinguish a full record read from a
// title-only discovery hit without replaying a potentially very large result.
const trustedScientificEvidenceRecordSignalPrefix = "evidence-record:"

// Discovery continuity is deliberately non-authoritative. It remembers only
// that an exact patent identifier was returned by a task-scoped discovery so a
// later full record read can be linked across language and compaction
// boundaries. The discovery signal alone never satisfies completion.
const trustedScientificPatentDiscoverySignalPrefix = "evidence-discovery:patent:"
const trustedScientificTrialDiscoverySignalPrefix = "evidence-discovery:trial:"
const trustedScientificPublicationDiscoverySignalPrefix = "evidence-discovery:publication:"

// Route exhaustion is not a failure or a weaker evidence grade. It records
// that one governed transport returned a real exact record without the
// substantive fields required for this source class. Persisting this compact
// fact lets the next bounded execution unit advance to another existing route
// instead of restarting the same metadata-only read.
const trustedScientificEvidenceRouteExhaustedSignalPrefix = "evidence-route-exhausted:"

// A source-grounded execution signal is emitted only when a successful local
// scientific execution happens after authoritative source evidence is already
// durable for the same logical task and the execution materializes at least
// one output. This preserves the causal distinction between preparing an
// environment and actually analysing acquired data. The signal is generic:
// it does not encode a dataset, domain, repository, or task-specific workflow.
const trustedScientificSourceGroundedExecutionSignalPrefix = "source-grounded-execution:"

// User attachments are authoritative for the bytes the user supplied, but
// they are not public-source evidence. Keep both the input receipt and causal
// execution receipt distinct so an unrelated attachment can never satisfy an
// explicit literature, database, or public-source request.
const trustedScientificUserArtifactInputSignal = "input-authority:user-artifact"
const trustedScientificUserArtifactGroundedExecutionSignalPrefix = "input-grounded-execution:"

// A named managed environment is the same governed execution authority whether
// the model reaches it through the generic Python/R/Bash surface or through the
// lower-level software_runtime surface. Preserve that durable distinction so a
// runtime acceptance does not reject a successful managed kernel merely because
// the model selected the language-native tool.
const trustedScientificManagedEnvironmentExecutionSignalPrefix = "managed-environment-execution:"

func (run *sessionRunnerChatRun) addTrustedScientificReviewSignals(signals ...string) {
	if run == nil || len(signals) == 0 {
		return
	}
	normalized := normalizeTrustedScientificReviewSignals(signals)
	reviewSignals := make([]string, 0, len(normalized))
	requiredCapabilities := make([]string, 0)
	for _, signal := range normalized {
		if strings.HasPrefix(signal, trustedScientificRequiredCapabilitySignalPrefix) {
			requiredCapabilities = append(requiredCapabilities,
				strings.TrimPrefix(signal, trustedScientificRequiredCapabilitySignalPrefix))
			continue
		}
		reviewSignals = append(reviewSignals, signal)
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.TrustedScientificReviewSignals = uniqueSortedFolded(append(
		append([]string(nil), run.TrustedScientificReviewSignals...), reviewSignals...,
	))
	run.RequiredScientificCapabilities = uniqueSortedScientificCapabilities(append(
		append([]string(nil), run.RequiredScientificCapabilities...), requiredCapabilities...,
	))
}

func (run *sessionRunnerChatRun) trustedScientificReviewSignalsSnapshot() []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.TrustedScientificReviewSignals...)
}

func (s *Server) trustedScientificReviewSignalsForCompletedTool(
	toolName string,
	input any,
	result any,
	attestation *workspaceMCPSourceEvidenceAttestation,
) []string {
	routeExhaustionSignals := trustedScientificEvidenceRouteExhaustionSignals(toolName, input, result)
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return routeExhaustionSignals
	}
	signals := make([]string, 0, 4)
	if attestation != nil {
		signals = append(signals, trustedScientificReviewSignalForMCPAttestation(*attestation)...)
	}
	signals = append(signals, trustedScientificEvidenceRecordSignals(toolName, input, result)...)
	signals = append(signals, trustedScientificPatentDiscoverySignals(toolName, input, result)...)
	signals = append(signals, routeExhaustionSignals...)

	normalizedTool := normalizeAgentToolName(toolName)
	if runtimeToolContractHasCapability(toolName, "scientific-system-tool") {
		signals = append(signals, "scientific-tool:"+strings.ToLower(strings.TrimSpace(toolName)))
	}
	inputMap, _ := input.(map[string]any)
	switch normalizedTool {
	case "python", "r", "bash", "repl":
		resultMap, _ := result.(map[string]any)
		nonExecutingPreflight := resultMap != nil && resultMap["executed"] == false
		if !nonExecutingPreflight && runtimeToolContractHasCapability(toolName, "runtime-execution") {
			signals = append(signals, "execution-tool:"+normalizedTool)
		}
		if trustedScientificManagedEnvironmentExecution(toolName, input, result) {
			signals = append(signals, trustedScientificManagedEnvironmentExecutionSignalPrefix+normalizedTool)
		}
	case "softwareruntime":
		if verifiedSoftwareRuntimeExecutionResult(result) {
			signals = append(signals, "execution-tool:"+softwareRuntimeToolName)
			if witness, found := s.trustedScientificCapabilityWitnessForCompletedTool(toolName, input, result); found {
				signals = append(signals, "capability-execution:"+witness.Capability)
			}
		}
	case "downloadrcsbfile":
		// The host-side RCSB download is the authoritative source boundary for
		// coordinate and ligand-definition files. Preserve that provenance as a
		// source signal instead of treating the tool as an ungrounded artifact
		// writer; the completion gate must distinguish a real RCSB source from
		// a file that merely happens to have a .cif/.pdb extension.
		signals = append(signals, "source-host:files.rcsb.org")
	case "searchrcsbstructures":
		if trustedScientificRCSBSearchResult(result) {
			signals = append(signals, "source-host:search.rcsb.org", "source-host:data.rcsb.org")
		}
	case "downloadpublicscientificfile":
		signals = append(signals, trustedScientificValidatedPublicDownloadSignals(inputMap, result)...)
	case "webfetch":
		validated := trustedScientificValidatedPublicWebFetchSignals(inputMap, result)
		if len(validated) > 0 {
			signals = append(signals, trustedScientificReviewSignalForURL(stringValue(inputMap["url"]))...)
			signals = append(signals, validated...)
		}
	case "websearch", "webresearch":
		validated := trustedScientificValidatedSearchExcerptSignals(result)
		if normalizedTool == "webresearch" {
			validated = append(validated, trustedScientificValidatedWebResearchReadSignals(result)...)
		}
		if len(validated) > 0 {
			collectTrustedScientificReviewURLSignals(result, 0, new(int), &signals)
			signals = append(signals, validated...)
		}
	case "fetcharticlefulltext":
		if trustedScientificFullTextAvailable(result) {
			collectTrustedScientificReviewURLSignals(result, 0, new(int), &signals)
		}
	case "saveartifacts", "artifactregister":
		signals = append(signals, trustedScientificReviewArtifactSignals(inputMap)...)
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificEvidenceRecordSignals(toolName string, input, result any) []string {
	arguments, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	callID := "evidence-record-signal"
	index := runnerEvidenceRecordDepthIndexFromMessages([]agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: callID, Name: toolName, Arguments: arguments, VerifiedEvidence: true,
		}}},
		{Role: "tool", ToolCallID: callID, Content: string(encodedResult)},
	})
	signals := make([]string, 0, len(index.patents)+len(index.trials)+len(index.publications)+len(index.web))
	for identifier := range index.patents {
		signals = append(signals, trustedScientificEvidenceRecordSignalPrefix+"patent:"+identifier)
	}
	for identifier := range index.trials {
		signals = append(signals, trustedScientificEvidenceRecordSignalPrefix+"trial:"+identifier)
	}
	for identifier := range index.publications {
		signals = append(signals, trustedScientificEvidenceRecordSignalPrefix+"publication:"+identifier)
	}
	for identifier := range index.web {
		signals = append(signals, trustedScientificEvidenceRecordSignalPrefix+"web:"+identifier)
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificEvidenceRouteExhaustionSignals(toolName string, input, result any) []string {
	if len(trustedScientificEvidenceRecordSignals(toolName, input, result)) > 0 {
		return nil
	}
	normalized := normalizeAgentToolName(toolName)
	inputMap, _ := input.(map[string]any)
	if normalized == "patentsearch" &&
		strings.EqualFold(strings.TrimSpace(stringValue(inputMap["operation"])), "search") {
		object := runnerCorrectionResultObject(result)
		retrieval := mapValue(object["retrieval"])
		if object != nil && boolValue(retrieval["complete"], false) &&
			len(anySliceValue(object["records"])) == 0 {
			return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "patent:patentsearch"}
		}
	}
	if normalized == "fetcharticlefulltext" {
		object := runnerCorrectionResultObject(result)
		if object != nil {
			available, recorded := object["available"].(bool)
			if recorded && !available && !boolValue(object["recordAvailable"], false) {
				return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "publication:fetcharticlefulltext"}
			}
		}
	}
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return nil
	}
	if runnerEvidenceDepthStructuredPublicationTool(normalized) {
		identifier := runnerEvidenceCanonicalPublication(firstNonEmpty(
			stringValue(inputMap["work_id"]), stringValue(inputMap["id"]), stringValue(inputMap["doi"]),
		))
		if identifier != "" {
			return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "publication:repl"}
		}
	}
	if strings.Contains(normalized, "clinicaltrial") && strings.Contains(normalized, "gettrialdetails") {
		inputMap, _ := input.(map[string]any)
		if sessionRunnerNCTPattern.MatchString(stringValue(inputMap["nct_id"])) {
			return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "trial:repl"}
		}
	}
	if normalized == "webfetch" && len(trustedScientificValidatedPublicWebFetchSignals(inputMap, result)) > 0 {
		urlValue := stringValue(inputMap["url"])
		if runnerEvidenceCanonicalPublication(urlValue) != "" {
			return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "publication:webfetch"}
		}
		if sessionRunnerNCTPattern.MatchString(urlValue) {
			return []string{trustedScientificEvidenceRouteExhaustedSignalPrefix + "trial:webfetch"}
		}
	}
	return nil
}

func trustedScientificEvidenceDiscoverySignals(toolName string, result any) []string {
	normalized := normalizeAgentToolName(toolName)
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	object := runnerEvidenceDepthResultObject(string(encoded))
	if object == nil || !strings.Contains(normalized, "search") {
		return nil
	}
	index := newRunnerEvidenceRecordDepthIndex()
	signals := make([]string, 0)
	switch {
	case strings.Contains(normalized, "clinicaltrial"):
		runnerEvidenceCollectTrialIdentifiers(object, index.trials, 0, new(int))
		for identifier := range index.trials {
			signals = append(signals, trustedScientificTrialDiscoverySignalPrefix+identifier)
		}
	case strings.Contains(normalized, "literature") || strings.Contains(normalized, "pubmed") ||
		strings.Contains(normalized, "openalex"):
		runnerEvidenceCollectPublicationIdentifiers(object, &index, 0, new(int))
		for identifier := range index.publications {
			signals = append(signals, trustedScientificPublicationDiscoverySignalPrefix+identifier)
		}
	}
	return uniqueSortedFolded(signals)
}

func runnerEvidenceCollectTrialIdentifiers(value any, identifiers map[string]struct{}, depth int, visited *int) {
	if identifiers == nil || visited == nil || depth > 10 || *visited >= 2048 {
		return
	}
	(*visited)++
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if normalizeAgentToolName(key) == "nctid" {
				if identifier := strings.ToUpper(sessionRunnerNCTPattern.FindString(stringValue(child))); identifier != "" {
					identifiers[identifier] = struct{}{}
				}
			}
			runnerEvidenceCollectTrialIdentifiers(child, identifiers, depth+1, visited)
		}
	case []any:
		for _, child := range typed {
			runnerEvidenceCollectTrialIdentifiers(child, identifiers, depth+1, visited)
		}
	}
}

// bindTrustedScientificCompletion keeps the live completion gate and the
// immutable tool checkpoint on one evidence path. Approval recovery, detached
// settlement, ordinary tool execution, and restart reconciliation must all
// derive scientific state from the same server-verified terminal result.
func (s *Server) bindTrustedScientificCompletion(
	run *sessionRunnerChatRun,
	details map[string]any,
	attestation *workspaceMCPSourceEvidenceAttestation,
) {
	if run == nil || details == nil {
		return
	}
	toolName := stringValue(details["toolName"])
	result, recorded := details["toolResult"]
	if strings.TrimSpace(toolName) == "" || !recorded {
		return
	}
	input := runnerCheckpointExecutedToolInput(details)
	signals := s.trustedScientificReviewSignalsForCompletedTool(toolName, input, result, attestation)
	signals = append(signals, trustedScientificPatentContinuitySignals(
		toolName, input, result, run.trustedScientificReviewSignalsSnapshot(), run.CorrectionDetail,
	)...)
	priorSignals := run.trustedScientificReviewSignalsSnapshot()
	if sessionRunnerHasAuthoritativeSourceEvidence(priorSignals) &&
		trustedScientificExecutionMaterializedOutput(toolName, result) {
		if executionTool := trustedScientificExecutionToolName(toolName, result); executionTool != "" {
			signals = append(signals, trustedScientificSourceGroundedExecutionSignalPrefix+executionTool)
		}
	}
	if trustedScientificExecutionMaterializedOutput(toolName, result) &&
		s.trustedScientificExecutionConsumesUserArtifact(run, priorSignals, toolName, input, result) {
		if executionTool := trustedScientificExecutionToolName(toolName, result); executionTool != "" {
			signals = append(signals, trustedScientificUserArtifactGroundedExecutionSignalPrefix+executionTool)
		}
	}
	if len(signals) > 0 {
		run.addTrustedScientificReviewSignals(signals...)
	}
	// Persist the cumulative server-verified state on the immutable terminal
	// checkpoint. A large result may have bound its signals before the engine
	// emitted the compact descriptor, so recording only signals derived from
	// that descriptor would lose the authoritative receipt on resume.
	if durableSignals := run.trustedScientificReviewSignalsSnapshot(); len(durableSignals) > 0 {
		details[trustedScientificReviewSignalsField] = durableSignals
	}
	if witness, found := s.trustedScientificCapabilityWitnessForCompletedTool(toolName, input, result); found {
		run.addTrustedScientificCapabilityWitnesses(witness)
		details[trustedScientificCapabilityWitnessesField] = []sciencecapability.Witness{witness}
	}
}

func (s *Server) bindTrustedScientificLargeToolResult(
	run *sessionRunnerChatRun,
	input agentruntime.LargeToolResultInput,
) {
	if s == nil || run == nil || len(input.RawJSON) == 0 {
		return
	}
	var result any
	if json.Unmarshal(input.RawJSON, &result) != nil {
		return
	}
	var toolInput any
	if len(input.ToolCall.Arguments) > 0 {
		_ = json.Unmarshal(input.ToolCall.Arguments, &toolInput)
	}
	signals := s.trustedScientificReviewSignalsForCompletedTool(
		input.ToolCall.Name, toolInput, result, nil,
	)
	signals = append(signals, trustedScientificPatentContinuitySignals(
		input.ToolCall.Name, toolInput, result, run.trustedScientificReviewSignalsSnapshot(), run.CorrectionDetail,
	)...)
	// A layered research result can be partially successful because one source
	// was unavailable while other documents were completely read. Preserve only
	// the independently verifiable per-record receipts from that mixed result;
	// never promote the whole partial call to success. This prevents the next
	// execution unit from re-reading sources whose exact deep-read evidence is
	// already durable.
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		signals = append(signals, trustedScientificPartialResultEvidenceSignals(
			input.ToolCall.Name, toolInput, result,
		)...)
	}
	if len(signals) > 0 {
		run.addTrustedScientificReviewSignals(signals...)
	}
}

func trustedScientificPartialResultEvidenceSignals(toolName string, input, result any) []string {
	signals := make([]string, 0)
	switch normalizeAgentToolName(toolName) {
	case "webresearch":
		signals = append(signals, trustedScientificValidatedWebResearchReadSignals(result)...)
		signals = append(signals, trustedScientificEvidenceRecordSignals(toolName, input, result)...)
	case "patentsearch":
		signals = append(signals, trustedScientificEvidenceRecordSignals(toolName, input, result)...)
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificPatentDiscoverySignals(toolName string, input, result any) []string {
	if normalizeAgentToolName(toolName) != "patentsearch" {
		return nil
	}
	inputMap, _ := input.(map[string]any)
	if !strings.EqualFold(strings.TrimSpace(stringValue(inputMap["operation"])), "search") {
		return nil
	}
	object := runnerCorrectionResultObject(result)
	if object == nil {
		return nil
	}
	index := newRunnerEvidenceRecordDepthIndex()
	runnerEvidenceCollectDiscoveredPatents(object, &index)
	signals := make([]string, 0, len(index.discoveredPatents))
	for identifier := range index.discoveredPatents {
		signals = append(signals, trustedScientificPatentDiscoverySignalPrefix+identifier)
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificPatentContinuitySignals(
	toolName string,
	input any,
	result any,
	priorSignals []string,
	correctionDetail string,
) []string {
	if normalizeAgentToolName(toolName) != "patentsearch" {
		return nil
	}
	inputMap, _ := input.(map[string]any)
	if !strings.EqualFold(strings.TrimSpace(stringValue(inputMap["operation"])), "lookup") {
		return nil
	}
	object := runnerCorrectionResultObject(result)
	if object == nil || !strings.EqualFold(strings.TrimSpace(stringValue(object["evidenceDepth"])), "full_record") ||
		!runnerPatentLookupContainsFullRecord(object) {
		return nil
	}
	identifier := runnerEvidenceCanonicalPatent(firstNonEmpty(
		stringValue(inputMap["publicationNumber"]), stringValue(inputMap["publication_number"]),
	))
	if identifier == "" {
		return nil
	}
	want := strings.ToLower(trustedScientificPatentDiscoverySignalPrefix + identifier)
	for _, signal := range priorSignals {
		if strings.ToLower(strings.TrimSpace(signal)) == want {
			return []string{trustedScientificEvidenceRecordSignalPrefix + "patent:" + identifier}
		}
	}
	for _, requirement := range runnerEvidenceRecordRequirementsFromCorrection(correctionDetail) {
		if requirement.class == "patent" && requirement.identifier == identifier {
			return []string{trustedScientificEvidenceRecordSignalPrefix + "patent:" + identifier}
		}
	}
	return nil
}

func trustedScientificExecutionToolName(toolName string, result any) string {
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return ""
	}
	normalized := normalizeAgentToolName(toolName)
	if runtimeToolContractHasCapability(toolName, "scientific-system-tool") {
		return strings.ToLower(strings.TrimSpace(toolName))
	}
	if runtimeToolContractHasCapability(toolName, "runtime-execution") {
		resultMap, _ := softwareRuntimeObjectReceipt(result)
		if resultMap == nil || resultMap["executed"] == false {
			return ""
		}
		return normalized
	}
	if normalized == "softwareruntime" {
		if verifiedSoftwareRuntimeExecutionResult(result) {
			return softwareRuntimeToolName
		}
	}
	return ""
}

func trustedScientificManagedEnvironmentExecution(toolName string, input any, result any) bool {
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return false
	}
	normalizedTool := normalizeAgentToolName(toolName)
	if normalizedTool != "python" && normalizedTool != "r" && normalizedTool != "bash" {
		return false
	}
	inputMap, _ := input.(map[string]any)
	environment := strings.TrimSpace(stringValue(inputMap["environment"]))
	if environment == "" || agentKernelSystemPythonEnvironment(environment) {
		return false
	}
	receipt, ok := softwareRuntimeObjectReceipt(result)
	if !ok || !strings.EqualFold(strings.TrimSpace(stringValue(receipt["exit_status"])), "ok") {
		return false
	}
	return strings.TrimSpace(stringValue(receipt["kernel_id"])) != "" &&
		strings.TrimSpace(stringValue(receipt["exec_id"])) != ""
}

func trustedScientificExecutionMaterializedOutput(toolName string, result any) bool {
	if trustedScientificExecutionToolName(toolName, result) == "" {
		return false
	}
	resultMap, ok := softwareRuntimeObjectReceipt(result)
	if !ok {
		return false
	}
	if runtimeToolContractHasCapability(toolName, "scientific-system-tool") {
		return true
	}
	for _, key := range []string{"files_written", "outputs", "artifacts"} {
		if values, ok := resultMap[key].([]any); ok && len(values) > 0 {
			return true
		}
		encoded, err := json.Marshal(resultMap[key])
		if err != nil || string(encoded) == "null" || string(encoded) == "[]" || string(encoded) == "{}" {
			continue
		}
		var values []json.RawMessage
		if json.Unmarshal(encoded, &values) == nil && len(values) > 0 {
			return true
		}
	}
	return false
}

func trustedScientificRCSBSearchResult(rawResult any) bool {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok || !isSHA256Hex(stringValue(result["search_request_sha256"])) {
		return false
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(stringValue(result["retrieved_at"]))); err != nil {
		return false
	}
	entries, ok := result["entries"].([]any)
	if !ok || len(entries) == 0 || len(entries) > 25 {
		return false
	}
	for _, rawEntry := range entries {
		entry, ok := softwareRuntimeObjectReceipt(rawEntry)
		if !ok || strings.TrimSpace(stringValue(entry["entry_id"])) == "" ||
			strings.TrimSpace(stringValue(entry["initial_release_date"])) == "" {
			return false
		}
		structureURL, structureOK := trustedScientificPublicHTTPSURL(stringValue(entry["structure_url"]))
		metadataURL, metadataOK := trustedScientificPublicHTTPSURL(stringValue(entry["metadata_url"]))
		if !structureOK || !metadataOK || !strings.EqualFold(structureURL.Hostname(), "www.rcsb.org") ||
			!strings.EqualFold(metadataURL.Hostname(), "data.rcsb.org") {
			return false
		}
	}
	return true
}

func trustedScientificValidatedPublicDownloadSignals(input map[string]any, rawResult any) []string {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok {
		return nil
	}
	download, ok := softwareRuntimeObjectReceipt(result["download"])
	if !ok || strings.TrimSpace(stringValue(download["source_tool_call_id"])) == "" {
		return nil
	}
	requestedURL := strings.TrimSpace(stringValue(input["url"]))
	sourceURL := strings.TrimSpace(stringValue(download["source_url"]))
	if requestedURL == "" || sourceURL == "" || !agentPublicScientificURLsEquivalent(requestedURL, sourceURL) {
		return nil
	}
	encodedArtifacts, err := json.Marshal(result["artifacts"])
	if err != nil {
		return nil
	}
	var artifacts []map[string]any
	if json.Unmarshal(encodedArtifacts, &artifacts) != nil || len(artifacts) == 0 {
		return nil
	}
	first := artifacts[0]
	if strings.TrimSpace(stringValue(first["artifact_id"])) == "" ||
		strings.TrimSpace(stringValue(first["version_id"])) == "" {
		return nil
	}
	return []string{trustedScientificValidatedPublicDownloadSignal}
}

func trustedScientificValidatedPublicWebFetchSignals(input map[string]any, rawResult any) []string {
	if _, ok := trustedScientificPublicHTTPSURL(stringValue(input["url"])); !ok {
		return nil
	}
	if trustedScientificSuccessfulExternalizedResult(rawResult) {
		return []string{trustedScientificValidatedPublicWebFetchSignal}
	}
	result, ok := sessionRunnerWebFetchResultMap(rawResult)
	if !ok || sessionRunnerWebFetchStatusCode(rawResult) < 200 || sessionRunnerWebFetchStatusCode(rawResult) >= 300 ||
		boolValue(result["sourceUnavailable"], false) || boolValue(result["partial"], false) ||
		boolValue(result["truncated"], false) {
		return nil
	}
	if _, ok := trustedScientificPublicHTTPSURL(stringValue(result["url"])); !ok {
		return nil
	}
	body := strings.TrimSpace(stringValue(result["body"]))
	if body == "" {
		body = strings.TrimSpace(stringValue(result["result"]))
	}
	if body == "" {
		return nil
	}
	return []string{trustedScientificValidatedPublicWebFetchSignal}
}

func trustedScientificValidatedSearchExcerptSignals(rawResult any) []string {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok {
		return nil
	}
	if nested, nestedOK := softwareRuntimeObjectReceipt(result["result"]); nestedOK {
		result = nested
	}
	if result["failure"] != nil {
		return nil
	}
	encoded, err := json.Marshal(result["sources"])
	if err != nil {
		return nil
	}
	var sources []map[string]any
	if json.Unmarshal(encoded, &sources) != nil {
		return nil
	}
	for _, source := range sources {
		if !strings.EqualFold(strings.TrimSpace(stringValue(source["status"])), "search_result") ||
			!strings.EqualFold(strings.TrimSpace(stringValue(source["evidenceState"])), "discovered") ||
			strings.TrimSpace(stringValue(source["title"])) == "" ||
			len([]rune(strings.TrimSpace(stringValue(source["snippet"])))) < 200 ||
			numberValue(source["qualityScore"]) < 75 {
			continue
		}
		if _, ok := trustedScientificPublicHTTPSURL(firstNonEmpty(
			stringValue(source["canonicalUrl"]), stringValue(source["url"]),
		)); !ok {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(stringValue(source["retrievedAt"]))); err != nil {
			continue
		}
		return []string{trustedScientificValidatedSearchExcerptSignal}
	}
	return nil
}

func trustedScientificValidatedWebResearchReadSignals(rawResult any) []string {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok {
		return nil
	}
	if nested, nestedOK := softwareRuntimeObjectReceipt(result["result"]); nestedOK {
		result = nested
	}
	encoded, err := json.Marshal(result["sources"])
	if err != nil {
		return nil
	}
	var sources []map[string]any
	if json.Unmarshal(encoded, &sources) != nil {
		return nil
	}
	for _, source := range sources {
		if !strings.EqualFold(strings.TrimSpace(stringValue(source["status"])), "fetched") {
			continue
		}
		receipt, ok := softwareRuntimeObjectReceipt(source["readReceipt"])
		if !ok || !boolValue(receipt["deepRead"], false) ||
			!boolValue(receipt["responseComplete"], false) ||
			boolValue(receipt["truncated"], false) || boolValue(receipt["partial"], false) ||
			numberValue(receipt["extractableCharacters"]) < webResearchMinimumSubstantiveCharacters {
			continue
		}
		if _, ok := trustedScientificPublicHTTPSURL(stringValue(source["url"])); ok {
			return []string{trustedScientificValidatedWebResearchReadSignal}
		}
	}
	return nil
}

func trustedScientificFullTextAvailable(rawResult any) bool {
	result, ok := softwareRuntimeObjectReceipt(rawResult)
	if !ok {
		return false
	}
	if nested, nestedOK := softwareRuntimeObjectReceipt(result["result"]); nestedOK {
		result = nested
	}
	if available, recorded := result["available"].(bool); recorded && !available {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(stringValue(result["status"])))
	if status == "not_available" || status == "unavailable" || status == "failed" ||
		boolValue(result["sourceUnavailable"], false) {
		return false
	}
	body := strings.TrimSpace(firstNonEmpty(stringValue(result["body"]), stringValue(result["text"])))
	return body != ""
}

func trustedScientificPublicHTTPSURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" || (parsed.Port() != "" && parsed.Port() != "443") {
		return nil, false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if _, err := networkpolicy.NormalizePattern(host); err != nil || networkpolicy.PrivateOrReserved(host) ||
		networkpolicy.ConflictingPattern(host, networkpolicy.BuiltInDeniedPatterns()) != "" {
		return nil, false
	}
	return parsed, true
}

func trustedScientificSuccessfulExternalizedResult(rawResult any) bool {
	encoded, err := json.Marshal(rawResult)
	if err != nil {
		return false
	}
	descriptor, _, found, err := toolcontract.DecodeExternalizedResult(encoded)
	return err == nil && found && descriptor.Outcome == string(agentruntime.ToolResultSucceeded) &&
		isRunnerLargeToolResultArtifactID(descriptor.ArtifactID)
}

func (s *Server) trustedScientificCapabilityWitnessForCompletedTool(
	toolName string,
	rawInput any,
	rawResult any,
) (sciencecapability.Witness, bool) {
	if normalizeAgentToolName(toolName) != "softwareruntime" || s == nil || s.scienceCapabilities == nil {
		return sciencecapability.Witness{}, false
	}
	input, ok := rawInput.(map[string]any)
	if !ok {
		return sciencecapability.Witness{}, false
	}
	request, err := decodeSoftwareRuntimeRequest(input)
	if err != nil || request.ScientificEvidence == nil {
		return sciencecapability.Witness{}, false
	}
	result, ok := rawResult.(map[string]any)
	if !ok || !verifiedSoftwareRuntimeExecutionResult(result) {
		return sciencecapability.Witness{}, false
	}
	requestDigest, err := software.RequestDigest(request)
	if err != nil || stringValue(result["request_digest"]) != requestDigest ||
		stringValue(result["executable"]) != request.Executable {
		return sciencecapability.Witness{}, false
	}
	witnesses, err := decodeScientificCapabilityWitnesses([]any{result["scientific_witness"]})
	if err != nil || len(witnesses) != 1 ||
		!softwareRuntimeScientificWitnessMatchesResult(request, witnesses[0], result) ||
		sciencecapability.ValidateWitness(*s.scienceCapabilities, witnesses[0]) != nil {
		return sciencecapability.Witness{}, false
	}
	return witnesses[0], true
}

func softwareRuntimeScientificWitnessMatchesResult(
	request software.Request,
	witness sciencecapability.Witness,
	result map[string]any,
) bool {
	declaration := request.ScientificEvidence
	if declaration == nil || witness.Capability != request.Capability ||
		witness.Engine != declaration.Engine || witness.EnginePackage != declaration.EnginePackage ||
		witness.ScoreKind != declaration.ScoreKind || witness.Provider != stringValue(result["provider_id"]) ||
		witness.EnvironmentSHA256 != stringValue(result["runtime_generation"]) ||
		witness.JobID != stringValue(result["exec_id"]) {
		return false
	}
	profileSHA256, err := software.ScientificEvidenceDigest(*declaration)
	if err != nil || witness.ProfileSHA256 != profileSHA256 || len(witness.Inputs) != len(declaration.Inputs) ||
		len(witness.Artifacts) != len(declaration.Artifacts) {
		return false
	}
	for _, input := range declaration.Inputs {
		if !isSHA256Hex(witness.Inputs[input.Kind]) {
			return false
		}
	}
	outputs, ok := softwareRuntimeOutputReceipts(result["outputs"])
	if !ok {
		return false
	}
	outputDigests := make(map[string]string, len(outputs))
	for _, output := range outputs {
		path := stringValue(output["path"])
		digest := stringValue(output["sha256"])
		if path == "" || !isSHA256Hex(digest) || outputDigests[path] != "" {
			return false
		}
		outputDigests[path] = digest
	}
	artifactsByKind := make(map[string]string, len(witness.Artifacts))
	for _, artifact := range witness.Artifacts {
		if artifactsByKind[artifact.Kind] != "" {
			return false
		}
		artifactsByKind[artifact.Kind] = artifact.SHA256
	}
	for _, artifact := range declaration.Artifacts {
		if artifactsByKind[artifact.Kind] == "" || artifactsByKind[artifact.Kind] != outputDigests[artifact.Path] {
			return false
		}
	}
	return true
}

func softwareRuntimeOutputReceipts(raw any) ([]map[string]any, bool) {
	if raw == nil {
		return nil, true
	}
	if values, ok := raw.([]any); ok {
		result := make([]map[string]any, len(values))
		for index, value := range values {
			item, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			result[index] = item
		}
		return result, true
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var result []map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, false
	}
	return result, true
}

func verifiedSoftwareRuntimeExecutionResult(raw any) bool {
	result, ok := raw.(map[string]any)
	if !ok || !boolValue(result["ok"], false) || stringValue(result["status"]) != "completed" ||
		stringValue(result["exit_status"]) != "ok" || stringValue(result["provider_id"]) != "local-conda" ||
		strings.TrimSpace(stringValue(result["environment"])) == "" ||
		strings.TrimSpace(stringValue(result["executable"])) == "" ||
		!isSHA256Hex(strings.TrimSpace(stringValue(result["runtime_generation"]))) ||
		!isSHA256Hex(strings.TrimSpace(stringValue(result["request_digest"]))) ||
		!isSHA256Hex(strings.TrimSpace(stringValue(result["stdout_sha256"]))) ||
		!isSHA256Hex(strings.TrimSpace(stringValue(result["stderr_sha256"]))) {
		return false
	}
	cleanup, ok := softwareRuntimeObjectReceipt(result["cleanup"])
	if !ok || !boolValue(cleanup["process_group_terminated"], false) ||
		!boolValue(cleanup["process_tree_terminated"], false) ||
		!boolValue(cleanup["temporary_streams_closed"], false) {
		return false
	}
	outputs, ok := softwareRuntimeOutputReceipts(result["outputs"])
	if !ok {
		return false
	}
	for _, rawOutput := range outputs {
		output := rawOutput
		if strings.TrimSpace(stringValue(output["path"])) == "" ||
			numberValue(output["bytes"]) < 0 ||
			!isSHA256Hex(strings.TrimSpace(stringValue(output["sha256"]))) {
			return false
		}
	}
	return true
}

func softwareRuntimeObjectReceipt(raw any) (map[string]any, bool) {
	if result, ok := raw.(map[string]any); ok {
		return result, true
	}
	if raw == nil {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, false
	}
	return result, true
}

func trustedScientificReviewSignalForMCPAttestation(attestation workspaceMCPSourceEvidenceAttestation) []string {
	connectorID := strings.ToLower(strings.TrimSpace(attestation.ConnectorID))
	if attestation.Schema != workspaceMCPSourceEvidenceSchemaV1 ||
		attestation.EvidenceClass != workspace.KernelMCPEvidenceClassBundledReadOnly ||
		!attestation.ReadOnlyHint || !strings.EqualFold(strings.TrimSpace(attestation.ConnectorSource), "bundled") ||
		!isBundledAgentConnector(connectorID) {
		return nil
	}
	return []string{"source-connector:" + connectorID}
}

// bindKernelMCPTrustedScientificEvidence makes a durable, governed host.mcp
// result visible to the live logical task immediately. The same evidence is
// reconstructed from the transcript checkpoint after restart; updating the
// in-memory run here prevents a later read_file or artifact step from being
// incorrectly rejected during the still-active execution unit.
func bindKernelMCPTrustedScientificEvidence(
	ctx context.Context,
	attestation kernelMCPSourceEvidenceAttestation,
	toolName string,
	input any,
	result any,
) {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || !workspaceMCPSourceResultSucceeded(result) {
		return
	}
	signals := trustedScientificReviewSignalForMCPAttestation(workspaceMCPSourceEvidenceAttestation{
		Schema:            workspaceMCPSourceEvidenceSchemaV1,
		EvidenceClass:     attestation.Class,
		ConnectorID:       attestation.ConnectorID,
		ConnectorSource:   attestation.ConnectorSource,
		InputSchemaSHA256: attestation.InputSchemaSHA256,
		ReadOnlyHint:      attestation.ReadOnlyHint,
	})
	if len(signals) == 0 {
		return
	}
	// The durable kernel checkpoint already contains the exact governed method,
	// validated input and structured output. Preserve record-level depth here,
	// before the outer REPL intentionally reduces the payload to stdout/files.
	// This removes the need for the model to print a magic evidence field and
	// keeps restart reconstruction on the same authoritative event path.
	signals = append(signals, trustedScientificEvidenceRecordSignals(toolName, input, result)...)
	signals = append(signals, trustedScientificEvidenceRouteExhaustionSignals(toolName, input, result)...)
	if len(signals) > 0 {
		run.addTrustedScientificReviewSignals(signals...)
	}
}

func (s *Server) bindKernelMCPTrustedScientificEvidenceForSession(
	ctx context.Context,
	sessionID string,
	attestation kernelMCPSourceEvidenceAttestation,
	toolName string,
	input any,
	result any,
) {
	if run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun); run == nil {
		if active := s.activeSessionChatRun(sessionID); active != nil {
			ctx = withTranscriptRunnerChatRun(ctx, active)
		}
	}
	bindKernelMCPTrustedScientificEvidence(ctx, attestation, toolName, input, result)
}

func trustedScientificReviewSignalForURL(raw string) []string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	normalized, err := networkpolicy.NormalizePattern(host)
	if err != nil || networkpolicy.PrivateOrReserved(normalized) {
		return nil
	}
	return []string{"source-host:" + normalized}
}

func collectTrustedScientificReviewURLSignals(value any, depth int, visited *int, target *[]string) {
	if depth > 8 || visited == nil || *visited >= 512 || target == nil {
		return
	}
	(*visited)++
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(strings.TrimSpace(typed), "http://") || strings.HasPrefix(strings.TrimSpace(typed), "https://") {
			*target = append(*target, trustedScientificReviewSignalForURL(typed)...)
		}
	case []any:
		for _, item := range typed {
			collectTrustedScientificReviewURLSignals(item, depth+1, visited, target)
		}
	case map[string]any:
		for _, item := range typed {
			collectTrustedScientificReviewURLSignals(item, depth+1, visited, target)
		}
	}
}

func trustedScientificReviewArtifactSignals(input map[string]any) []string {
	if len(input) == 0 {
		return nil
	}
	paths := append([]string(nil), stringArrayValue(input["files"])...)
	for _, key := range []string{"path", "file_path", "filePath"} {
		if value := strings.TrimSpace(stringValue(input[key])); value != "" {
			paths = append(paths, value)
		}
	}
	signals := make([]string, 0, len(paths))
	for _, path := range paths {
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(path))), ".")
		if validRuntimeArtifactFormatToken(extension) {
			signals = append(signals, "artifact-format:"+extension)
		}
	}
	return uniqueSortedFolded(signals)
}

func trustedScientificReviewSignalsFromRunnerEntries(entries []eventjournal.Entry) []string {
	signals := make([]string, 0)
	hasAuthoritativeSource := false
	for _, entry := range entries {
		message := entry.Message
		messageType := strings.ToLower(strings.TrimSpace(stringValue(message["type"])))
		if strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "user") &&
			(messageType == "message" || messageType == "user_message" || messageType == "user_input_response" ||
				messageType == "history_user_message") {
			if refs, err := decodeUserArtifactReferences(message["artifactRefs"]); err == nil && len(refs) > 0 {
				signals = append(signals, trustedScientificUserArtifactInputSignals(refs)...)
			}
		}
		if stringValue(message["type"]) == runnerTrustedScientificEvidenceReplayType {
			entrySignals := make([]string, 0)
			for _, signal := range stringArrayValue(message[trustedScientificReviewSignalsField]) {
				if validTrustedScientificReviewSignal(signal) {
					entrySignals = append(entrySignals, signal)
				}
			}
			signals = append(signals, entrySignals...)
			hasAuthoritativeSource = hasAuthoritativeSource || sessionRunnerHasAuthoritativeSourceEvidence(entrySignals)
			continue
		}
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["status"]) != "completed" ||
			stringValue(message["toolPhase"]) != "completed" {
			continue
		}
		toolResult, resultRecorded := message["toolResult"]
		if resultRecorded && agentruntime.ClassifyToolResult(toolResult) != agentruntime.ToolResultSucceeded {
			continue
		}
		entrySignals := make([]string, 0)
		for _, signal := range stringArrayValue(message[trustedScientificReviewSignalsField]) {
			if validTrustedScientificReviewSignal(signal) {
				entrySignals = append(entrySignals, signal)
			}
		}
		toolName := stringValue(message["toolName"])
		if normalizeAgentToolName(toolName) == "fetcharticlefulltext" &&
			!trustedScientificFullTextAvailable(toolResult) {
			entrySignals = removeTrustedScientificSourceSignals(entrySignals)
		}
		if runtimeToolContractHasCapability(toolName, "scientific-system-tool") {
			entrySignals = append(entrySignals, "scientific-tool:"+strings.ToLower(strings.TrimSpace(toolName)))
		}
		entrySignals = append(entrySignals, trustedScientificReviewSignalsForMCPCheckpoint(message)...)
		input, _ := message["toolInput"].(map[string]any)
		normalizedTool := normalizeAgentToolName(toolName)
		switch normalizedTool {
		case "python", "r", "bash", "repl":
			if resultRecorded && runtimeToolContractHasCapability(toolName, "runtime-execution") {
				entrySignals = append(entrySignals, "execution-tool:"+normalizedTool)
			}
			if resultRecorded && trustedScientificManagedEnvironmentExecution(toolName, input, toolResult) {
				entrySignals = append(
					entrySignals,
					trustedScientificManagedEnvironmentExecutionSignalPrefix+normalizedTool,
				)
			}
		case "softwareruntime":
			if resultRecorded && verifiedSoftwareRuntimeExecutionResult(toolResult) {
				entrySignals = append(entrySignals, "execution-tool:"+softwareRuntimeToolName)
			}
		case "webfetch":
			validated := trustedScientificValidatedPublicWebFetchSignals(input, toolResult)
			if len(validated) > 0 {
				entrySignals = append(entrySignals, trustedScientificReviewSignalForURL(stringValue(input["url"]))...)
				entrySignals = append(entrySignals, validated...)
			}
		case "websearch", "webresearch":
			validated := trustedScientificValidatedSearchExcerptSignals(toolResult)
			if normalizedTool == "webresearch" {
				validated = append(validated, trustedScientificValidatedWebResearchReadSignals(toolResult)...)
			}
			if len(validated) > 0 {
				collectTrustedScientificReviewURLSignals(toolResult, 0, new(int), &entrySignals)
				entrySignals = append(entrySignals, validated...)
			}
		case "downloadpublicscientificfile":
			entrySignals = append(entrySignals, trustedScientificValidatedPublicDownloadSignals(input, toolResult)...)
		case "saveartifacts", "artifactregister":
			entrySignals = append(entrySignals, trustedScientificReviewArtifactSignals(input)...)
		}
		if hasAuthoritativeSource && trustedScientificExecutionMaterializedOutput(toolName, toolResult) {
			if executionTool := trustedScientificExecutionToolName(toolName, toolResult); executionTool != "" {
				entrySignals = append(entrySignals, trustedScientificSourceGroundedExecutionSignalPrefix+executionTool)
			}
		}
		if trustedScientificExecutionMaterializedOutput(toolName, toolResult) &&
			trustedScientificInputDigestsMatchSignals(signals, trustedScientificResultInputDigests(toolResult)) {
			if executionTool := trustedScientificExecutionToolName(toolName, toolResult); executionTool != "" {
				entrySignals = append(
					entrySignals,
					trustedScientificUserArtifactGroundedExecutionSignalPrefix+executionTool,
				)
			}
		}
		signals = append(signals, entrySignals...)
		hasAuthoritativeSource = hasAuthoritativeSource || sessionRunnerHasAuthoritativeSourceEvidence(entrySignals)
	}
	return uniqueSortedFolded(signals)
}

func removeTrustedScientificSourceSignals(signals []string) []string {
	filtered := make([]string, 0, len(signals))
	for _, signal := range signals {
		normalized := strings.ToLower(strings.TrimSpace(signal))
		if strings.HasPrefix(normalized, "source-host:") ||
			strings.HasPrefix(normalized, "source-connector:") ||
			normalized == trustedScientificValidatedPublicWebFetchSignal ||
			normalized == trustedScientificValidatedPublicDownloadSignal ||
			normalized == trustedScientificValidatedWebResearchReadSignal ||
			normalized == trustedScientificValidatedSearchExcerptSignal {
			continue
		}
		filtered = append(filtered, signal)
	}
	return filtered
}

func trustedScientificReviewSignalsForMCPCheckpoint(message eventjournal.Message) []string {
	attestation := workspaceMCPSourceEvidenceAttestation{
		Schema: stringValue(message["schema"]), EvidenceClass: stringValue(message["evidenceClass"]),
		ConnectorID: stringValue(message["connectorId"]), ConnectorSource: stringValue(message["connectorSource"]),
		ReadOnlyHint: boolValue(message["readOnlyHint"], false),
	}
	if attestation.Schema == "synon.kernel_mcp_evidence.v1" {
		encoded, err := json.Marshal(message)
		if err != nil {
			return nil
		}
		var checkpoint sessionRunnerDurableToolCheckpoint
		if json.Unmarshal(encoded, &checkpoint) != nil || !validSessionRunnerMCPDurableCheckpoint(checkpoint) {
			return nil
		}
		attestation.Schema = workspaceMCPSourceEvidenceSchemaV1
	}
	signals := trustedScientificReviewSignalForMCPAttestation(attestation)
	if len(signals) == 0 {
		return nil
	}
	signals = append(signals, trustedScientificEvidenceRecordSignals(
		stringValue(message["toolName"]), message["toolInput"], message["toolResult"],
	)...)
	signals = append(signals, trustedScientificEvidenceDiscoverySignals(
		stringValue(message["toolName"]), message["toolResult"],
	)...)
	signals = append(signals, trustedScientificEvidenceRouteExhaustionSignals(
		stringValue(message["toolName"]), message["toolInput"], message["toolResult"],
	)...)
	return uniqueSortedFolded(signals)
}

func validTrustedScientificReviewSignal(signal string) bool {
	signal = strings.ToLower(strings.TrimSpace(signal))
	switch {
	case strings.HasPrefix(signal, "source-connector:"):
		return isBundledAgentConnector(strings.TrimPrefix(signal, "source-connector:"))
	case strings.HasPrefix(signal, "source-host:"):
		host, err := networkpolicy.NormalizePattern(strings.TrimPrefix(signal, "source-host:"))
		return err == nil && !networkpolicy.PrivateOrReserved(host)
	case strings.HasPrefix(signal, "source-authority:"):
		return signal == trustedScientificValidatedPublicDownloadSignal ||
			signal == trustedScientificValidatedPublicWebFetchSignal ||
			signal == trustedScientificValidatedWebResearchReadSignal ||
			signal == trustedScientificValidatedSearchExcerptSignal
	case signal == trustedScientificUserArtifactInputSignal:
		return true
	case strings.HasPrefix(signal, trustedScientificUserArtifactDigestSignalPrefix):
		return isSHA256Hex(strings.TrimPrefix(signal, trustedScientificUserArtifactDigestSignalPrefix))
	case strings.HasPrefix(signal, "artifact-format:"):
		return validRuntimeArtifactFormatToken(strings.TrimPrefix(signal, "artifact-format:"))
	case strings.HasPrefix(signal, "scientific-tool:"):
		return runtimeToolContractHasCapability(strings.TrimPrefix(signal, "scientific-tool:"), "scientific-system-tool")
	case strings.HasPrefix(signal, "execution-tool:"):
		toolName := strings.TrimPrefix(signal, "execution-tool:")
		return toolName == strings.ToLower(softwareRuntimeToolName) ||
			runtimeToolContractHasCapability(toolName, "runtime-execution")
	case strings.HasPrefix(signal, "capability-execution:"):
		capability := strings.TrimPrefix(signal, "capability-execution:")
		_, err := decodeSoftwareRuntimeRequest(map[string]any{
			"capability": capability, "language": "python", "executable": "python",
		})
		return err == nil
	case strings.HasPrefix(signal, trustedScientificSourceGroundedExecutionSignalPrefix):
		toolName := strings.TrimPrefix(signal, trustedScientificSourceGroundedExecutionSignalPrefix)
		return toolName == strings.ToLower(softwareRuntimeToolName) ||
			runtimeToolContractHasCapability(toolName, "runtime-execution") ||
			runtimeToolContractHasCapability(toolName, "scientific-system-tool")
	case strings.HasPrefix(signal, trustedScientificUserArtifactGroundedExecutionSignalPrefix):
		toolName := strings.TrimPrefix(signal, trustedScientificUserArtifactGroundedExecutionSignalPrefix)
		return toolName == strings.ToLower(softwareRuntimeToolName) ||
			runtimeToolContractHasCapability(toolName, "runtime-execution") ||
			runtimeToolContractHasCapability(toolName, "scientific-system-tool")
	case strings.HasPrefix(signal, trustedScientificManagedEnvironmentExecutionSignalPrefix):
		toolName := strings.TrimPrefix(signal, trustedScientificManagedEnvironmentExecutionSignalPrefix)
		return toolName == "python" || toolName == "r" || toolName == "bash"
	case strings.HasPrefix(signal, trustedScientificEvidenceRecordSignalPrefix):
		_, _, found := runnerEvidenceRecordSignal(signal)
		return found
	case strings.HasPrefix(signal, trustedScientificPatentDiscoverySignalPrefix):
		identifier := strings.TrimPrefix(signal, trustedScientificPatentDiscoverySignalPrefix)
		return identifier != "" && runnerEvidenceCanonicalPatent(identifier) == strings.ToUpper(identifier)
	case strings.HasPrefix(signal, trustedScientificTrialDiscoverySignalPrefix):
		identifier := strings.TrimPrefix(signal, trustedScientificTrialDiscoverySignalPrefix)
		return identifier != "" && strings.ToUpper(sessionRunnerNCTPattern.FindString(identifier)) == strings.ToUpper(identifier)
	case strings.HasPrefix(signal, trustedScientificPublicationDiscoverySignalPrefix):
		identifier := strings.TrimPrefix(signal, trustedScientificPublicationDiscoverySignalPrefix)
		return identifier != "" && runnerEvidenceCanonicalPublication(identifier) == strings.ToLower(identifier)
	case strings.HasPrefix(signal, trustedScientificEvidenceRouteExhaustedSignalPrefix):
		parts := strings.Split(strings.TrimPrefix(signal, trustedScientificEvidenceRouteExhaustedSignalPrefix), ":")
		if len(parts) != 2 {
			return false
		}
		class, route := parts[0], parts[1]
		return (class == "publication" || class == "trial" || class == "patent") &&
			(route == "repl" || route == "webfetch" || route == "webresearch" || route == "patentsearch" || route == "fetcharticlefulltext")
	case strings.HasPrefix(signal, trustedScientificRequiredCapabilitySignalPrefix):
		capability := strings.TrimPrefix(signal, trustedScientificRequiredCapabilitySignalPrefix)
		_, err := decodeSoftwareRuntimeRequest(map[string]any{
			"capability": capability, "language": "python", "executable": "python",
		})
		return err == nil
	default:
		return false
	}
}

func normalizeTrustedScientificReviewSignals(signals []string) []string {
	result := make([]string, 0, len(signals))
	for _, signal := range signals {
		signal = strings.ToLower(strings.TrimSpace(signal))
		if validTrustedScientificReviewSignal(signal) {
			result = append(result, signal)
		}
	}
	return uniqueSortedFolded(result)
}

func trustedSoftwareRuntimeCapabilities(signals []string) []string {
	capabilities := make([]string, 0)
	for _, signal := range normalizeTrustedScientificReviewSignals(signals) {
		if strings.HasPrefix(signal, "capability-execution:") {
			capabilities = append(capabilities, strings.TrimPrefix(signal, "capability-execution:"))
		}
	}
	return uniqueSortedScientificCapabilities(capabilities)
}

func runtimeToolContractHasCapability(toolName, capability string) bool {
	return agentRuntimeToolSchemaHasCapability(
		agentruntime.ToolSchema{Name: strings.TrimSpace(toolName)}, capability,
	)
}

func validRuntimeArtifactFormatToken(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

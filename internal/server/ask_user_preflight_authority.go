package server

import (
	"encoding/json"
	"sort"
	"strings"
	"synon-go/internal/agentruntime"
	"unicode"
	"unicode/utf8"
)

func askUserCompletedResultSupportsDecision(content string) bool {
	var decoded any
	if json.Unmarshal([]byte(content), &decoded) != nil || decoded == nil ||
		agentruntime.ClassifyToolResult(decoded) != agentruntime.ToolResultSucceeded {
		return false
	}
	outer := mapValue(decoded)
	for _, envelope := range []map[string]any{outer, mapValue(outer["result"])} {
		if envelope["executed"] == false && envelope["decision_required"] == true {
			return false
		}
	}
	return true
}

func filterAskUserMatchingPreflightReferences(references []string, implementation string, authorities map[string]askUserEvidenceAuthority) []string {
	filtered := make([]string, 0, len(references))
	for _, reference := range references {
		authority := authorities[reference]
		if implementation != "" && authority.Implementation != "" && authority.Resources != nil &&
			!askUserImplementationIdentityMatches(authority.Implementation, implementation) {
			continue
		}
		filtered = append(filtered, reference)
	}
	return filtered
}

// askUserCompletedToolEvidenceAuthority extracts only server-observed fields
// from a completed environment preflight. The model remains responsible for
// proposing scientifically meaningful alternatives, while the Harness binds
// each option to the matching immutable receipt and renders the measured
// CPU/memory/GPU profile without asking the model to copy opaque call IDs.
func askUserCompletedToolEvidenceAuthority(
	call agentruntime.ToolCall,
	resultContent string,
	authority askUserEvidenceAuthority,
) askUserEvidenceAuthority {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if name != manageEnvironmentsToolName && name != managePackagesToolName {
		return authority
	}
	input := map[string]any{}
	if json.Unmarshal(call.Arguments, &input) != nil ||
		!strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "preflight") {
		return authority
	}
	result := map[string]any{}
	if json.Unmarshal([]byte(strings.TrimSpace(resultContent)), &result) != nil {
		return authority
	}
	// Preserve the transport envelope. A valid read-only preflight may carry
	// executed=false, but an outer error/partial result cannot attest readiness.
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return authority
	}
	if nested := mapValue(result["result"]); len(nested) > 0 {
		result = nested
	}
	if !strings.EqualFold(strings.TrimSpace(stringValue(result["mode"])), "preflight") ||
		!boolValue(result["ok"], false) {
		return authority
	}
	implementation := strings.TrimSpace(stringValue(result["implementation"]))
	if implementation == "" {
		implementation = strings.TrimSpace(stringValue(input["implementation"]))
	}
	verifiedRequirements := boolValue(result["resource_requirements_verified"], false)
	resources := &askUserResourceProfile{CPU: "unresolved", Memory: "unresolved", GPU: "unresolved"}
	if verifiedRequirements {
		resources = askUserResourceProfileFromPreflightRequirements(mapValue(result["requirements"]))
	}
	if implementation == "" || resources == nil {
		return authority
	}
	authority.Implementation = implementation
	authority.Resources = resources
	authority.PreflightStatus = strings.TrimSpace(stringValue(result["status"]))
	if verifiedRequirements {
		authority.SetupState = strings.TrimSpace(stringValue(result["setup_state"]))
		if feasible, present := result["feasible"]; present {
			value := boolValue(feasible, false)
			authority.Feasible = &value
		}
		authority.Blockers = uniqueSortedFolded(stringArrayValue(result["blockers"]))
	}
	return authority
}

func askUserImplementationEvidenceAuthority(
	implementation string,
	authorities map[string]askUserEvidenceAuthority,
) (string, askUserEvidenceAuthority) {
	implementation = strings.TrimSpace(implementation)
	if implementation == "" {
		return "", askUserEvidenceAuthority{}
	}
	keys := make([]string, 0, len(authorities))
	for reference := range authorities {
		keys = append(keys, reference)
	}
	sort.Strings(keys)
	selectedReference := ""
	selected := askUserEvidenceAuthority{}
	for _, reference := range keys {
		authority := authorities[reference]
		if authority.Class != askUserEvidenceCompletedTool || authority.Resources == nil ||
			!askUserImplementationIdentityMatches(authority.Implementation, implementation) {
			continue
		}
		if selectedReference == "" || authority.Ordinal > selected.Ordinal {
			selectedReference, selected = reference, authority
		}
	}
	return selectedReference, selected
}

func askUserImplementationIdentityMatches(observed, proposed string) bool {
	observed = askUserImplementationIdentityKey(observed)
	proposed = askUserImplementationIdentityKey(proposed)
	if observed == "" || proposed == "" {
		return false
	}
	return observed == proposed
}

// Comparison, automatic binding and receipt deduplication must share this key.
// Case-insensitive compatibility is limited to plain letter/space display names.
// Paths, URLs, tags and version-bearing identifiers are opaque; even a digit
// or punctuation opts out of case folding. This is deliberately conservative:
// repair data exposes exact identities instead of guessing provider semantics.
func askUserImplementationIdentityKey(value string) string {
	value = askUserPublicImplementationIdentity(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	for _, char := range value {
		if !unicode.IsLetter(char) && !unicode.IsSpace(char) {
			return "opaque:" + value
		}
	}
	return "display:" + strings.ToLower(value)
}

func askUserPublicImplementationIdentity(value string) string {
	// Only colon + whitespace delimits presentation prose. OCI tags,
	// versions, URLs and service identifiers retain identity-bearing colons.
	for index, char := range value {
		if char == ':' && index > 0 && index+1 < len(value) {
			next, _ := utf8.DecodeRuneInString(value[index+1:])
			if unicode.IsSpace(next) {
				return strings.TrimSpace(value[:index])
			}
		}
	}
	return value
}

func askUserAvailablePreflightIdentities(authorities map[string]askUserEvidenceAuthority) []any {
	latest := make(map[string]string)
	for reference, authority := range authorities {
		if authority.Class != askUserEvidenceCompletedTool || authority.Resources == nil || strings.TrimSpace(authority.Implementation) == "" {
			continue
		}
		key := askUserImplementationIdentityKey(authority.Implementation)
		previous, found := latest[key]
		if !found || authority.Ordinal > authorities[previous].Ordinal || (authority.Ordinal == authorities[previous].Ordinal && reference < previous) {
			latest[key] = reference
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]any, 0, len(keys))
	for _, key := range keys {
		reference := latest[key]
		result = append(result, map[string]any{"implementation": authorities[reference].Implementation, "evidence_ref": reference})
	}
	return result
}

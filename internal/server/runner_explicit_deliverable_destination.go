package server

import (
	"path/filepath"
	"strings"
)

// sessionRunnerNormalizeExplicitDeliverableDestinations keeps model-selected
// working data private while ensuring outputs explicitly requested by the user
// cannot be accidentally hidden. It changes only retention metadata; file
// bytes, names, scientific content, and the selected output set remain model
// owned.
func sessionRunnerNormalizeExplicitDeliverableDestinations(taskIntent string, input map[string]any) map[string]any {
	if len(input) == 0 {
		return input
	}
	files := stringArrayValue(input["files"])
	if len(files) == 0 {
		return input
	}
	explicit := make(map[string]struct{})
	for _, name := range sessionRunnerExplicitDeliverableNames(taskIntent) {
		explicit[strings.ToLower(filepath.Base(name))] = struct{}{}
	}
	formats := sessionRunnerExplicitDeliverableFormats(taskIntent)
	durableOutputRequested := sessionRunnerRequiresDurableArtifact(taskIntent)
	required := make(map[string]struct{})
	for _, path := range files {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
		if _, found := explicit[base]; found {
			required[path] = struct{}{}
			continue
		}
		explicitFormat := false
		for _, format := range formats {
			if sessionRunnerArtifactMatchesDeliverableFormat(base, format) {
				explicitFormat = true
				break
			}
		}
		// A task may name private working files alongside its requested
		// deliverables (for example, a raw source ledger). The durable-output
		// signal is a fallback only when the task did not identify a narrower
		// deliverable name or format; otherwise it would promote every listed
		// file and expose internal working data in the public artifact set.
		if explicitFormat || (durableOutputRequested && len(explicit) == 0 && len(formats) == 0) {
			required[path] = struct{}{}
		}
	}
	if len(required) == 0 {
		return input
	}
	normalized := make(map[string]any, len(input))
	for key, value := range input {
		normalized[key] = value
	}
	destination := make(map[string]any)
	if current, ok := input["destination"].(map[string]any); ok {
		for key, value := range current {
			destination[key] = value
		}
	}
	for path := range required {
		destination[path] = "snapshot"
	}
	normalized["destination"] = destination
	return normalized
}

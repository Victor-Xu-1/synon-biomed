package server

import (
	"path/filepath"
	"regexp"
	"strings"
)

var agentWorkspaceArtifactReferencePattern = regexp.MustCompile(`\{\{artifact:[0-9a-fA-F-]{36}\}\}`)
var agentWorkspaceAnyArtifactReferencePattern = regexp.MustCompile(`\{\{artifact:[^{}\r\n]+\}\}`)

func agentRuntimeWorkspaceArtifactReferencePreflight(publicName string, input map[string]any) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(publicName), "edit_file") {
		return nil
	}
	newString := stringValue(input["new_string"])
	for _, reference := range agentWorkspaceAnyArtifactReferencePattern.FindAllString(newString, -1) {
		if !agentWorkspaceArtifactReferencePattern.MatchString(reference) {
			return map[string]any{
				"ok": true, "status": "artifact_reference_preflight_required", "executed": false,
				"code":    "artifact_reference_must_be_canonical",
				"message": "The proposed file content contains a non-canonical artifact reference.",
				"recovery": "For a companion file in the same deliverable set, use its exact workspace-relative path. " +
					"Use only the exact UUID version reference returned by save_artifacts in the final answer; never use art_ identifiers or invented markers.",
			}
		}
	}
	return agentWorkspaceArtifactSelfReferencePreflight(input)
}

// agentWorkspaceArtifactSelfReferencePreflight closes an impossible update
// cycle: changing a report so it points at its own latest immutable version
// necessarily creates yet another version. Prior immutable references remain
// valid; the final assistant answer is the right place to expose the newest
// version returned by save_artifacts.
func agentWorkspaceArtifactSelfReferencePreflight(input map[string]any) map[string]any {
	filePath := strings.TrimSpace(stringValue(input["file_path"]))
	oldString := stringValue(input["old_string"])
	newString := stringValue(input["new_string"])
	if filePath == "" || oldString == "" || newString == "" || oldString == newString {
		return nil
	}
	filename := filepath.Base(filepath.ToSlash(filePath))
	if filename == "." || filename == "" ||
		!strings.Contains(oldString, filename) || !strings.Contains(newString, filename) {
		return nil
	}
	oldReferences := agentWorkspaceArtifactReferencePattern.FindAllString(oldString, -1)
	newReferences := agentWorkspaceArtifactReferencePattern.FindAllString(newString, -1)
	if len(oldReferences) == 0 || len(oldReferences) != len(newReferences) {
		return nil
	}
	normalize := func(value string) string {
		return agentWorkspaceArtifactReferencePattern.ReplaceAllString(value, "{{artifact:IMMUTABLE_VERSION}}")
	}
	if normalize(oldString) != normalize(newString) {
		return nil
	}
	return map[string]any{
		"ok": true, "status": "no_change_required", "executed": false,
		"code":     "artifact_self_reference_is_immutable",
		"message":  "The requested edit only chases this file's own latest immutable artifact version, which would create another version indefinitely. The existing immutable reference remains valid.",
		"recovery": "Do not edit or save this file again solely to update its own artifact reference. Leave a self-entry as plain text when editing is otherwise required, and use the newest artifact_ref returned by save_artifacts only in the final assistant answer.",
	}
}

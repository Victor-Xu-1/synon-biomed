package server

import (
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

type sessionRunnerInputAttachmentReader struct {
	VersionID          string
	Filename           string
	FormatID           string
	Reader             string
	EquivalentPackages []string
	Read               bool
}

func inputAttachmentReaderStatesFromRunnerEntries(entries []eventjournal.Entry) []sessionRunnerInputAttachmentReader {
	states := make([]sessionRunnerInputAttachmentReader, 0)
	byVersion := map[string]int{}
	for _, entry := range entries {
		if role := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["role"]))); role != "" && role != "user" {
			continue
		}
		refs, err := decodeUserArtifactReferences(entry.Message["artifactRefs"])
		if err != nil {
			continue
		}
		for _, ref := range refs {
			versionID := strings.TrimSpace(ref.VersionID)
			if versionID == "" {
				continue
			}
			if _, duplicate := byVersion[versionID]; duplicate {
				continue
			}
			format, _, _ := agentWorkspaceFormatHint(ref.Filename, ref.ContentType)
			byVersion[versionID] = len(states)
			states = append(states, sessionRunnerInputAttachmentReader{
				VersionID: versionID, Filename: ref.Filename, FormatID: format.ID, Reader: format.Reader,
				EquivalentPackages: append([]string(nil), format.EquivalentPackages...),
			})
		}
	}
	if len(states) == 0 {
		return nil
	}
	for _, entry := range entries {
		message := entry.Message
		if !strings.EqualFold(strings.TrimSpace(stringValue(message["type"])), "runner_checkpoint") ||
			!strings.EqualFold(strings.TrimSpace(stringValue(message["toolName"])), "read_file") ||
			!strings.EqualFold(strings.TrimSpace(stringValue(message["toolPhase"])), "completed") ||
			agentruntime.ClassifyToolResult(message["toolResult"]) != agentruntime.ToolResultSucceeded {
			continue
		}
		input := decodeReadReuseMap(message["toolInput"])
		markInputAttachmentReaderState(states, input)
	}
	return states
}

func (run *sessionRunnerChatRun) setInputAttachmentReaders(states []sessionRunnerInputAttachmentReader) {
	if run == nil {
		return
	}
	copyStates := make([]sessionRunnerInputAttachmentReader, len(states))
	for index, state := range states {
		copyStates[index] = state
		copyStates[index].EquivalentPackages = append([]string(nil), state.EquivalentPackages...)
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.InputAttachmentReaders = copyStates
}

func (run *sessionRunnerChatRun) recordInputAttachmentRead(input map[string]any) {
	if run == nil || input == nil {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	markInputAttachmentReaderState(run.InputAttachmentReaders, input)
}

func markInputAttachmentReaderState(states []sessionRunnerInputAttachmentReader, input map[string]any) {
	versionID := strings.TrimSpace(stringValue(input["version_id"]))
	if versionID == "" {
		return
	}
	for index := range states {
		if versionID == states[index].VersionID {
			states[index].Read = true
		}
	}
}

func (run *sessionRunnerChatRun) unreadBuiltinAttachmentReadersForPackages(packages []string) []sessionRunnerInputAttachmentReader {
	if run == nil || len(packages) == 0 {
		return nil
	}
	requested := make(map[string]bool, len(packages))
	for _, value := range packages {
		if name := packageSpecName(value); name != "" {
			requested[name] = true
		}
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	result := make([]sessionRunnerInputAttachmentReader, 0)
	for _, state := range run.InputAttachmentReaders {
		if state.Read || !strings.HasPrefix(state.Reader, "builtin_") {
			continue
		}
		overlaps := false
		for _, value := range state.EquivalentPackages {
			if requested[packageSpecName(value)] {
				overlaps = true
				break
			}
		}
		if overlaps {
			copyState := state
			copyState.EquivalentPackages = append([]string(nil), state.EquivalentPackages...)
			result = append(result, copyState)
		}
	}
	return result
}

func (run *sessionRunnerChatRun) unreadBuiltinAttachmentReadersReferencedBy(content string) []sessionRunnerInputAttachmentReader {
	if run == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	normalized := filepath.ToSlash(content)
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	result := make([]sessionRunnerInputAttachmentReader, 0)
	for _, state := range run.InputAttachmentReaders {
		if state.Read || !strings.HasPrefix(state.Reader, "builtin_") ||
			!strings.Contains(normalized, state.VersionID+"-") {
			continue
		}
		copyState := state
		copyState.EquivalentPackages = append([]string(nil), state.EquivalentPackages...)
		result = append(result, copyState)
	}
	return result
}

func (g serverAgentRuntimeToolGateway) agentRuntimeBuiltinAttachmentReaderPreflight(publicName string, input map[string]any) map[string]any {
	if g.taskRun == nil {
		return nil
	}
	var unread []sessionRunnerInputAttachmentReader
	requestedPackages := []string(nil)
	switch publicName {
	case "manage_packages":
		if !strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "install") {
			return nil
		}
		for _, requirement := range buildSessionRunnerExplicitToolContract(g.taskRun.TaskIntent).Requirements {
			if requirement.Name == "manage_packages" {
				return nil
			}
		}
		requestedPackages = uniqueSortedFolded(skillStringValues(input["packages"]))
		unread = g.taskRun.unreadBuiltinAttachmentReadersForPackages(requestedPackages)
	case "bash", "python", "r", "repl", "powershell":
		content := strings.Join([]string{
			stringValue(input["command"]), stringValue(input["code"]), stringValue(input["script"]),
		}, "\n")
		unread = g.taskRun.unreadBuiltinAttachmentReadersReferencedBy(content)
	default:
		return nil
	}
	if len(unread) == 0 {
		return nil
	}
	requiredReads := make([]map[string]any, 0, len(unread))
	for _, state := range unread {
		requiredReads = append(requiredReads, map[string]any{
			"version_id": state.VersionID, "filename": state.Filename,
			"format_id": state.FormatID, "reader": state.Reader,
		})
	}
	return map[string]any{
		"ok": false, "status": "builtin_attachment_reader_required", "executed": false,
		"message":            "The requested operation bypasses built-in readers for unread task attachments.",
		"required_reads":     requiredReads,
		"requested_packages": requestedPackages,
		"recovery":           "Read each listed immutable version_id with read_file first. Install additional packages only if that real read reports a missing capability needed by the task; do not provision duplicate readers speculatively.",
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

// A changed proposal is not proof that a native validation failure was fixed.
// Recover the outstanding condition from canonical terminal receipts, then
// check the actual proposed bytes with the native reader/replacement/validators.
// No file mutation, publication, alternate validator or recovery store is added.
func (g serverAgentRuntimeToolGateway) nativeEditRecoveryPreflight(ctx context.Context, calls []agentruntime.ToolCall) (map[int]string, error) {
	if g.taskRun == nil || g.taskRun.Transcript == nil || g.kernel == nil {
		return nil, nil
	}
	inputs := map[int]map[string]any{}
	targets := map[string]bool{}
	keys := map[int]string{}
	for index, call := range calls {
		name, err := canonicalRuntimeToolName(call.Name)
		if err != nil || name != "edit_file" || !g.AdmitsToolCall(call) {
			continue
		}
		var input map[string]any
		if json.Unmarshal(call.Arguments, &input) != nil {
			continue
		}
		input = g.normalizeAdmittedToolArguments(name, input)
		for key := range g.correctionActionResources(input) {
			keys[index], targets[key] = key, false
			inputs[index] = input
		}
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	if err := g.validateCorrectionRouteClaim(ctx); err != nil {
		return nil, err
	}
	err := g.server.scanSessionRunnerRecoveryEntries(ctx, g.taskRun.Transcript, func(entry eventjournal.Entry) error {
		if runnerEntryStartsNewLogicalTask(entry) {
			for key := range targets {
				targets[key] = false
			}
		}
		if !runnerCorrectionRouteReceipt(entry) || normalizeAgentToolName(stringValue(entry.Message["toolName"])) != "editfile" {
			return nil
		}
		input := g.normalizeAdmittedToolArguments("edit_file", mapValue(runnerCheckpointExecutedToolInput(entry.Message)))
		result := mapValue(entry.Message["toolResult"])
		for key := range g.correctionActionResources(input) {
			if _, relevant := targets[key]; !relevant {
				continue
			}
			if nativeEditRecoverableCondition(result) {
				targets[key] = true
			} else if entry.Message["status"] == "completed" && entry.Message["toolPhase"] == "completed" && len(result) > 0 && agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded && result["executed"] != false {
				targets[key] = false
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	diagnostics := map[int]string{}
	for index, input := range inputs {
		if !targets[keys[index]] {
			continue
		}
		diagnostic, err := g.validateNativeRecoveryEdit(ctx, input)
		if err != nil {
			return nil, err
		}
		if diagnostic != "" {
			diagnostics[index] = diagnostic
		}
	}
	if err := g.validateCorrectionRouteClaim(ctx); err != nil {
		return nil, err
	}
	return diagnostics, nil
}

func nativeEditRecoverableCondition(result map[string]any) bool {
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultFailed || result["executed"] != false {
		return false
	}
	switch stringValue(result["code"]) {
	case "edit_conflict", "invalid_file_structure", "file_content_type_mismatch":
		return true
	default:
		return false
	}
}

func (g serverAgentRuntimeToolGateway) validateNativeRecoveryEdit(ctx context.Context, input map[string]any) (diagnostic string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	access, err := g.server.validateKernelHostIdentity(ctx, g.kernel.access)
	if err != nil {
		return "", err
	}
	// This preflight needs only read authority. The native mutation still owns
	// the final write grant, exact bytes check, lock, staging and commit.
	target, err := g.server.resolveAgentWorkspaceFileTarget(g.kernel, access.UserID, stringValue(input["file_path"]), false)
	if err != nil {
		// Leave path/grant denials to their authoritative native boundary. Do
		// not turn unavailable inspection into a fabricated structural finding.
		return "", nil
	}
	preview, err := os.CreateTemp("", "synon-edit-recovery-*")
	if err != nil {
		return nativeEditRecoveryUnavailable(err)
	}
	defer func() {
		if cleanupErr := errors.Join(preview.Close(), os.Remove(preview.Name())); cleanupErr != nil {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			} else {
				diagnostic, returnErr = nativeEditRecoveryUnavailable(cleanupErr)
			}
		}
	}()
	oldText, nextText := stringValue(input["old_string"]), stringValue(input["new_string"])
	if oldText == "" {
		_, err = writeAgentWorkspaceBytes(ctx, preview, []byte(nextText))
	} else {
		var current *os.File
		current, err = openAgentWorkspaceRegularFile(target.root, target.relativePath)
		if err != nil {
			return "", nil // Native execution diagnoses missing or denied files.
		}
		oldText, err = normalizeAgentWorkspaceQuotedOldString(ctx, current, oldText)
		if err == nil {
			_, _, err = streamAgentWorkspaceReplacement(ctx, current, preview, []byte(oldText), []byte(nextText))
		}
		closeErr := current.Close()
		if closeErr != nil {
			return nativeEditRecoveryUnavailable(closeErr)
		}
	}
	if err == nil {
		err = validateAgentFileContentType(target.displayPath, preview)
	}
	if err == nil {
		if structureErr := validateAgentSavedArtifactStructure(target.displayPath, preview); structureErr != nil {
			err = fmt.Errorf("%w: %v", errAgentFileStructureInvalid, structureErr)
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err == nil {
		return "", nil
	}
	value := agentRuntimeToolErrorValue(err)
	if !nativeEditRecoverableCondition(value) {
		return nativeEditRecoveryUnavailable(err)
	}
	value["message"] = firstNonEmpty(stringValue(value["message"]), stringValue(value["error"]))
	value["preflight"] = true
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func nativeEditRecoveryUnavailable(cause error) (string, error) {
	encoded, err := json.Marshal(map[string]any{
		"ok": false, "executed": false, "preflight": true, "retryable": true,
		"code":     "native_recovery_inspection_unavailable",
		"message":  "The candidate could not be inspected; no file write was performed.",
		"error":    cause.Error(),
		"recovery": "Inspect the reported local resource or access failure, or choose another authorized route. Preserve the current file and do not claim that this write completed.",
	})
	return string(encoded), err
}

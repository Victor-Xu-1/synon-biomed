package server

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

func runnerMachineValidationFailures(name, content string) []string {
	if !runnerMachineValidationArtifactName(name) {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(content, "\ufeff")))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return []string{"machine_validation_invalid_json:" + name}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return []string{"machine_validation_trailing_data:" + name}
	}
	if !runnerMachineValidationRoot(document) {
		return []string{"machine_validation_invalid_root:" + name}
	}
	state := runnerMachineValidationState{file: name}
	state.walk(document, "")
	if !state.hasPassingCheck {
		state.failures = append(state.failures, "machine_validation_missing_passing_check:"+name)
	}
	sort.Strings(state.failures)
	return state.failures
}

func runnerMachineValidationRoot(document any) bool {
	switch typed := document.(type) {
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return false
	}
}

func runnerMachineValidationArtifactName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if filepath.Ext(name) != ".json" {
		return false
	}
	return strings.Contains(name, "validation") || strings.Contains(name, "verify") ||
		strings.Contains(name, "验收") || strings.Contains(name, "校验") || strings.Contains(name, "验证")
}

type runnerMachineValidationState struct {
	file            string
	hasPassingCheck bool
	failures        []string
}

func (state *runnerMachineValidationState) walk(value any, path string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if runnerMachineValidationFailureCollectionKey(key) && runnerMachineValidationCollectionNonEmpty(child) {
				state.failures = append(state.failures, fmt.Sprintf("machine_validation_reported_failure:%s path=%s", state.file, childPath))
			}
			if text, ok := child.(string); ok && runnerMachineValidationFailureStatusKey(key) {
				if runnerMachineValidationFailureStatus(text) {
					state.failures = append(state.failures, fmt.Sprintf("machine_validation_failed_status:%s path=%s value=%s", state.file, childPath, strings.TrimSpace(text)))
				} else if runnerMachineValidationPassingStatus(text) {
					state.hasPassingCheck = true
				}
			}
			if flag, ok := child.(bool); ok && runnerMachineValidationCheckKey(key) {
				if flag {
					state.hasPassingCheck = true
				} else {
					state.failures = append(state.failures, fmt.Sprintf("machine_validation_false_check:%s path=%s", state.file, childPath))
				}
			}
			state.walk(child, childPath)
		}
	case []any:
		for index, child := range typed {
			state.walk(child, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}

func runnerMachineValidationCheckKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "ok" || key == "pass" || key == "passed" || key == "valid" || key == "validated" ||
		key == "verified" || key == "success" || key == "meets_requirements" || key == "all_requirements_met" {
		return true
	}
	switch key {
	case "input_fidelity", "missing_values_preserved", "cross_artifact_consistency", "source_integrity",
		"deterministic_reproducibility", "data_to_chart_consistency", "visual_quality", "process_cleanup":
		return true
	}
	for _, suffix := range []string{"_ok", "_pass", "_passed", "_valid", "_verified", "_consistent", "_complete", "_completed", "_success", "_clean", "_closed"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func runnerMachineValidationFailureCollectionKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "errors", "failures", "failed_checks", "validation_errors", "validation_failures":
		return true
	default:
		return false
	}
}

func runnerMachineValidationCollectionNonEmpty(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return false
	}
}

func runnerMachineValidationFailureStatusKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "status", "result", "validation_status", "validation_result", "check_result":
		return true
	default:
		return false
	}
}

func runnerMachineValidationFailureStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "failed", "failure", "error", "invalid", "incomplete", "blocked", "rejected":
		return true
	default:
		return false
	}
}

func runnerMachineValidationPassingStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok", "pass", "passed", "valid", "verified", "success", "succeeded", "complete", "completed":
		return true
	default:
		return false
	}
}

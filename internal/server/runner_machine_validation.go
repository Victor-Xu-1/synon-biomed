package server

import (
	"path/filepath"
	"strings"
)

func runnerMachineValidationArtifactName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(filepath.Base(name)))
	if filepath.Ext(name) != ".json" {
		return false
	}
	return strings.Contains(name, "validation") || strings.Contains(name, "verify") ||
		strings.Contains(name, "验收") || strings.Contains(name, "校验") || strings.Contains(name, "验证")
}

type runnerMachineValidationState struct {
	hasPassingCheck bool
	failures        []string
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

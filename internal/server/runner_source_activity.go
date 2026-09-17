package server

import (
	"strings"
	"sync"

	eventjournal "synon-go/internal/persistence/journal"
)

// Source activity is telemetry, not a model policy or a task budget.
type sessionRunnerSourceToolActivity struct {
	mu   sync.Mutex
	used int
}

func newSessionRunnerSourceToolActivity(used int) *sessionRunnerSourceToolActivity {
	if used < 0 {
		used = 0
	}
	return &sessionRunnerSourceToolActivity{used: used}
}

func (activity *sessionRunnerSourceToolActivity) record(capabilities []string) int {
	if activity == nil || !runtimeCapabilitiesContainSource(capabilities) {
		return 0
	}
	activity.mu.Lock()
	defer activity.mu.Unlock()
	activity.used++
	return activity.used
}

func runtimeCapabilitiesContainSource(capabilities []string) bool {
	for _, capability := range capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case runtimeCapabilityResearch, runtimeCapabilitySourceEvidence, "evidence-read", "source-download":
			return true
		}
	}
	return false
}

// legacySessionRunnerSourceTool is used only to reconstruct checkpoints written
// before Tool capabilities were persisted. New activity is classified solely
// from the immutable Tool schema snapshot.
func legacySessionRunnerSourceTool(toolName string) bool {
	switch normalizeAgentToolName(toolName) {
	case "websearch", "webfetch", "webresearch", "fetcharticlefulltext":
		return true
	default:
		return false
	}
}

func sessionRunnerGenericSourceToolAttemptCount(entries []eventjournal.Entry) int {
	settled := make(map[string]struct{})
	for _, entry := range entries {
		message := entry.Message
		capabilities := stringArrayValue(message["toolCapabilities"])
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" ||
			(!runtimeCapabilitiesContainSource(capabilities) &&
				!(len(capabilities) == 0 && legacySessionRunnerSourceTool(stringValue(message["toolName"])))) ||
			!sessionRunnerToolContinuityTerminalPhase(stringValue(message["toolPhase"])) {
			continue
		}
		if id := strings.TrimSpace(stringValue(message["toolCallId"])); id != "" {
			settled[id] = struct{}{}
		}
	}
	return len(settled)
}

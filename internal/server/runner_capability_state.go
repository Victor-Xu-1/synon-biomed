package server

import (
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

const (
	runtimeCapabilityResearch       = "research"
	runtimeCapabilitySourceEvidence = "source-evidence"
)

type sessionRunnerCapabilitySnapshot struct {
	ActivatedTools        []string `json:"activatedTools,omitempty"`
	ActivatedCapabilities []string `json:"activatedCapabilities,omitempty"`
}

func runtimeCapabilitiesContain(capabilities []string, capability string) bool {
	wanted := strings.ToLower(strings.TrimSpace(capability))
	if wanted == "" {
		return false
	}
	for _, candidate := range capabilities {
		if strings.EqualFold(strings.TrimSpace(candidate), wanted) {
			return true
		}
	}
	return false
}

// setToolCapabilityCatalog seals the executable Tool metadata captured for the
// current logical task. It mirrors Codex's turn-scoped registry snapshot: later
// plan, Skill, and recovery decisions may select from this catalog but cannot
// invent a capability or a second executor.
func (run *sessionRunnerChatRun) setToolCapabilityCatalog(schemas []agentruntime.ToolSchema) {
	if run == nil {
		return
	}
	catalog := make(map[string][]string, len(schemas))
	for _, schema := range schemas {
		key := normalizeAgentToolName(schema.Name)
		if key == "" {
			continue
		}
		catalog[key] = uniqueSortedFolded(effectiveAgentRuntimeToolCapabilities(schema))
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	run.toolCapabilityCatalog = catalog
}

func (run *sessionRunnerChatRun) toolCapabilities(toolName string) []string {
	if run == nil {
		return nil
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return append([]string(nil), run.toolCapabilityCatalog[normalizeAgentToolName(toolName)]...)
}

func (run *sessionRunnerChatRun) toolNamesForCapability(capability string) map[string]struct{} {
	result := make(map[string]struct{})
	wanted := strings.ToLower(strings.TrimSpace(capability))
	if run == nil || wanted == "" {
		return result
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	for name, capabilities := range run.toolCapabilityCatalog {
		for _, candidate := range capabilities {
			if strings.EqualFold(strings.TrimSpace(candidate), wanted) {
				result[name] = struct{}{}
				break
			}
		}
	}
	return result
}

func (run *sessionRunnerChatRun) activateToolCapability(toolName string, capabilities ...string) {
	if run == nil || strings.TrimSpace(toolName) == "" {
		return
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	key := normalizeAgentToolName(toolName)
	run.activatedToolNames = uniqueSortedFolded(append(run.activatedToolNames, key))
	if len(capabilities) == 0 {
		capabilities = run.toolCapabilityCatalog[key]
	}
	run.activatedToolCapabilities = uniqueSortedFolded(append(
		run.activatedToolCapabilities, capabilities...,
	))
}

func (run *sessionRunnerChatRun) restoreToolCapabilitiesFromEntries(entries []eventjournal.Entry) {
	if run == nil {
		return
	}
	for _, entry := range entries {
		message := entry.Message
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" ||
			strings.TrimSpace(stringValue(message["toolPhase"])) != "completed" ||
			boolValue(message["rejectedBeforeExecution"], false) {
			continue
		}
		name := strings.TrimSpace(stringValue(message["toolName"]))
		if name == "" {
			continue
		}
		run.activateToolCapability(name, stringArrayValue(message["toolCapabilities"])...)
	}
}

func (run *sessionRunnerChatRun) restoreToolCapabilitiesFromContinuity(
	records []sessionRunnerToolContinuityRecord,
) {
	if run == nil {
		return
	}
	for _, record := range records {
		if !record.Successful {
			continue
		}
		run.activateToolCapability(record.ToolName, record.ToolCapabilities...)
	}
}

func (run *sessionRunnerChatRun) capabilitySnapshot() sessionRunnerCapabilitySnapshot {
	if run == nil {
		return sessionRunnerCapabilitySnapshot{}
	}
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	return sessionRunnerCapabilitySnapshot{
		ActivatedTools: append([]string(nil), run.activatedToolNames...),
		ActivatedCapabilities: append(
			[]string(nil), run.activatedToolCapabilities...,
		),
	}
}

func (run *sessionRunnerChatRun) hasActivatedCapability(capability string) bool {
	wanted := strings.ToLower(strings.TrimSpace(capability))
	if run == nil || wanted == "" {
		return false
	}
	for _, candidate := range run.capabilitySnapshot().ActivatedCapabilities {
		if strings.EqualFold(strings.TrimSpace(candidate), wanted) {
			return true
		}
	}
	return false
}

// sessionRunnerSourceWorkflowActive derives source provenance behavior only
// from selected executable contracts and observed receipts. Task words,
// filenames, plan titles, and report prose are never activation authority.
func (s *Server) sessionRunnerSourceWorkflowActive(run *sessionRunnerChatRun) bool {
	if run == nil {
		return false
	}
	if run.hasActivatedCapability(runtimeCapabilityResearch) ||
		run.hasActivatedCapability(runtimeCapabilitySourceEvidence) ||
		sessionRunnerHasAuthoritativeSourceEvidence(run.trustedScientificReviewSignalsSnapshot()) {
		return true
	}
	if s == nil || s.skillCatalog == nil {
		return false
	}
	for _, skillName := range run.executedSkillNamesSnapshot() {
		skill, found := findCatalogSkill(s.skillCatalog, skillName)
		if !found {
			continue
		}
		for _, toolName := range skill.Tools {
			for _, capability := range run.toolCapabilities(toolName) {
				if strings.EqualFold(capability, runtimeCapabilityResearch) ||
					strings.EqualFold(capability, runtimeCapabilitySourceEvidence) {
					return true
				}
			}
		}
	}
	return false
}

package server

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

type managedEnvironmentSetupEvidenceCall struct {
	name string
	urls []string
}

type managedEnvironmentSetupEvidenceState struct {
	evidence     map[string]string
	missing      []string
	pendingReads map[string]string
}

func (s *Server) managedEnvironmentSetupEvidenceRequirement(
	ctx context.Context,
	implementation string,
) (map[string]any, error) {
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if s == nil || s.skillCatalog == nil || run == nil || strings.TrimSpace(implementation) == "" {
		return nil, nil
	}
	dedicated, found := dedicatedSkillForImplementation(s.skillCatalog, implementation)
	if !found || len(dedicated.SetupEvidenceURLs) == 0 || !runHasExecutedSkill(run, dedicated.Name) {
		return nil, nil
	}
	messages, err := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
	if err != nil {
		return nil, err
	}
	state := managedEnvironmentSetupEvidenceStateFromMessagesUsing(
		messages, dedicated.SetupEvidenceURLs, run.toolNamesForCapability("evidence-read"),
	)
	evidence, missing := state.evidence, state.missing
	if len(missing) == 0 {
		return nil, nil
	}
	return map[string]any{
		"tool": manageEnvironmentsToolName, "ok": true, "executed": false, "feasible": false,
		"status": "implementation_setup_evidence_required", "implementation": implementation,
		"required_skill": dedicated.Name, "missing_setup_evidence_urls": missing,
		"setup_evidence": evidence, "pending_setup_evidence_reads": state.pendingReads,
		"required_actions": managedEnvironmentSetupEvidenceRequiredActions(missing, state.pendingReads),
		"retry_contract":   "Execute every required_actions entry successfully before retrying this same mutation. Keep the selected implementation and do not change package versions, environment names, or implementation labels to bypass the missing evidence.",
		"recovery":         "Keep the selected implementation. Complete the exact structured required_actions, then derive one coherent environment and binary-source plan from the observed machine and those current official documents. Search titles/snippets, legacy version pages, and remembered compatibility tables are discovery only.",
	}, nil
}

func runHasExecutedSkill(run *sessionRunnerChatRun, name string) bool {
	if run == nil {
		return false
	}
	for _, loaded := range run.executedSkillNamesSnapshot() {
		if strings.EqualFold(strings.TrimSpace(loaded), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func managedEnvironmentSetupEvidenceFromMessages(
	messages []agentruntime.Message,
	required []string,
) (map[string]string, []string) {
	state := managedEnvironmentSetupEvidenceStateFromMessages(messages, required)
	return state.evidence, state.missing
}

func managedEnvironmentSetupEvidenceStateFromMessages(
	messages []agentruntime.Message,
	required []string,
) managedEnvironmentSetupEvidenceState {
	return managedEnvironmentSetupEvidenceStateFromMessagesUsing(
		messages, required, map[string]struct{}{
			"webfetch": {}, "webresearch": {},
		},
	)
}

func managedEnvironmentSetupEvidenceStateFromMessagesUsing(
	messages []agentruntime.Message,
	required []string,
	evidenceReaders map[string]struct{},
) managedEnvironmentSetupEvidenceState {
	calls := make(map[string]managedEnvironmentSetupEvidenceCall)
	readCalls := make(map[string]string)
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			name := strings.ToLower(strings.TrimSpace(call.Name))
			arguments := map[string]any{}
			if json.Unmarshal(call.Arguments, &arguments) != nil {
				continue
			}
			if name == "read_file" {
				if versionID := strings.TrimSpace(stringValue(arguments["version_id"])); versionID != "" {
					readCalls[strings.TrimSpace(call.ID)] = versionID
				}
				continue
			}
			if _, admitted := evidenceReaders[normalizeAgentToolName(name)]; !admitted {
				continue
			}
			urls := []string(nil)
			if value := strings.TrimSpace(stringValue(arguments["url"])); value != "" {
				urls = append(urls, value)
			}
			for _, raw := range anySliceValue(arguments["urls"]) {
				if value := strings.TrimSpace(stringValue(raw)); value != "" {
					urls = append(urls, value)
				}
			}
			calls[strings.TrimSpace(call.ID)] = managedEnvironmentSetupEvidenceCall{name: name, urls: urls}
		}
	}
	completedReads := make(map[string]string)
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		versionID := strings.TrimSpace(readCalls[strings.TrimSpace(message.ToolCallID)])
		if versionID == "" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) == nil &&
			agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			completedReads[versionID] = "tool-call:" + strings.TrimSpace(message.ToolCallID)
		}
	}
	evidence := make(map[string]string)
	pendingReads := make(map[string]string)
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		call, found := calls[strings.TrimSpace(message.ToolCallID)]
		if !found || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil ||
			agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
			continue
		}
		versionID := managedEnvironmentSetupEvidenceLargeResultVersionID(result)
		for _, observed := range call.urls {
			observedKey := managedEnvironmentSetupEvidenceURLKey(observed)
			for _, wanted := range required {
				if observedKey == "" || observedKey != managedEnvironmentSetupEvidenceURLKey(wanted) {
					continue
				}
				if versionID != "" {
					if readReceipt := strings.TrimSpace(completedReads[versionID]); readReceipt != "" {
						evidence[wanted] = "tool-call:" + strings.TrimSpace(message.ToolCallID) + "+" + readReceipt
					} else if strings.TrimSpace(evidence[wanted]) == "" {
						pendingReads[wanted] = versionID
					}
					continue
				}
				evidence[wanted] = "tool-call:" + strings.TrimSpace(message.ToolCallID)
			}
		}
	}
	missing := make([]string, 0)
	for _, wanted := range required {
		if strings.TrimSpace(evidence[wanted]) == "" {
			missing = append(missing, wanted)
		} else {
			delete(pendingReads, wanted)
		}
	}
	sort.Strings(missing)
	return managedEnvironmentSetupEvidenceState{
		evidence: evidence, missing: missing, pendingReads: pendingReads,
	}
}

func managedEnvironmentSetupEvidenceLargeResultVersionID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		versionID := strings.TrimSpace(stringValue(typed["version_id"]))
		if versionID != "" && (strings.TrimSpace(stringValue(typed["artifact_id"])) != "" ||
			strings.TrimSpace(stringValue(typed["read_with"])) != "") {
			return versionID
		}
		for _, key := range []string{"result", "data", "output"} {
			if nested := managedEnvironmentSetupEvidenceLargeResultVersionID(typed[key]); nested != "" {
				return nested
			}
		}
	case []any:
		for _, item := range typed {
			if nested := managedEnvironmentSetupEvidenceLargeResultVersionID(item); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func managedEnvironmentSetupEvidenceRequiredActions(
	missing []string,
	pendingReads map[string]string,
) []map[string]any {
	actions := make([]map[string]any, 0, len(missing))
	for _, wanted := range missing {
		if versionID := strings.TrimSpace(pendingReads[wanted]); versionID != "" {
			actions = append(actions, map[string]any{
				"tool": "read_file", "input": map[string]any{"version_id": versionID},
				"satisfies": wanted,
			})
			continue
		}
		actions = append(actions, map[string]any{
			"tool": "web_fetch", "input": map[string]any{"url": wanted, "limit": 2 << 20},
			"satisfies": wanted,
		})
	}
	return actions
}

func managedEnvironmentSetupEvidenceURLKey(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return ""
	}
	cleanPath := path.Clean("/" + strings.TrimSpace(parsed.EscapedPath()))
	if cleanPath != "/" {
		cleanPath = strings.TrimSuffix(cleanPath, "/")
	}
	return host + cleanPath
}

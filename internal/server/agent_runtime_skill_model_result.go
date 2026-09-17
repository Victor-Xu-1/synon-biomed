package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"synon-go/internal/skills"
)

const agentRuntimeSkillMetadataPrefix = `<skill-metadata name=`
const agentRuntimeLegacySkillMetadataPrefix = `<skill-metadata replay="legacy" name=`

// agentRuntimeSkillModelResult projects the compatibility Skill payload into
// the single readable contract consumed by the model. The compatibility API
// retains its structured fields for external callers, but replaying both
// skill.body and prompt duplicated the full SKILL.md before the runtime added
// it again as recovery context. workspace sends one metadata line followed
// by one authoritative body; use the same shape here so lower-capability model
// providers do not lose the preferred execution route inside repeated JSON.
func agentRuntimeSkillModelResult(value any) any {
	payload := mapValue(value)
	if payload == nil {
		return value
	}
	prompt := strings.TrimSpace(stringValue(payload["prompt"]))
	if prompt == "" {
		return value
	}
	skill := mapValue(payload["skill"])
	name := strings.TrimSpace(stringValue(skill["name"]))
	if name == "" {
		return value
	}
	description := strings.Join(strings.Fields(stringValue(skill["description"])), " ")
	header := agentRuntimeSkillMetadataPrefix + strconv.Quote(name)
	if description != "" {
		header += ` description=` + strconv.Quote(description)
	}
	header += " />"
	return fmt.Sprintf(
		"%s\n\n%s\n\nThis capability is now active for the current task. Continue with the requested work; do not reload the same capability unless its arguments or the task change.",
		header, prompt,
	)
}

func agentRuntimeLegacySkillModelResult(value any) any {
	projected := agentRuntimeSkillModelResult(value)
	text, ok := projected.(string)
	if !ok || !strings.HasPrefix(text, agentRuntimeSkillMetadataPrefix) {
		return projected
	}
	// A durable Skill receipt proves only that the capability was loaded in the
	// logical task. Its rendered body can contain an older materialized runtime
	// path after a deployment, so replaying that body would let stale assets
	// compete with the current catalog contract. Retain the bounded identity
	// receipt and let runner_execution inject exactly one freshly materialized
	// contract for the current turn.
	header := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	header = agentRuntimeLegacySkillMetadataPrefix + strings.TrimPrefix(header, agentRuntimeSkillMetadataPrefix)
	return header + "\n\nThe capability was loaded earlier in this logical task. Follow only the current runtime contract supplied by the system."
}

func providerVisibleSkillResultNames(messages []chatCompletionMessage) map[string]struct{} {
	names := make(map[string]struct{})
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		// Legacy identity-only receipts are audit evidence, not a current
		// provider-visible contract. The current catalog body is injected from
		// durable completed Skill names later in runner preparation.
		if !strings.HasPrefix(strings.TrimSpace(message.Content), agentRuntimeSkillMetadataPrefix) {
			continue
		}
		name, found := providerSkillResultName(message.Content)
		if !found {
			continue
		}
		names[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	return names
}

func providerSkillResultName(content string) (string, bool) {
	content = strings.TrimSpace(content)
	prefix := ""
	for _, candidate := range []string{agentRuntimeSkillMetadataPrefix, agentRuntimeLegacySkillMetadataPrefix} {
		if strings.HasPrefix(content, candidate) {
			prefix = candidate
			break
		}
	}
	if prefix == "" {
		return "", false
	}
	quoted := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(content, "\n", 2)[0], prefix))
	if index := strings.Index(quoted, " description="); index >= 0 {
		quoted = strings.TrimSpace(quoted[:index])
	}
	quoted = strings.TrimSpace(strings.TrimSuffix(quoted, "/>"))
	name, err := strconv.Unquote(quoted)
	if err != nil || strings.TrimSpace(name) == "" {
		return "", false
	}
	return strings.TrimSpace(name), true
}

// deactivateProviderSkillResultsForSelectedImplementation preserves the
// native tool-call/result pair while removing execution authority from a
// dedicated Skill superseded by a later exact AskUser answer. Historical
// receipts remain auditable; only the current catalog contract may guide the
// next action.
func deactivateProviderSkillResultsForSelectedImplementation(
	messages []chatCompletionMessage,
	catalog *skills.Catalog,
	selected []string,
	taskIntent string,
) []chatCompletionMessage {
	if len(messages) == 0 || catalog == nil {
		return messages
	}
	out := append([]chatCompletionMessage(nil), messages...)
	for index := range out {
		if out[index].Role != "tool" {
			continue
		}
		name, found := providerSkillResultName(out[index].Content)
		if !found {
			continue
		}
		skill, found := findCatalogSkill(catalog, name)
		if !found || skillAuthorizedByImplementationState(skill, selected, taskIntent) {
			continue
		}
		header := strings.TrimSpace(strings.SplitN(out[index].Content, "\n", 2)[0])
		out[index].Content = header + "\n\nThis historical dedicated capability is inactive because a later exact user answer selected another implementation. Follow only the current matching or implementation-agnostic capability contract."
	}
	return out
}

func providerVisibleSkillNames(messages []chatCompletionMessage) []string {
	visible := providerVisibleSkillResultNames(messages)
	names := make([]string, 0, len(visible))
	for name := range visible {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func compactProviderVisibleSkillResultBodies(messages []chatCompletionMessage) []chatCompletionMessage {
	out := append([]chatCompletionMessage(nil), messages...)
	for index := range out {
		if out[index].Role != "tool" {
			continue
		}
		content := strings.TrimSpace(out[index].Content)
		if !strings.HasPrefix(content, agentRuntimeSkillMetadataPrefix) {
			continue
		}
		header := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
		out[index].Content = header + "\n\nThe current task-scoped capability contract is supplied once in system context."
	}
	return out
}

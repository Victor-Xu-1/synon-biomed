package toolcontract

import "strings"

const SearchSkills = "search_skills"

const Skill = "skill"

const ListMCPTools = "ListMcpTools"

var runtimeExactAliases = map[string]string{
	"AgentOutputTool":  "TaskOutput",
	"BashOutputTool":   "TaskOutput",
	"Brief":            "SendUserMessage",
	"KillShell":        "TaskStop",
	"ListMcpToolsTool": ListMCPTools,
	"PowerShell":       "powershell",
	"Skill":            Skill,
	"SkillSearch":      SearchSkills,
	"SynonLink":        "synon_link",
	"Task":             "Agent",
	"WebFetch":         "web_fetch",
	"WebResearch":      "web_research",
	"WebSearch":        "web_search",
	"file_read_batch":  "ReadBatch",
	"glob":             "Glob",
	"grep":             "Grep",
	"mcp":              "MCPTool",
	"session_compact":  "Compact",
	"sleep":            "Sleep",
	"todo_write":       "TodoWrite",
	"update_step":      "update_step_status",
}

// NormalizeRuntimeName preserves whitespace normalization for ordinary ASCII
// tool identifiers while accepting compatibility aliases only as exact bytes.
// AskUser near misses remain rejected because they can otherwise create a
// different user-decision authority.
func NormalizeRuntimeName(name string) (string, bool) {
	if canonical, ok := CanonicalAskUser(name); ok {
		return canonical, true
	}
	if canonical, ok := runtimeExactAliases[name]; ok {
		return canonical, true
	}
	for index := 0; index < len(name); index++ {
		if name[index] >= 0x80 {
			return "", false
		}
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}
	comparison := strings.TrimPrefix(trimmed, "\ufeff")
	for _, alias := range askUserAliases {
		if strings.EqualFold(comparison, alias) {
			return "", false
		}
	}
	return trimmed, true
}

// CanonicalRuntimeAlias returns the canonical identity for one exact legacy
// inbound name. Aliases are never registrations or model-visible tools.
func CanonicalRuntimeAlias(name string) (string, bool) {
	canonical, ok := runtimeExactAliases[name]
	return canonical, ok
}

func RuntimeAliases() map[string]string {
	aliases := make(map[string]string, len(runtimeExactAliases))
	for alias, canonical := range runtimeExactAliases {
		aliases[alias] = canonical
	}
	return aliases
}

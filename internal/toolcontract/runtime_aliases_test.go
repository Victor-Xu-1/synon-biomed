package toolcontract

import "testing"

func TestNormalizeRuntimeNameRejectsAskUserNearMissesAndNonASCII(t *testing.T) {
	for _, name := range []string{
		" ask_user", "ask_user ", "ASK_USER", "Askuserquestion", "ASK_USER_QUESTION",
		"\ufeffask_user", "aſk_user", "аsk_user", "αsk_user", "工具", "\u00a0Read\u00a0",
	} {
		if got, ok := NormalizeRuntimeName(name); ok || got != "" {
			t.Fatalf("NormalizeRuntimeName(%q) = %q, %t; want rejected", name, got, ok)
		}
	}
	if got, ok := NormalizeRuntimeName(" Read "); !ok || got != "Read" {
		t.Fatalf("NormalizeRuntimeName ordinary tool = %q, %t", got, ok)
	}
}

func TestNormalizeRuntimeNameAcceptsOnlyExactListMCPToolsLegacyAlias(t *testing.T) {
	if got, ok := NormalizeRuntimeName("ListMcpToolsTool"); !ok || got != ListMCPTools {
		t.Fatalf("NormalizeRuntimeName legacy ListMcpTools alias = %q, %t", got, ok)
	}
	for _, name := range []string{" ListMcpToolsTool", "ListMcpToolsTool ", "listmcptoolstool", "ListMcpToolsTools"} {
		got, ok := NormalizeRuntimeName(name)
		if ok && got == ListMCPTools {
			t.Fatalf("NormalizeRuntimeName(%q) unexpectedly widened legacy alias authority", name)
		}
	}
}

func TestNormalizeRuntimeNameCollapsesRetiredCatalogAliases(t *testing.T) {
	want := map[string]string{
		"AgentOutputTool": "TaskOutput", "BashOutputTool": "TaskOutput",
		"Brief": "SendUserMessage", "KillShell": "TaskStop", "Task": "Agent",
		"SynonLink": "synon_link",
		"WebFetch":  "web_fetch", "WebSearch": "web_search", "WebResearch": "web_research",
		"file_read_batch": "ReadBatch", "glob": "Glob", "grep": "Grep", "mcp": "MCPTool",
		"session_compact": "Compact", "sleep": "Sleep", "todo_write": "TodoWrite",
		"update_step": "update_step_status",
	}
	for alias, canonical := range want {
		if got, ok := NormalizeRuntimeName(alias); !ok || got != canonical {
			t.Fatalf("NormalizeRuntimeName(%q) = %q, %t; want %q", alias, got, ok, canonical)
		}
		if got, ok := NormalizeRuntimeName(alias + " "); ok && got == canonical {
			t.Fatalf("NormalizeRuntimeName accepted padded retired alias %q as %q", alias, got)
		}
	}
}

func TestRuntimeAliasesReturnsAnIsolatedCatalogCopy(t *testing.T) {
	aliases := RuntimeAliases()
	aliases["WebSearch"] = "tampered"
	if canonical, ok := CanonicalRuntimeAlias("WebSearch"); !ok || canonical != "web_search" {
		t.Fatalf("runtime alias catalog was mutated through its public copy: %q, %t", canonical, ok)
	}
}

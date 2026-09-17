package assets_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

var positiveToolInvocation = regexp.MustCompile(`(?i)\b(?:call|calling|use|invoke|run|execute|through|via)\s+(?:the\s+)?`)

func TestBundledSkillRuntimeContractsStayClosed(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillsRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")
	catalog := skills.Load([]string{skillsRoot})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	toolUniverse := map[string]string{}
	for _, skill := range loaded {
		for _, tool := range skill.Tools {
			toolUniverse[strings.ToLower(tool)] = tool
		}
	}
	for _, tool := range []string{
		"web_search", "web_fetch", "fetch_article_fulltext", "read_file", "edit_file",
		"save_artifacts", "search_skills", "skill", "ask_user", "repl", "python", "bash",
		"manage_environments", "manage_packages", "generate_plan", "update_step_status",
		"wait_for_notification", "patent_search", "download_public_scientific_file",
	} {
		toolUniverse[strings.ToLower(tool)] = tool
	}

	retired := []string{
		"software_runtime", "search_rcsb_structures", "download_rcsb_file",
		"binding_mode_analysis", "AgentRuntimeDoctor",
	}
	for _, skill := range loaded {
		for _, name := range retired {
			if strings.Contains(skill.Body, name) || containsString(skill.Tools, name) {
				t.Errorf("%s retains retired runtime tool %q", skill.Name, name)
			}
		}
		if len(skill.Tools) == 0 {
			continue
		}
		allowed := map[string]bool{}
		for _, tool := range skill.Tools {
			allowed[strings.ToLower(tool)] = true
		}
		for lineIndex, line := range strings.Split(skill.Body, "\n") {
			lowerLine := strings.ToLower(line)
			if strings.Contains(lowerLine, "do not") || strings.Contains(lowerLine, "never ") {
				continue
			}
			for canonical, display := range toolUniverse {
				if allowed[canonical] || !lineInvokesDeclaredTool(line, display) {
					continue
				}
				t.Errorf("%s:%d invokes tool %q outside allowed-tools %v", skill.Name, lineIndex+1, display, skill.Tools)
			}
		}
	}

	domainsPath := filepath.Join(
		repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib", "mcp_bio", "domains.json",
	)
	domainContent, err := os.ReadFile(domainsPath)
	if err != nil {
		t.Fatal(err)
	}
	var domains map[string][]string
	if err := json.Unmarshal(domainContent, &domains); err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for domain, tools := range domains {
		for _, tool := range tools {
			served[domain+"/"+tool] = true
		}
	}
	mcpPattern := regexp.MustCompile(`host\.mcp\(\s*["']([a-z0-9-]+)["']\s*,\s*["']([a-z0-9_]+)["']`)
	missing := []string{}
	for _, skill := range loaded {
		for _, match := range mcpPattern.FindAllStringSubmatch(skill.Body, -1) {
			contract := match[1] + "/" + match[2]
			if !served[contract] {
				missing = append(missing, skill.Name+":"+contract)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("Skills reference unserved MCP contracts: %v", missing)
	}
}

func lineInvokesDeclaredTool(line, tool string) bool {
	escaped := regexp.QuoteMeta(tool)
	direct := regexp.MustCompile(`(?:^|[^a-zA-Z0-9_.-])` + escaped + `\(`)
	if direct.MatchString(line) {
		return true
	}
	for _, location := range regexp.MustCompile("(?i)`"+escaped+"`").FindAllStringIndex(line, -1) {
		if positiveToolInvocation.MatchString(line[:location[0]]) {
			return true
		}
	}
	return false
}

package v11reuse

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProducesDeterministicContentAddressedManifest(t *testing.T) {
	source := newFixture(t)
	rules := fixtureRules()

	first, err := Build(source, rules)
	if err != nil {
		t.Fatalf("Build() first error = %v", err)
	}
	second, err := Build(source, rules)
	if err != nil {
		t.Fatalf("Build() second error = %v", err)
	}

	firstJSON, err := Marshal(first)
	if err != nil {
		t.Fatalf("Marshal() first error = %v", err)
	}
	secondJSON, err := Marshal(second)
	if err != nil {
		t.Fatalf("Marshal() second error = %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("manifest is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if bytes.Contains(firstJSON, []byte(source)) {
		t.Fatalf("manifest leaked absolute source path: %s", firstJSON)
	}
	if first.SchemaVersion != 1 || first.Summary.Entries != 2 || first.Summary.Files != 3 || first.Summary.Symlinks != 1 {
		t.Fatalf("summary = %+v", first.Summary)
	}
	if first.SourceTreeSHA256 == "" || len(first.Entries) != 2 {
		t.Fatalf("manifest = %+v", first)
	}
	if first.Entries[0].ID != "agent-operon" || first.Entries[1].ID != "runtime-state" {
		t.Fatalf("entry order = %#v", first.Entries)
	}
	if first.Entries[0].SHA256 == "" || first.Entries[0].FileCount != 2 || first.Entries[0].SymlinkCount != 1 {
		t.Fatalf("agent entry = %+v", first.Entries[0])
	}

	writeFile(t, filepath.Join(source, "agents", "operon", "agent.md"), "changed\n")
	changed, err := Build(source, rules)
	if err != nil {
		t.Fatalf("Build() changed error = %v", err)
	}
	if changed.SourceTreeSHA256 == first.SourceTreeSHA256 || changed.Entries[0].SHA256 == first.Entries[0].SHA256 {
		t.Fatal("content change did not change entry and tree hashes")
	}
}

func TestBuildRejectsUnclassifiedContent(t *testing.T) {
	source := newFixture(t)
	writeFile(t, filepath.Join(source, "orphan.txt"), "not classified\n")

	_, err := Build(source, fixtureRules())
	if !errors.Is(err, ErrUnclassified) || !strings.Contains(err.Error(), "orphan.txt") {
		t.Fatalf("Build() error = %v, want ErrUnclassified naming orphan.txt", err)
	}
}

func TestBuildRejectsDuplicateClassification(t *testing.T) {
	source := newFixture(t)
	rules := fixtureRules()
	duplicate := rules[0]
	duplicate.ID = "agent-operon-duplicate"
	rules = append(rules, duplicate)

	_, err := Build(source, rules)
	if !errors.Is(err, ErrDuplicateClassification) || !strings.Contains(err.Error(), "agents/operon/agent.md") {
		t.Fatalf("Build() error = %v, want duplicate classification", err)
	}
}

func TestBuildRejectsRuleAndSymlinkPathEscape(t *testing.T) {
	source := newFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "private\n")

	rules := fixtureRules()
	escape := rules[0]
	escape.ID = "escape"
	escape.SourcePath = "../outside.txt"
	_, err := Build(source, []Rule{escape})
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("Build() traversal error = %v, want ErrPathEscape", err)
	}

	link := filepath.Join(source, "agents", "operon", "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	_, err = Build(source, fixtureRules())
	if !errors.Is(err, ErrPathEscape) || !strings.Contains(err.Error(), "outside-link") {
		t.Fatalf("Build() symlink error = %v, want escaping symlink", err)
	}
}

func TestBuildRejectsInvalidRuleMetadata(t *testing.T) {
	source := newFixture(t)
	testCases := []struct {
		name   string
		mutate func(*Rule)
	}{
		{name: "missing id", mutate: func(rule *Rule) { rule.ID = "" }},
		{name: "invalid decision", mutate: func(rule *Rule) { rule.Decision = Decision("copy-maybe") }},
		{name: "missing license", mutate: func(rule *Rule) { rule.Licenses = nil }},
		{name: "missing evidence", mutate: func(rule *Rule) { rule.Evidence = nil }},
		{name: "release target missing", mutate: func(rule *Rule) {
			rule.ReleaseRequired = true
			rule.TargetPath = ""
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rules := fixtureRules()
			testCase.mutate(&rules[0])
			_, err := Build(source, rules)
			if !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("Build() error = %v, want ErrInvalidRule", err)
			}
		})
	}
}

func newFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "agents", "operon", "agent.md"), "agent\n")
	writeFile(t, filepath.Join(source, "agents", "operon", "config.json"), "{}\n")
	writeFile(t, filepath.Join(source, "runtime", "state.log"), "runtime state\n")
	if err := os.Symlink("agent.md", filepath.Join(source, "agents", "operon", "agent-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	return source
}

func fixtureRules() []Rule {
	return []Rule{
		{
			ID:                  "agent-operon",
			SourcePath:          "agents/operon",
			Category:            "agent",
			Language:            "markdown-json",
			Decision:            DirectCopy,
			TargetPath:          "assets/synonbiomed/agents/operon",
			RuntimeDependencies: []string{"agent-runtime"},
			Evidence:            []string{"fixture agent loader"},
			Licenses:            []string{"SynonBiomed-v1.1-private"},
			Owner:               "agent-runtime",
			MainRuntimeWired:    true,
			ReleaseRequired:     true,
		},
		{
			ID:                  "runtime-state",
			SourcePath:          "runtime",
			Category:            "runtime-state",
			Language:            "data",
			Decision:            RuntimeStateExcluded,
			RuntimeDependencies: []string{},
			Evidence:            []string{"fixture runtime state"},
			Licenses:            []string{"not-applicable"},
			Owner:               "operations",
			MainRuntimeWired:    false,
			ReleaseRequired:     false,
		},
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

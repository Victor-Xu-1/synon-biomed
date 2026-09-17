package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestCompoundSourcingSkillUsesCanonicalPubChemRoute(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "compound-sourcing", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, marker := range []string{
		`host.mcp(`,
		`"chemistry",`,
		`"pubchem_search_compounds",`,
		`namespace="name"`,
		`max_cids=10`,
		`identity["properties"][0]["CID"]`,
		"The result is one object, not a list",
		"MolecularFormula",
		"InChIKey",
		"is not an MCP method",
		"is not a cataloged tool and must not be called",
		"provider-neutral web search",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("compound-sourcing contract is missing marker %q", marker)
		}
	}
	if strings.Contains(text, `host.mcp("chemistry", "compound_sourcing"`) {
		t.Fatal("compound-sourcing Skill still demonstrates the nonexistent combined MCP method")
	}

	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range manifest.Files {
		if entry.Path != "compound-sourcing/SKILL.md" {
			continue
		}
		found = true
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Fatalf("compound-sourcing manifest entry is stale: bytes=%d/%d sha256=%s/%s",
				entry.Bytes, len(content), entry.SHA256, digest)
		}
	}
	if !found {
		t.Fatal("compound-sourcing is missing from the bundled skills manifest")
	}

	fullCatalog := skills.Load([]string{filepath.Dir(skillPath)})
	limitedCatalog := skills.Load([]string{filepath.Dir(skillPath)}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := fullCatalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	if loadErrors := limitedCatalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatal(loadErrors[0].Err)
	}
	fullSkills, limitedSkills := fullCatalog.Skills(), limitedCatalog.Skills()
	if len(fullSkills) != 1 || len(limitedSkills) != 1 {
		t.Fatalf("unexpected compound-sourcing catalog sizes: full=%d limited=%d", len(fullSkills), len(limitedSkills))
	}
	if fullSkills[0].Body != limitedSkills[0].Body {
		t.Fatalf("compound-sourcing body exceeds the 12000-byte runtime budget: full=%d limited=%d",
			len(fullSkills[0].Body), len(limitedSkills[0].Body))
	}
}

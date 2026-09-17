package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestNGSAnalysisRouterReusesExistingCapabilitiesAndKeepsPreflightReadOnly(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "ngs-analysis-router")

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
	manifestFiles := make(map[string]struct {
		SHA256 string
		Bytes  int
	}, len(manifest.Files))
	for _, entry := range manifest.Files {
		manifestFiles[entry.Path] = struct {
			SHA256 string
			Bytes  int
		}{SHA256: entry.SHA256, Bytes: entry.Bytes}
	}
	for _, relative := range []string{
		"SKILL.md", "THIRD_PARTY_NOTICES.md", "scripts/ngs_preflight.py", "scripts/test_ngs_preflight.py",
	} {
		content, err := os.ReadFile(filepath.Join(skillRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Errorf("required ngs-analysis-router file %s: %v", relative, err)
			continue
		}
		entry, found := manifestFiles["ngs-analysis-router/"+relative]
		if !found {
			t.Errorf("ngs-analysis-router/%s is absent from the skills manifest", relative)
			continue
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		if entry.Bytes != len(content) || entry.SHA256 != digest {
			t.Errorf("ngs-analysis-router/%s manifest entry is stale", relative)
		}
	}

	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load ngs-analysis-router skill: %v", loadErrors[0].Err)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "ngs-analysis-router" {
		t.Fatalf("unexpected ngs-analysis-router catalog: %#v", loaded)
	}
	for _, tool := range []string{"python", "read_file", "search_skills", "skill", "ask_user"} {
		if !slices.Contains(loaded[0].Tools, tool) {
			t.Errorf("ngs-analysis-router tool dependency is missing: %s", tool)
		}
	}
	for _, query := range []string{"RNA-seq FASTQ analysis", "NGS variant workflow", "single-cell sequencing", "metagenomics reads"} {
		if matches := catalog.SearchNames(query, 5); !slices.Contains(matches, "ngs-analysis-router") {
			t.Errorf("ngs-analysis-router is not discoverable for %q: %v", query, matches)
		}
	}

	skillContent, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skillText := string(skillContent)
	for _, delegatedSkill := range []string{"parabricks", "genomics-workflow-acceleration", "single-cell-rna-analysis", "scvi-tools", "scgpt", "borzoi"} {
		if !strings.Contains(skillText, "`"+delegatedSkill+"`") {
			t.Errorf("existing Synon capability is not delegated through its bundled Skill: %s", delegatedSkill)
		}
	}
	for _, marker := range []string{
		"install or run it.", "do not upload local payloads", "explicit execution gap",
		"never overwrite inputs or a prior run", "assembly-consistent FASTA/index/annotation",
	} {
		if !strings.Contains(skillText, marker) {
			t.Errorf("NGS routing boundary is missing: %s", marker)
		}
	}
	for _, forbidden := range []string{"pip install", "conda install", "docker run", "requests.get", "requests.post"} {
		if strings.Contains(skillText, forbidden) {
			t.Errorf("router duplicates an installer, runner, or source client: %q", forbidden)
		}
	}

	preflightContent, err := os.ReadFile(filepath.Join(skillRoot, "scripts", "ngs_preflight.py"))
	if err != nil {
		t.Fatal(err)
	}
	preflight := string(preflightContent)
	for _, marker := range []string{
		"MAX_FILES = 100_000", "MAX_DEPTH = 12", "followlinks=False",
		"resolved.relative_to(root)", "path.is_symlink()", "payloadsRead\": False",
		"pass --overwrite to replace it", "os.replace(temporary, path)",
	} {
		if !strings.Contains(preflight, marker) {
			t.Errorf("bounded read-only preflight marker is missing: %s", marker)
		}
	}
	for _, forbidden := range []string{
		"import subprocess", "import requests", "import urllib", "import socket",
		"read_bytes(", "read_text(", "gzip.open(", "pysam", "shell=True",
	} {
		if strings.Contains(preflight, forbidden) {
			t.Errorf("preflight reads payloads, uses network, or executes programs: %q", forbidden)
		}
	}

	testContent, err := os.ReadFile(filepath.Join(skillRoot, "scripts", "test_ngs_preflight.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"test_inventory_classifies_inputs_without_reading_payloads",
		"test_skips_symlinks_and_rejects_workspace_escape",
		"test_cli_writes_only_the_explicit_workspace_report",
		"test_cli_requires_explicit_overwrite",
	} {
		if !strings.Contains(string(testContent), marker) {
			t.Errorf("NGS preflight regression coverage is missing: %s", marker)
		}
	}
}

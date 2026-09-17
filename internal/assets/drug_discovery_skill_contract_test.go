package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestDrugDiscoverySkillUsesBundledMCPContracts(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillPath := filepath.Join(repositoryRoot, "skills", "synonbiomed", "drug-discovery-pipeline", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	assemblerPath := filepath.Join(filepath.Dir(skillPath), "scripts", "assemble_docking_results.py")
	assemblerContent, err := os.ReadFile(assemblerPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json")
	manifestContent, err := os.ReadFile(manifestPath)
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
	foundManifestEntry := false
	foundAssemblerEntry := false
	for _, entry := range manifest.Files {
		switch entry.Path {
		case "drug-discovery-pipeline/SKILL.md":
			foundManifestEntry = true
			digest := fmt.Sprintf("%x", sha256.Sum256(content))
			if entry.Bytes != len(content) || entry.SHA256 != digest {
				t.Fatalf("drug-discovery-pipeline manifest entry is stale: bytes=%d/%d sha256=%s/%s",
					entry.Bytes, len(content), entry.SHA256, digest)
			}
		case "drug-discovery-pipeline/scripts/assemble_docking_results.py":
			foundAssemblerEntry = true
			digest := fmt.Sprintf("%x", sha256.Sum256(assemblerContent))
			if entry.Bytes != len(assemblerContent) || entry.SHA256 != digest {
				t.Fatal("drug-discovery result assembler manifest entry is stale")
			}
		}
	}
	if !foundManifestEntry {
		t.Fatal("drug-discovery-pipeline is missing from the bundled skills manifest")
	}
	if !foundAssemblerEntry {
		t.Fatal("drug-discovery result assembler is missing from the bundled skills manifest")
	}

	start := strings.Index(string(content), "## Existing MCP evidence contracts")
	end := strings.Index(string(content), "## Freeze the compute handoff")
	if start < 0 || end <= start {
		t.Fatal("evidence routing section is missing or malformed")
	}
	evidenceSection := string(content[start:end])
	for _, forbidden := range []string{"rest_request.py", "requests.", "http://", "https://"} {
		if strings.Contains(evidenceSection, forbidden) {
			t.Fatalf("evidence routing must reuse MCP tools, found direct-client marker %q", forbidden)
		}
	}

	domainsPath := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib", "mcp_bio", "domains.json")
	domainContent, err := os.ReadFile(domainsPath)
	if err != nil {
		t.Fatal(err)
	}
	var domains map[string][]string
	if err := json.Unmarshal(domainContent, &domains); err != nil {
		t.Fatal(err)
	}
	served := make(map[string]bool)
	for domain, tools := range domains {
		for _, tool := range tools {
			served[domain+"/"+tool] = true
		}
	}

	contractPattern := regexp.MustCompile(`host\.mcp\("([a-z0-9-]+)",\s*"([a-z0-9_]+)"`)
	used := make(map[string]bool)
	for _, match := range contractPattern.FindAllStringSubmatch(evidenceSection, -1) {
		contract := match[1] + "/" + match[2]
		if !served[contract] {
			t.Errorf("skill references MCP contract absent from domains.json: %s", contract)
		}
		used[contract] = true
	}
	for _, required := range []string{
		"chembl/compound_search",
		"chembl/get_bioactivity",
		"chembl/get_mechanism",
		"chembl/target_search",
		"chemistry/bindingdb_ligands_by_target",
		"chemistry/bindingdb_targets_by_compound",
		"chemistry/pubchem_search_compounds",
		"genes-ontologies/get_uniprot_entries",
		"pubmed/search_articles",
		"structures-interactions/alphafold_get_prediction",
		"structures-interactions/pdb_select_latest_liganded_structure",
	} {
		if !used[required] {
			t.Errorf("required evidence contract is missing from skill: %s", required)
		}
	}
	for _, marker := range []string{
		"Read `targets`, not `records`",
		"components[].accession",
		"components[].gene_symbol",
		"accessions=[verified_accession]",
		"Fields mode returns `records` (not `entries`)",
		"invent a UniProt search method",
		"release-date-descending",
		"rejected_newer_candidates",
		"Do not replace it with repeated",
	} {
		if !strings.Contains(evidenceSection, marker) {
			t.Errorf("target-normalization contract is missing marker %q", marker)
		}
	}
	if strings.Contains(evidenceSection, "details.sort(") {
		t.Fatal("latest-structure recipe still hydrates and sorts the full result set client-side")
	}
	invalidUniProtCall := regexp.MustCompile(`get_uniprot_entries[\s\S]{0,180}(gene_symbol|organism=|query=|accession=)`)
	if invalidUniProtCall.MatchString(evidenceSection) {
		t.Fatal("target-normalization contract demonstrates an invalid get_uniprot_entries call")
	}

	fullCatalog := skills.Load([]string{filepath.Dir(skillPath)})
	limitedCatalog := skills.Load([]string{filepath.Dir(skillPath)}, skills.LoadOptions{MaxBodyBytes: 12000})
	if loadErrors := fullCatalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load upgraded skill: %v", loadErrors[0].Err)
	}
	if loadErrors := limitedCatalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load upgraded skill with runtime budget: %v", loadErrors[0].Err)
	}
	fullSkills, limitedSkills := fullCatalog.Skills(), limitedCatalog.Skills()
	if len(fullSkills) != 1 || len(limitedSkills) != 1 {
		t.Fatalf("unexpected drug-discovery-pipeline catalog sizes: full=%d limited=%d", len(fullSkills), len(limitedSkills))
	}
	if fullSkills[0].Body != limitedSkills[0].Body {
		t.Fatalf("drug-discovery-pipeline body exceeds the 12000-byte runtime budget: full=%d limited=%d",
			len(fullSkills[0].Body), len(limitedSkills[0].Body))
	}
	if !slices.Equal(limitedSkills[0].PreferredExecutionAssets, []string{"scripts/assemble_docking_results.py"}) {
		t.Fatalf("preferred execution assets=%#v", limitedSkills[0].PreferredExecutionAssets)
	}
	criticalConstraints := strings.Join(limitedSkills[0].CriticalConstraints, "\n")
	for _, marker := range []string{
		"never calculate or edit", "Publish the assembler ranking", "never add a features field",
		"binding-mode-analysis execution asset", "identical candidate IDs",
	} {
		if !strings.Contains(criticalConstraints, marker) {
			t.Errorf("critical constraints are missing %q: %#v", marker, limitedSkills[0].CriticalConstraints)
		}
	}
	matches := limitedCatalog.SearchNames("medicinal chemistry evidence", 3)
	if len(matches) == 0 || matches[0] != "drug-discovery-pipeline" {
		t.Fatalf("upgraded skill is not discoverable for evidence routing: %v", matches)
	}
	for _, marker := range []string{"GenMol", "DiffDock", "Boltz2"} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("existing compute pipeline marker was lost: %s", marker)
		}
	}
	for _, delegatedSkill := range []string{
		"structure-based-molecule-generation", "genmol-nim", "diffdock-nim", "autodock-vina", "boltz2-nim",
	} {
		if !strings.Contains(string(content), "`"+delegatedSkill+"`") {
			t.Errorf("existing compute capability is not delegated through its bundled Skill: %s", delegatedSkill)
		}
	}
	if strings.Contains(string(content), "Candidate generation belongs only to `medicinal-chemistry-optimization`") {
		t.Fatal("drug-discovery routing still hardcodes every generation request to RDKit analog enumeration")
	}
	if !strings.Contains(string(content), "Result assembly is generator-agnostic") ||
		!strings.Contains(string(content), "calculation does not change which engine generated them") {
		t.Fatal("drug-discovery result assembly does not preserve generator provenance")
	}
	for _, marker := range []string{
		`${SYNON_SKILL_DIR}/scripts/assemble_docking_results.py`, "candidate_ranking.csv", "results_summary.json",
		"project_report.md", "--report-language", "docking_complex_ensemble.pdb", "docking_components.csv",
		"--structures-sdf", "--structures-smiles", "candidate IDs and canonical SMILES across", "Header-only",
		"docking_pose_scores.csv", "docking_pose_samples.csv", "one primary best-scoring pose",
		"internal multi-run sample ledger",
		"candidate IDs and canonical SMILES", `skill("drug-discovery-pipeline")`,
		"pandas/RDKit merge", "report with `edit_file`",
		"never add a features field", "binding-mode-analysis execution asset", "identical candidate IDs",
	} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("deterministic result contract is missing marker %q", marker)
		}
	}
	for _, marker := range []string{
		"candidate identity mismatch", "candidate_ranking.csv", "results_summary.json", "project_report.md",
		"read_smiles_records", "read_sdf_records", "structure_set_integrity", "synon.structure-guided-candidate-summary.v4",
		"repeat_count", "poses_per_candidate", "exactly one primary pose",
		"source_sha256", "lipinski_violations", "Recommended review sequence", "建议的项目复核顺序",
	} {
		if !strings.Contains(string(assemblerContent), marker) {
			t.Errorf("deterministic result assembler is missing marker %q", marker)
		}
	}
	for _, forbidden := range []string{
		"requests.post", "requests.get", "import requests", "http://", "https://",
		"docker run", "from vina import", "shutil.which", "pip install", "every candidate's best pose",
	} {
		if strings.Contains(string(content), forbidden) {
			t.Errorf("pipeline duplicates a source client or compute runtime: %q", forbidden)
		}
	}
}

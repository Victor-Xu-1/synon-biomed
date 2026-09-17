package assets_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

const designStrategyEntrypointBudgetBytes = 16_000

func TestProteinAntibodyAndRNADesignStrategiesAreSeparateBilingualContracts(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillsRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")

	type manifestFile struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int    `json:"bytes"`
	}
	var manifest struct {
		Skills []string       `json:"skills"`
		Files  []manifestFile `json:"files"`
	}
	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	manifestByPath := map[string]manifestFile{}
	for _, entry := range manifest.Files {
		manifestByPath[entry.Path] = entry
	}

	tests := []struct {
		name             string
		referenceMarkers []string
		artifactMarkers  []string
		queries          []string
	}{
		{
			name:             "protein-design-strategy",
			referenceMarkers: []string{"RFdiffusion", "ProteinMPNN", "LigandMPNN", "Boltz2/OpenFold3"},
			artifactMarkers:  []string{"editable PDB/mmCIF", "FASTA sequences", "candidate-by-stage table", "stable ID", "readable report"},
			queries: []string{
				"de novo protein binder backbone and inverse-folding design",
				"蛋白骨架生成、结合蛋白设计和序列反向折叠",
			},
		},
		{
			name:             "antibody-design-strategy",
			referenceMarkers: []string{"RFantibody", "AbX/DiffAb", "IgLM", "IgFold"},
			artifactMarkers:  []string{"paired heavy/light or VHH FASTA", "numbering and CDR table", "PDB/mmCIF files", "stable ID", "professional report"},
			queries: []string{
				"epitope-specific nanobody CDR design and humanization",
				"表位特异性纳米抗体、CDR 优化和抗体人源化",
			},
		},
		{
			name:             "rna-design-strategy",
			referenceMarkers: []string{"ViennaRNA", "NUPACK 4", "gRNAde", "LinearDesign", "RNAstructure/OligoWalk"},
			artifactMarkers:  []string{"FASTA/CSV sequences", "dot-bracket/CT", "PDB or mmCIF", "stable IDs", "readable report"},
			queries: []string{
				"RNA inverse folding, multi-strand design, and mRNA coding optimization",
				"RNA 反向折叠、多链核酸设计和 mRNA 密码子优化",
			},
		},
	}

	catalog := skills.Load([]string{skillsRoot}, skills.LoadOptions{MaxBodyBytes: designStrategyEntrypointBudgetBytes})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load bundled Skills: %v", loadErrors[0].Err)
	}
	for _, test := range tests {
		if !slices.Contains(manifest.Skills, test.name) {
			t.Errorf("%s is missing from the bundled manifest", test.name)
		}
		for _, relativePath := range []string{test.name + "/SKILL.md", test.name + "/references/methods.md"} {
			content, err := os.ReadFile(filepath.Join(skillsRoot, filepath.FromSlash(relativePath)))
			if err != nil {
				t.Errorf("read %s: %v", relativePath, err)
				continue
			}
			entry, found := manifestByPath[relativePath]
			if !found {
				t.Errorf("%s is missing from the bundled manifest", relativePath)
				continue
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(content))
			if entry.Bytes != len(content) || entry.SHA256 != digest {
				t.Errorf("%s manifest entry is stale", relativePath)
			}
		}

		rawSkillContent := mustRead(t, filepath.Join(skillsRoot, test.name, "SKILL.md"))
		rawReferenceContent := mustRead(t, filepath.Join(skillsRoot, test.name, "references", "methods.md"))
		skillContent := strings.Join(strings.Fields(rawSkillContent), " ")
		referenceContent := strings.Join(strings.Fields(rawReferenceContent), " ")
		for _, marker := range []string{
			"## Ask at a material route choice",
			"machine CPU/GPU/RAM/disk",
			"call `ask_user` once",
			"recommend one",
			"principal advantage",
			"limitation",
			"resource",
			"deliverables",
			"conversation language",
		} {
			if !strings.Contains(skillContent, marker) {
				t.Errorf("%s is missing decision marker %q", test.name, marker)
			}
		}
		for _, marker := range test.referenceMarkers {
			if !strings.Contains(referenceContent, marker) {
				t.Errorf("%s method reference is missing %q", test.name, marker)
			}
		}
		for _, marker := range test.artifactMarkers {
			if !strings.Contains(skillContent, marker) {
				t.Errorf("%s artifact contract is missing %q", test.name, marker)
			}
		}
		for _, marker := range []string{
			"Resource facts for an informed choice",
			"ask_user",
			"official",
		} {
			if !strings.Contains(referenceContent, marker) {
				t.Errorf("%s resource guidance is missing %q", test.name, marker)
			}
		}
		for _, query := range test.queries {
			matches := catalog.Search(query, 5)
			if len(matches) == 0 || matches[0].Name != test.name {
				t.Errorf("query %q routed to %#v instead of %s", query, matches, test.name)
			}
		}
		for _, forbidden := range []string{"CRBN", "KRAS", "MDM2", "9GBJ", "12BP"} {
			if strings.Contains(rawSkillContent, forbidden) || strings.Contains(rawReferenceContent, forbidden) {
				t.Errorf("%s contains task-specific hardcoding %q", test.name, forbidden)
			}
		}
	}

	byName := map[string]skills.Skill{}
	for _, skill := range catalog.Skills() {
		byName[skill.Name] = skill
	}
	for _, test := range tests {
		loaded := byName[test.name]
		if loaded.Name == "" {
			t.Errorf("%s was not loaded", test.name)
			continue
		}
		for _, tool := range []string{"search_skills", "skill", "ask_user", "repl", "list_compute", "manage_environments", "manage_packages", "save_artifacts"} {
			if !slices.Contains(loaded.Tools, tool) {
				t.Errorf("%s is missing tool %q", test.name, tool)
			}
		}
		constraints := strings.Join(loaded.CriticalConstraints, "\n")
		for _, marker := range []string{"ask_user", "readiness preflight", "conversation language"} {
			if !strings.Contains(constraints, marker) {
				t.Errorf("%s critical constraints are missing %q", test.name, marker)
			}
		}
	}

	for _, name := range []string{
		"structure-based-molecule-generation",
		"protein-design-strategy",
		"antibody-design-strategy",
		"rna-design-strategy",
	} {
		raw := mustRead(t, filepath.Join(skillsRoot, name, "SKILL.md"))
		parts := strings.SplitN(raw, "---", 3)
		if len(parts) != 3 {
			t.Errorf("%s frontmatter boundary is invalid", name)
			continue
		}
		expectedBody := strings.TrimSpace(parts[2])
		if len(expectedBody) > designStrategyEntrypointBudgetBytes {
			t.Errorf("%s body is %d bytes and exceeds the %d-byte entrypoint budget", name, len(expectedBody), designStrategyEntrypointBudgetBytes)
		}
		if loaded := byName[name]; loaded.Name == "" || loaded.Body != expectedBody {
			t.Errorf("%s body was truncated or changed at the %d-byte entrypoint budget", name, designStrategyEntrypointBudgetBytes)
		}
	}
}

func TestGenMolSkillUsesOneCurrentV2RequestContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	genMolRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed", "genmol-nim")
	combined := strings.Join([]string{
		mustRead(t, filepath.Join(genMolRoot, "SKILL.md")),
		mustRead(t, filepath.Join(genMolRoot, "references", "api.md")),
		mustRead(t, filepath.Join(genMolRoot, "references", "examples.md")),
		mustRead(t, filepath.Join(genMolRoot, "references", "parameters.md")),
		mustRead(t, filepath.Join(genMolRoot, "references", "science.md")),
		mustRead(t, filepath.Join(genMolRoot, "references", "validation.md")),
	}, "\n")
	for _, marker := range []string{
		"nvcr.io/nim/nvidia/genmol:2.0.0",
		"NV-GenMol-89M-v2",
		"smiles: null",
		"temperature",
		"0.01",
		"gamma",
		"filter",
		"deprecated and ignored in v2",
		"ligand/fragment conditioned",
	} {
		if !strings.Contains(combined, marker) {
			t.Errorf("GenMol v2 contract is missing %q", marker)
		}
	}
	for _, stale := range []string{
		"nvcr.io/nim/nvidia/genmol:1.0.1",
		"temperature\": \"1.0\"",
		"noise\": \"1.0\"",
		"string, not float",
		"chmod 777",
	} {
		if strings.Contains(combined, stale) {
			t.Errorf("GenMol retained stale request contract %q", stale)
		}
	}
}

func TestProfessionalDesignExecutionSkillsUseOneEvidenceBasedRouteChoice(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillsRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")
	for _, name := range []string{
		"genmol-nim", "molmim-nim", "diffdock-nim", "rfdiffusion-nim",
		"proteinmpnn-nim", "boltz2-nim", "openfold3-nim",
	} {
		content := strings.Join(strings.Fields(mustRead(t, filepath.Join(skillsRoot, name, "SKILL.md"))), " ")
		for _, marker := range []string{
			"Do not begin with an uninformed hosted-versus-local question",
			"resource preflight",
			"If one route is viable, use it",
			"principal advantage",
			"resource requirement",
			"conversation language",
		} {
			if !strings.Contains(content, marker) {
				t.Errorf("%s is missing route-choice marker %q", name, marker)
			}
		}
		for _, retired := range []string{
			"Ask only when context is unclear",
			"Hosted NVIDIA API or local Docker NIM?",
		} {
			if strings.Contains(content, retired) {
				t.Errorf("%s retained competing route-choice text %q", name, retired)
			}
		}
		apiContent := strings.Join(strings.Fields(mustRead(t, filepath.Join(skillsRoot, name, "references", "api.md"))), " ")
		executionContent := content + " " + apiContent
		for _, marker := range []string{"install -d -m 0770", "--user \"$(id -u):0\""} {
			if !strings.Contains(executionContent, marker) {
				t.Errorf("%s cache contract is missing %q", name, marker)
			}
		}
		if strings.Contains(executionContent, "chmod 777") {
			t.Errorf("%s retained world-writable cache instructions", name)
		}
	}

	complexa := strings.Join(strings.Fields(mustRead(t, filepath.Join(skillsRoot, "complexa-design", "SKILL.md"))), " ")
	for _, marker := range []string{
		"installed Complexa CLI's own status and validation commands",
		"governed capability-acquisition path",
		"Use `ask_user` only after preflight",
		"principal advantage, limitation, resource requirement, and expected output",
		"Preserve reproducibility evidence",
		"save_artifacts",
	} {
		if !strings.Contains(complexa, marker) {
			t.Errorf("complexa-design is missing %q", marker)
		}
	}
	for _, retired := range []string{
		"runtime/assets/skills",
		"have the user run",
		"If the user did not specify, this is what they want",
		"Use ask_user to fill in the four parameters that vary every run",
		"./complexa_setup/preflight.json",
	} {
		if strings.Contains(complexa, retired) {
			t.Errorf("complexa-design retained invalid or competing path %q", retired)
		}
	}
}

func TestComplexaDesignDelegationClosureUsesOnlyBundledOrGovernedRoutes(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillsRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")
	names := []string{
		"complexa-design", "complexa-setup", "complexa-target", "complexa-sweep",
		"complexa-evaluate-pdbs", "complexa-slurm",
	}
	for _, name := range names {
		root := filepath.Join(skillsRoot, name)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(content)
			for _, retired := range []string{
				"runtime/assets/skills",
				"_shared/scripts",
				"_shared/reference",
				"./complexa_setup/preflight.json",
				"write_manifest.py",
			} {
				if strings.Contains(text, retired) {
					t.Errorf("%s retained unavailable path %q", filepath.ToSlash(path), retired)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	setup := strings.Join(strings.Fields(mustRead(t, filepath.Join(skillsRoot, "complexa-setup", "SKILL.md"))), " ")
	for _, marker := range []string{
		"installed complexa CLI",
		"capability-acquisition",
		"list_compute",
		"complexa download --status",
		"complexa validate design <pipeline-config>",
		"There is no bundled shared preflight or manifest helper",
	} {
		if !strings.Contains(setup, marker) {
			t.Errorf("complexa-setup is missing %q", marker)
		}
	}
	for _, forbidden := range []string{"sudo apt-get", "pip install -e", "git clone http", "rm -rf"} {
		if strings.Contains(setup, forbidden) {
			t.Errorf("complexa-setup retained unmanaged mutation %q", forbidden)
		}
	}

	slurmSkill := mustRead(t, filepath.Join(skillsRoot, "complexa-slurm", "SKILL.md"))
	if !strings.Contains(slurmSkill, `${SYNON_SKILL_DIR}/scripts/cluster_preflight.sh`) {
		t.Fatal("complexa-slurm does not use its task-scoped bundled preflight")
	}
	script := filepath.Join(skillsRoot, "complexa-slurm", "scripts", "cluster_preflight.sh")
	command := exec.Command("bash", script, "--help")
	command.Dir = repositoryRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cluster preflight help failed: %v\n%s", err, output)
	}
}

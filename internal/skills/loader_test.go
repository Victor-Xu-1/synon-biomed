package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestLoadRejectsSymbolicLinkSkillManifest(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-skill.md")
	if err := os.WriteFile(outside, []byte("---\nname: escaped-skill\ndescription: must not load\n---\nESCAPED_SKILL_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "escaped-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	catalog := Load([]string{root})
	if skills := catalog.Skills(); len(skills) != 0 {
		t.Fatalf("symbolic link manifest loaded unexpectedly: %#v", skills)
	}
	errs := catalog.LoadErrors()
	if len(errs) != 1 || !strings.Contains(errs[0].Err, "symbolic link") {
		t.Fatalf("load errors=%#v", errs)
	}
}

func TestCatalogUpsertIsConcurrentWithSearch(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{Name: "base", Description: "base runtime skill", Body: "base"})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				catalog.UpsertSkill(Skill{Name: "dynamic", Description: "dynamic runtime skill", Body: "updated"})
				_ = catalog.Search("runtime", 10)
				_ = catalog.Skills()
			}
		}(worker)
	}
	workers.Wait()
	selected := catalog.Search("select:dynamic", 2)
	if len(selected) != 1 || selected[0].Body != "updated" {
		t.Fatalf("dynamic skill = %#v", selected)
	}
}

func TestBuiltinRuntimeCatalogUsesProductIdentity(t *testing.T) {
	loaded := BuiltinRuntimeCatalog().Skills()
	if len(loaded) != 1 {
		t.Fatalf("builtin skills = %#v", loaded)
	}
	if !strings.Contains(loaded[0].Description, "Synon Biomed runtime") ||
		!strings.Contains(loaded[0].Body, "Synon Biomed runtime contract") ||
		strings.Contains(loaded[0].Description, "Synon Go") || strings.Contains(loaded[0].Body, "Synon Go") {
		t.Fatalf("builtin runtime identity description=%q body=%q", loaded[0].Description, loaded[0].Body)
	}
	if !strings.Contains(loaded[0].Body, "host.delegate") {
		t.Fatalf("builtin runtime skill omitted the single supervision authority: %q", loaded[0].Body)
	}
	for _, retired := range []string{"TaskRun", "AgentRuntimeDoctor", "ToolSearch", "SkillSearch"} {
		if strings.Contains(loaded[0].Body, retired) || slices.Contains(loaded[0].Tools, retired) {
			t.Fatalf("builtin runtime skill retained retired model instruction %q: tools=%#v body=%q", retired, loaded[0].Tools, loaded[0].Body)
		}
	}
}

func TestLoadSkillCatalogLoadsReferencedFilesFromSkillDir(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(skillDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir skill dirs: %v", err)
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(`---
name: runtime-helper
description: helper with references
references:
  - nested/readme.md
---
Use references for runtime behavior.
`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "nested", "readme.md"), []byte("local reference included"), 0o600); err != nil {
		t.Fatalf("write local reference: %v", err)
	}

	catalog := Load([]string{skillDir}, LoadOptions{MaxBodyBytes: 12000})
	skills := catalog.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "runtime-helper" {
		t.Fatalf("unexpected skill name %q", skills[0].Name)
	}

	context := catalog.BuildSkillContext(skills, 12000, root)
	if !strings.Contains(context, "Runtime-selected SKILL.md contexts:") {
		t.Fatalf("context header missing: %s", context)
	}
	if !strings.Contains(context, "Source: "+skillPath) {
		t.Fatalf("skill source path missing: %s", context)
	}
	if !strings.Contains(context, "References: nested/readme.md") {
		t.Fatalf("skill reference list missing: %s", context)
	}
	if !strings.Contains(context, "Referenced files:") || !strings.Contains(context, "local reference included") {
		t.Fatalf("reference content missing: %s", context)
	}
}

func TestLoadSkillCatalogParsesBoundedCriticalConstraints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(`---
name: evidence-aware-workflow
description: Evidence-aware workflow.
critical-constraints:
  - Do not infer an unknown measurement from a proxy.
  - Keep unsupported thresholds unresolved.
---
Use evidence to complete the task.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := Load([]string{root}).Skills()
	if len(loaded) != 1 || !slices.Equal(loaded[0].CriticalConstraints, []string{
		"Do not infer an unknown measurement from a proxy.",
		"Keep unsupported thresholds unresolved.",
	}) {
		t.Fatalf("critical constraints=%#v", loaded)
	}
}

func TestLoadSkillCatalogParsesImplementationIdentities(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(`---
name: engine-runtime
description: Dedicated engine runtime.
implementation-identities:
  - Public Engine
  - Public Engine Cloud
---
Run the selected implementation.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := Load([]string{root}).Skills()
	if len(loaded) != 1 || !slices.Equal(loaded[0].ImplementationIdentities, []string{
		"Public Engine", "Public Engine Cloud",
	}) {
		t.Fatalf("implementation identities=%#v", loaded)
	}
}

func TestLoadSkillCatalogParsesSetupEvidenceURLs(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(`---
name: setup-evidence
description: setup evidence contract
setup-evidence-urls:
  - https://framework.example.org/current/install/
  - https://extensions.example.org/compatibility.html
---
Use current official compatibility evidence before setup.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{directory})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatal(errors)
	}
	loaded := catalog.Skills()
	want := []string{
		"https://framework.example.org/current/install/",
		"https://extensions.example.org/compatibility.html",
	}
	if len(loaded) != 1 || !slices.Equal(loaded[0].SetupEvidenceURLs, want) {
		t.Fatalf("setup evidence URLs=%v", loaded)
	}
}

func TestLoadSkillCatalogParsesRequiredSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(`---
name: deep-review
description: Deep review methodology.
metadata:
  dependencies:
    skills:
      - literature-review
      - patent-search
---
Use the composed methodology.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := Load([]string{root}).Skills()
	if len(loaded) != 1 || !slices.Equal(loaded[0].RequiredSkills, []string{"literature-review", "patent-search"}) {
		t.Fatalf("required skills=%#v", loaded)
	}
}

func TestLoadSkillCatalogRejectsInvalidOrDuplicateRequiredSkills(t *testing.T) {
	for name, dependencies := range map[string]string{
		"invalid":   "      - ../literature-review\n",
		"duplicate": "      - literature-review\n      - Literature-Review\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			content := "---\nname: deep-review\ndescription: Deep review methodology.\nmetadata:\n  dependencies:\n    skills:\n" + dependencies + "---\nBody.\n"
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := Load([]string{root})
			if len(catalog.LoadErrors()) != 1 || len(catalog.Skills()) != 0 {
				t.Fatalf("load errors=%#v skills=%#v", catalog.LoadErrors(), catalog.Skills())
			}
		})
	}
}

func TestLoadSkillCatalogRejectsOversizeBodyWithoutTruncating(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("完整方法。", 64)
	content := "---\nname: complete-method\ndescription: Complete method.\n---\n" + body
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root}, LoadOptions{MaxBodyBytes: 128, RejectOversizeBody: true})
	if len(catalog.Skills()) != 0 || len(catalog.LoadErrors()) != 1 ||
		!strings.Contains(catalog.LoadErrors()[0].Err, "exceeds 128 bytes") {
		t.Fatalf("oversize catalog skills=%#v errors=%#v", catalog.Skills(), catalog.LoadErrors())
	}
}

func TestLoadSkillCatalogDoesNotTreatNestedThirdPartyNameAsSkillName(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "alphafold2")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: alphafold2
description: >
  Predict protein structures with a verified
  third-party model integration.
tags: [protein, structure]
metadata:
  third_party:
    - kind: weights
      name: AlphaFold2 model weights
      license: CC-BY-4.0
    - kind: service
      name: ColabFold MSA server
---
# AlphaFold2
Use the managed runner.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load errors=%#v", errors)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "alphafold2" {
		t.Fatalf("loaded skills=%#v", loaded)
	}
	if loaded[0].Description != "Predict protein structures with a verified third-party model integration." {
		t.Fatalf("folded description=%q", loaded[0].Description)
	}
	if len(loaded[0].Tags) != 2 || loaded[0].Tags[0] != "protein" || loaded[0].Tags[1] != "structure" {
		t.Fatalf("tags=%#v", loaded[0].Tags)
	}
}

func TestLoadSkillCatalogAcceptsV11LegacyColonInPlainDescription(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "reaction-engineering")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: reaction-engineering
description: Reactor design methods (FUG: Fenske-Underwood-Gilliland) for process engineering.
---
# Reaction Engineering
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load errors=%#v", errors)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 || loaded[0].Name != "reaction-engineering" || !strings.Contains(loaded[0].Description, "FUG:") {
		t.Fatalf("loaded skills=%#v", loaded)
	}
}

func TestLoadSkillCatalogPreservesV11AllowedTools(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "v11-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: v11-skill
description: retained v1.1 tool policy
allowed-tools: Bash, Read, Write, AskUserQuestion
---
# Retained skill
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load errors=%#v", errors)
	}
	loaded := catalog.Skills()
	want := []string{"Bash", "Read", "Write", "AskUserQuestion"}
	if len(loaded) != 1 || !slices.Equal(loaded[0].Tools, want) {
		t.Fatalf("loaded skills=%#v want tools=%#v", loaded, want)
	}
}

func TestLoadSkillCatalogPreservesEnvironmentAndExecutionAssetMetadata(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "managed-workflow")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: managed-workflow
description: use one managed implementation
required-environment-packages:
  - example-runtime
  - example-parser>=2
preferred-execution-assets:
  - scripts/run.py
  - templates/report.md
---
# Managed workflow
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load errors=%#v", errors)
	}
	loaded := catalog.Skills()
	if len(loaded) != 1 {
		t.Fatalf("loaded skills=%#v", loaded)
	}
	wantPackages := []string{"example-runtime", "example-parser>=2"}
	if !slices.Equal(loaded[0].RequiredEnvironmentPackages, wantPackages) {
		t.Fatalf("loaded skills=%#v want environment packages=%#v", loaded, wantPackages)
	}
	wantAssets := []string{"scripts/run.py", "templates/report.md"}
	if !slices.Equal(loaded[0].PreferredExecutionAssets, wantAssets) {
		t.Fatalf("loaded skills=%#v want execution assets=%#v", loaded, wantAssets)
	}
}

func TestLoadSkillCatalogPreservesClosedRequiredCapabilities(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "scientific-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: scientific-skill
description: requires verified scientific execution
required-capabilities:
  - molecular-docking
  - binding-affinity
---
# Scientific skill
`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := Load([]string{root})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load errors=%#v", errors)
	}
	loaded := catalog.Skills()
	want := []string{"molecular-docking", "binding-affinity"}
	if len(loaded) != 1 || !slices.Equal(loaded[0].RequiredCapabilities, want) {
		t.Fatalf("loaded skills=%#v want capabilities=%#v", loaded, want)
	}
}

func TestLoadSkillCatalogRejectsInvalidOrDuplicateRequiredCapabilities(t *testing.T) {
	for name, capabilities := range map[string]string{
		"invalid":       "  - INVALID_VALUE\n",
		"empty":         "  []\n",
		"scalar":        "  molecular-docking\n",
		"mapping":       "  capability: molecular-docking\n",
		"double-hyphen": "  - molecular--docking\n",
		"duplicate":     "  - molecular-docking\n  - molecular-docking\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			skillDir := filepath.Join(root, "scientific-skill")
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := "---\nname: scientific-skill\ndescription: strict capability contract\nrequired-capabilities:\n" + capabilities + "---\n# Scientific skill\n"
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := Load([]string{root})
			if len(catalog.LoadErrors()) != 1 || len(catalog.Skills()) != 0 {
				t.Fatalf("errors=%#v skills=%#v", catalog.LoadErrors(), catalog.Skills())
			}
		})
	}
}

func TestLoadSkillCatalogRejectsMisspelledOrDuplicatedCapabilityContractField(t *testing.T) {
	for name, frontmatter := range map[string]string{
		"misspelled": "required_capabilities: [molecular-docking]\n",
		"duplicated": "required-capabilities: [molecular-docking]\nrequired-capabilities: [binding-affinity]\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			skillDir := filepath.Join(root, "scientific-skill")
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := "---\nname: scientific-skill\ndescription: strict capability contract\n" + frontmatter + "---\n# Scientific skill\n"
			if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			catalog := Load([]string{root})
			if len(catalog.LoadErrors()) != 1 || len(catalog.Skills()) != 0 {
				t.Fatalf("errors=%#v skills=%#v", catalog.LoadErrors(), catalog.Skills())
			}
		})
	}
}

func TestRetainedV11SkillAllowedToolsAreLoaded(t *testing.T) {
	skillDir := filepath.Join("..", "..", "skills", "synonbiomed", "msa-search-nim")
	catalog := Load([]string{skillDir})
	if errors := catalog.LoadErrors(); len(errors) != 0 {
		t.Fatalf("load retained skill errors=%#v", errors)
	}
	loaded := catalog.Skills()
	want := []string{"bash", "read_file", "edit_file", "ask_user"}
	if len(loaded) != 1 || loaded[0].Name != "msa-search-nim" || !slices.Equal(loaded[0].Tools, want) {
		t.Fatalf("retained skill=%#v want tools=%#v", loaded, want)
	}
}

func TestProviderKernelSkillsDeclareTheirUnavailableRuntimeDependency(t *testing.T) {
	for name, wantTools := range map[string][]string{
		"compute-env-setup":       {"compute_provider"},
		"managed-model-endpoints": {"compute_provider", "compute_provider_inference"},
		"remote-compute-modal":    {"repl", "compute_provider", "compute_provider_modal", "wait_for_notification", "save_artifacts"},
		"using-model-endpoint":    {"compute_provider", "compute_provider_inference"},
	} {
		t.Run(name, func(t *testing.T) {
			skillDir := filepath.Join("..", "..", "skills", "synonbiomed", name)
			catalog := Load([]string{skillDir})
			if errors := catalog.LoadErrors(); len(errors) != 0 {
				t.Fatalf("load %s errors=%#v", name, errors)
			}
			loaded := catalog.Skills()
			if len(loaded) != 1 || !slices.Equal(loaded[0].Tools, wantTools) {
				t.Fatalf("provider-kernel skill %s=%#v", name, loaded)
			}
		})
	}
}

func TestLoadReferencedFallbackToSkillFileDirectoryAndRoot(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "project", "skills")
	shared := filepath.Join(root, "shared.md")
	if err := os.WriteFile(shared, []byte("fallback content"), 0o600); err != nil {
		t.Fatalf("write shared fallback file: %v", err)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(`---
name: fallback-skill
description: fallback reference test
references:
  - ../shared.md
---
Need root based fallback resolution.
`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog := Load([]string{filepath.Join(root, "missing"), skillDir}, LoadOptions{MaxBodyBytes: 12000})
	skills := catalog.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d, load errors: %#v", len(skills), catalog.LoadErrors())
	}

	context := catalog.BuildSkillContext(skills, 12000, root)
	if !strings.Contains(context, "fallback content") {
		t.Fatalf("expected fallback content in context, got: %s", context)
	}
}

func TestLoadReferencedFilesRejectsEscapesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "project", "skills", "escape")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsideSecret, []byte("outside secret must not load"), 0o600); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	rootReference := filepath.Join(root, "allowed.md")
	if err := os.WriteFile(rootReference, []byte("root fallback allowed"), 0o600); err != nil {
		t.Fatalf("write root reference: %v", err)
	}
	if err := os.Symlink(outsideSecret, filepath.Join(skillDir, "linked-secret.md")); err != nil {
		t.Skipf("symlink unavailable on this filesystem: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(`---
name: escape-skill
description: reference escape test
references:
  - allowed.md
  - ../../../../outside-root.md
  - linked-secret.md
  - `+outsideSecret+`
---
Only bounded references should be loaded.
`), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog := Load([]string{skillDir}, LoadOptions{MaxBodyBytes: 12000})
	skills := catalog.Skills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d, load errors: %#v", len(skills), catalog.LoadErrors())
	}
	context := catalog.BuildSkillContext(skills, 12000, root)
	if !strings.Contains(context, "root fallback allowed") {
		t.Fatalf("bounded root fallback reference missing: %s", context)
	}
	if strings.Contains(context, "outside secret must not load") || strings.Contains(context, "## "+outsideSecret) {
		t.Fatalf("unbounded reference leaked into skill context: %s", context)
	}
	if strings.Contains(context, "## "+filepath.Join(skillDir, "linked-secret.md")) {
		t.Fatalf("symlink escape reference leaked into skill context: %s", context)
	}
}

func TestSkillSearchSelectAndMaxResults(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills")
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "SKILL.md"), []byte(`---
name: alpha
description: alpha helper
tags: [query]
keywords: [first]
---
alpha body text
`), 0o600); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "SKILL.md"), []byte(`---
name: beta
description: beta helper
tags: [query]
keywords: [second]
---
beta body text
`), 0o600); err != nil {
		t.Fatalf("write beta: %v", err)
	}

	catalog := Load([]string{dir}, LoadOptions{MaxBodyBytes: 12000})
	all := catalog.Search("query", 1)
	if len(all) != 1 {
		t.Fatalf("expected one result for maxResults=1, got %d", len(all))
	}
	selected := catalog.Search("select:alpha,beta", 3)
	if len(selected) != 2 {
		t.Fatalf("select did not return both skills: %#v", selected)
	}
	if selected[0].Name != "alpha" || selected[1].Name != "beta" {
		t.Fatalf("select order changed: %#v", selected)
	}
}

func TestSkillSearchCapsGenericBodyMatchesBelowDomainMetadata(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "stability-shelf-life",
		Description: "Drug product stability and shelf-life evaluation.",
		Body:        "Use a justified statistical approach.",
	})
	catalog.AddSkill(Skill{
		Name:        "generic-document-reader",
		Description: "Read documents and data.",
		Body: strings.Repeat(
			"stability data analysis shelf life product statistical evaluation ", 100,
		),
	})

	ranked := catalog.SearchRanked("drug product stability shelf-life analysis", 2)
	if len(ranked) != 2 {
		t.Fatalf("ranked matches=%#v", ranked)
	}
	if ranked[0].Skill.Name != "stability-shelf-life" {
		t.Fatalf("generic long body outranked domain metadata: %#v", ranked)
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Fatalf("domain match score=%d generic score=%d", ranked[0].Score, ranked[1].Score)
	}
}

func TestSkillActivationSearchCannotActivateFromInstructionBodyAlone(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "target-assessment",
		Description: "Evidence-backed target assessment and professional research reports.",
		Keywords:    []string{"target assessment", "research report", "靶点评估", "调研报告"},
	})
	catalog.AddSkill(Skill{
		Name:        "unrelated-workflow",
		Description: "Develop a dosage form.",
		Body: strings.Repeat(
			"target assessment research report evidence 调研 报告 研发价值 ", 100,
		),
	})

	ranked := catalog.SearchActivationRanked("完成靶点评估并形成调研报告", 2)
	if len(ranked) != 1 || ranked[0].Skill.Name != "target-assessment" {
		t.Fatalf("instruction body became proactive routing authority: %#v", ranked)
	}
}

func TestSkillActivationSearchUsesLatinTokenBoundaries(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{Name: "search-workflow", Keywords: []string{"search"}})
	if ranked := catalog.SearchActivationRanked("research and synthesis", 2); len(ranked) != 0 {
		t.Fatalf("partial Latin token activated a Skill: %#v", ranked)
	}
	if ranked := catalog.SearchActivationRanked("perform a search and synthesis", 2); len(ranked) != 1 || ranked[0].Skill.Name != "search-workflow" {
		t.Fatalf("complete Latin token did not activate its Skill: %#v", ranked)
	}
}

func TestSkillActivationSearchUsesDescriptionWithoutInstructionBody(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "clinical-biostatistics",
		Description: "Design clinical trial estimands, sample sizes, interim analyses, and statistical analysis plans.",
	})
	catalog.AddSkill(Skill{
		Name:        "regulatory-submission",
		Description: "Plan regulatory authority interactions and submission dossiers.",
		Body:        strings.Repeat("clinical trial sample sizes statistical analysis plans ", 100),
	})

	ranked := catalog.SearchActivationRanked(
		"Design the clinical trial estimand, sample size, and statistical analysis plan.", 2,
	)
	if len(ranked) == 0 || ranked[0].Skill.Name != "clinical-biostatistics" {
		t.Fatalf("standard Skill description did not provide routing metadata: %#v", ranked)
	}
	if len(ranked) > 1 && ranked[1].Skill.Name == "regulatory-submission" {
		t.Fatalf("instruction body became proactive routing authority: %#v", ranked)
	}
}

func TestSkillSearchPrefersCompleteIntentPhrasesOverGenericAliases(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "drug-discovery-pipeline",
		Description: "Small-molecule drug discovery and compound evidence workflow.",
		Keywords:    []string{"molecule", "drug", "compound"},
		Body:        strings.Repeat("clinical molecule drug analysis ", 40),
	})
	catalog.AddSkill(Skill{
		Name:        "clinical-development-plan",
		Description: "Clinical pharmacology and development planning.",
		Keywords:    []string{"首次人体", "起始剂量", "剂量递增"},
	})

	ranked := catalog.SearchRanked("为口服小分子药物制定首次人体起始剂量和剂量递增方案", 2)
	if len(ranked) != 2 || ranked[0].Skill.Name != "clinical-development-plan" {
		t.Fatalf("specific metadata phrases did not outrank generic aliases: %#v", ranked)
	}
}

func TestSkillSearchMatchesMixedLanguageMetadataAcrossWhitespace(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "protein-design",
		Description: "Generic inverse-folding workflow.",
		Keywords:    []string{"反向折叠"},
	})
	catalog.AddSkill(Skill{
		Name:        "rna-design",
		Description: "RNA inverse-folding workflow.",
		Keywords:    []string{"RNA反向折叠", "三维RNA骨架"},
	})

	ranked := catalog.SearchRanked("根据三维 RNA 骨架完成 RNA 反向折叠", 2)
	if len(ranked) != 2 || ranked[0].Skill.Name != "rna-design" {
		t.Fatalf("mixed-language metadata did not preserve the RNA domain: %#v", ranked)
	}
}

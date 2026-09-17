package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillCatalogIncludesPersonalRootWithExplicitDirectories(t *testing.T) {
	root := t.TempDir()
	bundledRoot := filepath.Join(root, "runtime", "skills", "synonbiomed")
	personalRoot := filepath.Join(root, "data", "skills", "personal-workflow")
	writeSkillDocumentForCatalogTest(t, bundledRoot, "bundled-workflow", "Bundled workflow")
	writeSkillDocumentForCatalogTest(t, personalRoot, "personal-workflow", "Personal workflow")

	catalog, directories, loadErrors := loadSkillCatalog(nil, []string{bundledRoot}, filepath.Join(root, "data"))
	if len(loadErrors) != 0 {
		t.Fatalf("load errors = %#v", loadErrors)
	}
	if len(directories) != 2 || !sameCleanPath(directories[0], bundledRoot) || !sameCleanPath(directories[1], filepath.Join(root, "data", "skills")) {
		t.Fatalf("loaded directories = %#v", directories)
	}

	var bundled, personal bool
	for _, skill := range catalog.Skills() {
		switch skill.Name {
		case "bundled-workflow":
			bundled = compatibilitySkillSource(skill) == "synon_llm"
		case "personal-workflow":
			personal = compatibilitySkillSource(skill) == "personal"
		}
	}
	if !bundled || !personal {
		t.Fatalf("catalog source classification bundled=%v personal=%v skills=%#v", bundled, personal, catalog.Skills())
	}
}

func TestLoadSkillCatalogDoesNotReportMissingPersonalRoot(t *testing.T) {
	root := t.TempDir()
	bundledRoot := filepath.Join(root, "runtime", "skills", "synonbiomed")
	writeSkillDocumentForCatalogTest(t, bundledRoot, "bundled-workflow", "Bundled workflow")

	_, directories, loadErrors := loadSkillCatalog(nil, []string{bundledRoot}, filepath.Join(root, "missing-data"))
	if len(loadErrors) != 0 {
		t.Fatalf("load errors = %#v", loadErrors)
	}
	if len(directories) != 1 || !sameCleanPath(directories[0], bundledRoot) {
		t.Fatalf("loaded directories = %#v", directories)
	}
}

func TestLoadSkillCatalogAlwaysIncludesBuiltinRuntimeWithConfiguredDirectories(t *testing.T) {
	root := t.TempDir()
	bundledRoot := filepath.Join(root, "runtime", "skills", "synonbiomed")
	writeSkillDocumentForCatalogTest(t, bundledRoot, "bundled-workflow", "Bundled workflow")

	catalog, _, loadErrors := loadSkillCatalog(nil, []string{bundledRoot}, filepath.Join(root, "data"))
	if len(loadErrors) != 0 {
		t.Fatalf("load errors = %#v", loadErrors)
	}
	var runtimeCount int
	for _, skill := range catalog.Skills() {
		if skill.Name == "synon-runtime" {
			runtimeCount++
			if skill.Path != "builtin:synon-runtime" {
				t.Fatalf("runtime skill path = %q", skill.Path)
			}
		}
	}
	if runtimeCount != 1 {
		t.Fatalf("runtime skill count = %d, skills=%#v", runtimeCount, catalog.Skills())
	}
}

func writeSkillDocumentForCatalogTest(t *testing.T, root, name, description string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: " + name + "\ndescription: " + description + "\n---\nUse this skill.\n"
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

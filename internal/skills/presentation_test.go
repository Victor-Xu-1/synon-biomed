package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPresentation = `{"schema_version":1,"skills":{"demo":{"category":"research-workflows","description_i18n":{"zh-CN":"可追溯的证据研究。"}}}}`

func writePresentationFixture(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Evidence research\n---\nPreserve primary evidence."), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePresentation(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, presentationFilename), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPresentationLoadsFromBundledRootAndParentRoot(t *testing.T) {
	parent := t.TempDir()
	bundled := filepath.Join(parent, "synonbiomed")
	writePresentationFixture(t, bundled, "demo")
	writePresentation(t, bundled, testPresentation)
	for _, root := range []string{bundled, parent} {
		catalog := Load([]string{root})
		if errs := catalog.LoadErrors(); len(errs) != 0 {
			t.Fatalf("root=%s errors=%#v", root, errs)
		}
		items := catalog.Skills()
		if len(items) != 1 || items[0].Category != "research-workflows" || items[0].DescriptionI18n["zh-CN"] != "可追溯的证据研究。" {
			t.Fatalf("root=%s skills=%#v", root, items)
		}
		if items[0].Description != "Evidence research" || items[0].Body != "Preserve primary evidence." {
			t.Fatal("presentation changed the executable Skill contract")
		}
	}
}

func TestPresentationNeverBorrowsMetadataAcrossRootsOrNestedCatalogs(t *testing.T) {
	parent := t.TempDir()
	writePresentation(t, parent, testPresentation)
	nested := filepath.Join(parent, "custom")
	writePresentationFixture(t, nested, "demo")
	// A configured root cannot read metadata outside its trust boundary.
	items := Load([]string{nested}).Skills()
	if len(items) != 1 || items[0].Category != "" || len(items[0].DescriptionI18n) != 0 {
		t.Fatalf("external skill borrowed presentation: %#v", items)
	}
	writePresentation(t, nested, strings.ReplaceAll(testPresentation, "research-workflows", "compute-platform"))
	catalog := Load([]string{parent})
	items = catalog.Skills()
	if len(items) != 1 || items[0].Category != "compute-platform" {
		t.Fatalf("nearest catalog must own presentation: %#v", items)
	}
}

func TestPresentationReportsMissingAndStaleEntriesWithoutRemovingSkills(t *testing.T) {
	root := t.TempDir()
	writePresentationFixture(t, root, "current")
	writePresentation(t, root, testPresentation)
	catalog := Load([]string{root})
	if len(catalog.Skills()) != 1 || len(catalog.LoadErrors()) != 2 {
		t.Fatalf("skills=%#v errors=%#v", catalog.Skills(), catalog.LoadErrors())
	}
}

func TestPresentationRejectsMalformedCatalogButPreservesSkills(t *testing.T) {
	for _, content := range []string{
		testPresentation + `{}`,
		strings.Replace(testPresentation, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(testPresentation, `"zh-CN":"可追溯的证据研究。"`, `"zh-CN":"first","zh-CN":"second"`, 1),
		strings.Replace(testPresentation, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Repeat(" ", maxPresentationBytes+1),
	} {
		root := t.TempDir()
		writePresentationFixture(t, root, "demo")
		writePresentation(t, root, content)
		catalog := Load([]string{root})
		if len(catalog.Skills()) != 1 || len(catalog.LoadErrors()) != 1 || catalog.Skills()[0].Category != "" {
			t.Fatalf("invalid catalog should be diagnostic only: skills=%#v errors=%#v", catalog.Skills(), catalog.LoadErrors())
		}
	}
}

func TestPresentationRejectsSymlinkCatalog(t *testing.T) {
	outside := t.TempDir()
	writePresentation(t, outside, testPresentation)
	root := t.TempDir()
	writePresentationFixture(t, root, "demo")
	if err := os.Symlink(filepath.Join(outside, presentationFilename), filepath.Join(root, presentationFilename)); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	catalog := Load([]string{root})
	if len(catalog.Skills()) != 1 || len(catalog.LoadErrors()) != 1 || catalog.Skills()[0].Category != "" {
		t.Fatalf("symbolic link presentation accepted: skills=%#v errors=%#v", catalog.Skills(), catalog.LoadErrors())
	}
}

func TestBundledPresentationCoversEveryShippedSkill(t *testing.T) {
	for _, root := range []string{filepath.Join("..", "..", "skills", "synonbiomed"), filepath.Join("..", "..", "skills")} {
		catalog := Load([]string{root})
		if errs := catalog.LoadErrors(); len(errs) != 0 {
			t.Fatalf("bundled catalog errors: %#v", errs)
		}
		if len(catalog.Skills()) == 0 {
			t.Fatal("bundled catalog is empty")
		}
		for _, skill := range catalog.Skills() {
			if skill.Category == "" || skill.DescriptionI18n["zh-CN"] == "" {
				t.Errorf("Skill %s lacks reviewed presentation", skill.Name)
			}
		}
	}
}

package assets

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestProductKnowledgeReferencesResolveInCurrentSource(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate source root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "skills/synonbiomed/product-self-knowledge/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, references, found := strings.Cut(string(raw), "## Local Reference Points")
	if !found {
		t.Fatal("product knowledge has no source reference section")
	}
	paths := regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(references, -1)
	if len(paths) < 3 {
		t.Fatal("product knowledge lacks concrete source references")
	}
	for _, match := range paths {
		relative := filepath.FromSlash(match[1])
		if !filepath.IsLocal(relative) {
			t.Errorf("non-local product reference: %s", match[1])
			continue
		}
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			t.Errorf("product reference does not resolve: %s: %v", match[1], err)
		}
	}
}

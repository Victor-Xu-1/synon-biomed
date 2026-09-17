package assets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillsDoNotAdvertiseRetiredModelAuthoredSoftwareRuntime(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	matches, err := filepath.Glob(filepath.Join(root, "skills", "synonbiomed", "*", "SKILL.md"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("discover Skills: count=%d err=%v", len(matches), err)
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, "software_runtime") {
			continue
		}
		for _, retired := range []string{
			`"packages":`, `"provider": "local-conda"`, `"executable":`,
			`"expected_outputs":`, `"scientific_evidence":`, "bounded self-repair",
		} {
			if strings.Contains(text, retired) {
				t.Errorf("%s retains retired software-runtime authoring marker %q", filepath.ToSlash(path), retired)
			}
		}
		if !strings.Contains(text, "execution_pack_id") && !strings.Contains(text, "unavailable") &&
			!strings.Contains(text, "accepts only locally available pack IDs") &&
			!strings.Contains(text, "only with that pack ID") &&
			!strings.Contains(text, "Never teach a Skill") {
			t.Errorf("%s mentions software_runtime without a pack or explicit unavailable boundary", filepath.ToSlash(path))
		}
	}
}

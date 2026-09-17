package v11reuse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOutputCreatesAndChecksExactManifest(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "manifest.json")
	data := []byte("{\"SchemaVersion\":1}\n")

	if err := WriteOutput(target, data, false); err != nil {
		t.Fatalf("WriteOutput() error = %v", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(actual) != string(data) {
		t.Fatalf("written data = %q, want %q", actual, data)
	}
	if err := WriteOutput(target, data, true); err != nil {
		t.Fatalf("WriteOutput(check) error = %v", err)
	}

	if err := WriteOutput(target, []byte("{\"SchemaVersion\":2}\n"), true); !errors.Is(err, ErrManifestDrift) {
		t.Fatalf("WriteOutput(drift) error = %v, want ErrManifestDrift", err)
	}
	if err := WriteOutput(filepath.Join(t.TempDir(), "missing.json"), data, true); !errors.Is(err, ErrManifestDrift) {
		t.Fatalf("WriteOutput(missing check) error = %v, want ErrManifestDrift", err)
	}
}

func TestWriteOutputRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual.json")
	writeFile(t, actual, "do not replace\n")
	link := filepath.Join(root, "manifest.json")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := WriteOutput(link, []byte("{}\n"), false)
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("WriteOutput() error = %v, want ErrPathEscape", err)
	}
	content, readErr := os.ReadFile(actual)
	if readErr != nil {
		t.Fatalf("ReadFile(actual) error = %v", readErr)
	}
	if string(content) != "do not replace\n" {
		t.Fatalf("symlink destination changed to %q", content)
	}
}

func TestReviewMarkdownIsDeterministicAndIncludesDisposition(t *testing.T) {
	manifest, err := Build(newFixture(t), fixtureRules())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	first := ReviewMarkdown(manifest)
	second := ReviewMarkdown(manifest)
	if string(first) != string(second) {
		t.Fatal("ReviewMarkdown() is not deterministic")
	}
	for _, expected := range []string{
		"# SynonBiomed v1.1 Reuse Review",
		"agent-operon",
		"direct_copy",
		"runtime_state_excluded",
		manifest.SourceTreeSHA256,
	} {
		if !strings.Contains(string(first), expected) {
			t.Fatalf("review does not contain %q:\n%s", expected, first)
		}
	}
}

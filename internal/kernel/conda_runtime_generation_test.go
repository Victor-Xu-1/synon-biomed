package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareManagedRuntimeGenerationReusesVerifiedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	verified := false
	ready, quarantined, err := prepareManagedRuntimeGeneration(path, func() error {
		verified = true
		return nil
	})
	if err != nil || !ready || quarantined != "" || !verified {
		t.Fatalf("ready=%t quarantined=%q verified=%t err=%v", ready, quarantined, verified, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("verified generation was moved: %v", err)
	}
}

func TestPrepareManagedRuntimeGenerationQuarantinesCorruptDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "generation")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ready, quarantined, err := prepareManagedRuntimeGeneration(path, func() error {
		return errors.New("marker mismatch")
	})
	if err != nil || ready || quarantined == "" {
		t.Fatalf("ready=%t quarantined=%q err=%v", ready, quarantined, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt generation still exists: %v", err)
	}
	if info, err := os.Stat(quarantined); err != nil || !info.IsDir() || !strings.Contains(filepath.Base(quarantined), ".staging-invalid-") {
		t.Fatalf("quarantined generation=%q info=%v err=%v", quarantined, info, err)
	}
	if err := os.RemoveAll(quarantined); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSmokeOutputHelpersIgnoreBoundedNoise(t *testing.T) {
	lines := reverseNonEmptyOutputLines("warning from native library\nSYNON_R_VERSION=4.5.3\n")
	if len(lines) != 2 || lines[0] != "SYNON_R_VERSION=4.5.3" {
		t.Fatalf("reversed lines=%#v", lines)
	}
	if got := boundedProcessDiagnostics("{\"ok\":true}", "warning"); got != "warning | stdout: {\"ok\":true}" {
		t.Fatalf("diagnostics=%q", got)
	}
}

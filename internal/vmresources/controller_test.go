package vmresources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControllerPreservesWSLConfigAndValidatesLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wslconfig")
	original := "# user settings\r\n[wsl2]\r\nmemory=4GB\r\nprocessors=2\r\nlocalhostForwarding=true\r\n\r\n[experimental]\r\nautoMemoryReclaim=gradual\r\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := New(path, Limits{
		MaxMemoryGB: 16, HostCPUCount: 8, LaunchedMemoryGB: 4, LaunchedCPUCount: 2,
	})
	resources, err := controller.Get()
	if err != nil {
		t.Fatal(err)
	}
	if resources.MemoryGB != 4 || resources.CPUCount != 2 || resources.MaxMemoryGB != 16 ||
		resources.HostCPUCount != 8 || resources.LaunchedMemoryGB != 4 || resources.LaunchedCPUCount != 2 ||
		resources.IsRestarting {
		t.Fatalf("resources = %#v", resources)
	}

	updated, err := controller.Set(6, 4)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MemoryGB != 6 || updated.CPUCount != 4 || updated.LaunchedMemoryGB != 4 || updated.LaunchedCPUCount != 2 {
		t.Fatalf("updated resources = %#v", updated)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, retained := range []string{"# user settings\r\n", "localhostForwarding=true\r\n", "[experimental]\r\n", "autoMemoryReclaim=gradual\r\n"} {
		if !strings.Contains(text, retained) {
			t.Fatalf("updated config lost %q: %q", retained, text)
		}
	}
	if !strings.Contains(text, "memory=6GB\r\n") || !strings.Contains(text, "processors=4\r\n") {
		t.Fatalf("updated config values = %q", text)
	}
	if _, err := controller.Set(17, 4); err == nil {
		t.Fatal("memory above host limit accepted")
	}
	if _, err := controller.Set(6, 9); err == nil {
		t.Fatal("CPU count above host limit accepted")
	}
}

func TestControllerCreatesMissingWSL2Section(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wslconfig")
	controller := New(path, Limits{
		MaxMemoryGB: 8, HostCPUCount: 4, LaunchedMemoryGB: 3, LaunchedCPUCount: 2,
	})
	if _, err := controller.Set(5, 3); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "[wsl2]") || !strings.Contains(text, "memory=5GB") || !strings.Contains(text, "processors=3") {
		t.Fatalf("created config = %q", text)
	}
}

func TestDetectLimitsReportsRealRuntimeCapacity(t *testing.T) {
	limits := DetectLimits()
	if limits.MaxMemoryGB < 1 || limits.HostCPUCount < 1 || limits.LaunchedMemoryGB < 1 || limits.LaunchedCPUCount < 1 {
		t.Fatalf("detected limits = %#v", limits)
	}
}

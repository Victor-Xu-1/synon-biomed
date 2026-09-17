package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagedEnvironmentManagerHasOneCohesiveAuthority(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve managed environment architecture path")
	}
	directory := filepath.Dir(currentFile)
	required := map[string][]string{
		"managed_environment.go":             {"type ManagedEnvironment struct", "func (m *Manager) ListManagedEnvironments("},
		"managed_environment_mutation.go":    {"func (m *Manager) CreateManagedEnvironment(", "func (m *Manager) mutateManagedPackages("},
		"managed_environment_publication.go": {"func (m *Manager) publishManagedEnvironment("},
		"managed_environment_inventory.go":   {"func (m *Manager) readManagedEnvironment("},
		"managed_environment_process.go":     {"func (m *Manager) runManagedEnvironmentProcess("},
		"managed_environment_validation.go":  {"func validateManagedEnvironmentBinaryCompatibility("},
		"managed_environment_generation.go":  {"func managedEnvironmentGeneration("},
		"managed_environment_input.go":       {"func validateManagedEnvironmentName("},
	}
	all := ""
	for fileName, markers := range required {
		raw, err := os.ReadFile(filepath.Join(directory, fileName))
		if err != nil {
			t.Errorf("read %s: %v", fileName, err)
			continue
		}
		content := string(raw)
		all += "\n" + content
		for _, marker := range markers {
			if !strings.Contains(content, marker) {
				t.Errorf("%s is missing %q", fileName, marker)
			}
		}
	}
	for _, authority := range []string{
		"func (m *Manager) CreateManagedEnvironment(",
		"func (m *Manager) mutateManagedPackages(",
	} {
		if count := strings.Count(all, authority); count != 1 {
			t.Errorf("managed environment authority %q count = %d, want 1", authority, count)
		}
	}
}

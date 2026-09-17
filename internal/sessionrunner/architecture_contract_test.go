package sessionrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestCanonicalPhasesMatchHarnessArchitectureAuthority(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve session runner architecture path")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(repo, "docs", "governance", "harness-architecture.json"))
	if err != nil {
		t.Fatalf("read Harness architecture contract: %v", err)
	}
	var document struct {
		Architecture struct {
			Modules []struct {
				Name  string   `json:"name"`
				Order []string `json:"order"`
			} `json:"modules"`
		} `json:"architecture"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode Harness architecture contract: %v", err)
	}
	var authority []string
	for _, boundary := range document.Architecture.Modules {
		if boundary.Name == "session-runner" {
			authority = boundary.Order
			break
		}
	}
	actual := make([]string, len(CanonicalPhases))
	for index, phase := range CanonicalPhases {
		actual[index] = string(phase)
	}
	if !reflect.DeepEqual(actual, authority) {
		t.Fatalf("runner phases = %v, architecture authority = %v", actual, authority)
	}
}

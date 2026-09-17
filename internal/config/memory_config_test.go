package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/memoryconfig"
)

func TestLoadUsesWorkspaceMemoryDefaults(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Memory, memoryconfig.Default()) {
		t.Fatalf("Memory = %#v, want %#v", cfg.Memory, memoryconfig.Default())
	}
}

func TestLoadOverlaysWorkspaceMemoryConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "synon.json")
	if err := os.WriteFile(configPath, []byte(`{
  "memory": {
		"enabled": true,
		"pi_classifier_model": "must-be-stripped",
    "context_in_system_prompt": false,
    "profile_max_rows": -1,
    "recall_inject_max": 9,
    "unknown_source_field": "stripped"
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_HOME", filepath.Join(root, "state"))

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || cfg.Memory.ContextInSystemPrompt || cfg.Memory.ProfileMaxRows != -1 || cfg.Memory.RecallInjectMax != 9 {
		t.Fatalf("Memory = %#v", cfg.Memory)
	}
	if cfg.Memory.ExtractMode != memoryconfig.Default().ExtractMode {
		t.Fatalf("Memory.ExtractMode = %q, want default %q", cfg.Memory.ExtractMode, memoryconfig.Default().ExtractMode)
	}
}

func TestLoadRejectsInvalidWorkspaceMemoryConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "synon.json")
	if err := os.WriteFile(configPath, []byte(`{"memory":{"extract_mode":"inline"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_HOME", filepath.Join(root, "state"))

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "memory.extract_mode must be forked or haiku") {
		t.Fatalf("Load() error = %v", err)
	}
}

package kernel

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRuntimeContractFixture(t *testing.T, version int, legacy bool) Config {
	t.Helper()
	config := bundledManagedPythonConfig(t)
	loaded, err := loadManagedPythonRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifest := loaded.manifest
	manifest.SchemaVersion = version
	manifest.ManifestSHA256 = ""
	entry := loaded.entry
	if legacy {
		manifest.Name = "claude-science-python"
		manifest.Oracle = &condaRuntimeReference{Product: "previous-runtime", Version: "1"}
		entry.Name, entry.Oracle = manifest.Name, manifest.Oracle
	}
	manifest.ManifestSHA256, err = canonicalJSONDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entry.Generation = manifest.ManifestSHA256
	entry.ManifestPath, entry.ExplicitPath, entry.LicensesPath = "runtime.json", "explicit.txt", "licenses.json"
	catalog := condaRuntimeCatalog{
		SchemaVersion: version, Platform: currentCondaPlatform(),
		Runtimes: []condaRuntimeCatalogEntry{entry},
	}
	if legacy {
		catalog.Oracle = manifest.Oracle
	}
	catalog.CatalogSHA256, err = canonicalJSONDigest(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"catalog.json": catalog, "runtime.json": manifest} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	explicit, err := os.ReadFile(loaded.explicitPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "explicit.txt"), explicit, 0o600); err != nil {
		t.Fatal(err)
	}
	licenses, err := os.ReadFile(filepath.Join(filepath.Dir(loaded.explicitPath), "licenses.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "licenses.json"), licenses, 0o600); err != nil {
		t.Fatal(err)
	}
	config.CondaRuntimeCatalog = filepath.Join(root, "catalog.json")
	config.ManagedPythonEnvironment = entry.Name
	return config
}

func TestCondaRuntimeVersionOneAndTwoKeepVerifiedPackageContracts(t *testing.T) {
	for _, version := range []int{1, 2} {
		config := writeRuntimeContractFixture(t, version, version == 1)
		loaded, err := loadManagedPythonRuntime(config)
		if err != nil {
			t.Fatalf("schema %d: %v", version, err)
		}
		if loaded.manifest.SchemaVersion != version || loaded.pythonVersion != "3.11.15" {
			t.Fatal("versioned runtime changed its pinned package contract")
		}
		if version == 1 && loaded.entry.Name != "synon-biomed-python-baseline" {
			t.Fatal("legacy runtime did not resolve to the canonical activation identity")
		}
	}
}

func TestCondaRuntimeVersionTwoRejectsLegacyMetadataWithValidDigest(t *testing.T) {
	config := writeRuntimeContractFixture(t, 2, true)
	if _, err := loadManagedPythonRuntime(config); err == nil || !strings.Contains(err.Error(), "unsupported legacy metadata") {
		t.Fatalf("legacy metadata accepted in v2: %v", err)
	}
}

func TestBundledNativeRuntimeCatalogsVerifyPlatformLocks(t *testing.T) {
	root := repositoryRootForCondaRuntimeTest(t)
	for _, target := range []struct {
		goos, goarch string
	}{
		{goos: "linux", goarch: "amd64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
	} {
		platform, ok := managedRuntimePlatformFor(target.goos, target.goarch)
		if !ok {
			t.Fatalf("unsupported test platform %s/%s", target.goos, target.goarch)
		}
		catalog := filepath.Join(root, "assets", "optional", "conda-runtimes", platform.ID, "manifest.json")
		if platform.ID == "linux-x86_64" {
			catalog = filepath.Join(root, "assets", "optional", "conda-runtimes", "manifest.json")
		}
		config := Config{CondaRuntimeCatalog: catalog}
		for _, name := range []string{defaultManagedPythonEnvironment, defaultManagedREnvironment} {
			runtime, err := loadManagedCondaRuntimeForPlatform(config, name, platform)
			if err != nil {
				t.Fatalf("%s/%s %s: %v", target.goos, target.goarch, name, err)
			}
			if runtime.entry.Platform != platform.ID || runtime.entry.Generation != runtime.manifestDigest {
				t.Fatalf("%s/%s %s: inconsistent generation", target.goos, target.goarch, name)
			}
		}
	}
}

func TestCondaRuntimeRejectsUnknownFieldsAndAlteredDigest(t *testing.T) {
	for _, unknown := range []bool{true, false} {
		config := writeRuntimeContractFixture(t, 2, false)
		data, err := os.ReadFile(config.CondaRuntimeCatalog)
		if err != nil {
			t.Fatal(err)
		}
		if unknown {
			data = bytes.Replace(data, []byte("{"), []byte("{\"unrecognized\":true,"), 1)
		} else {
			data = bytes.Replace(data, []byte(`"packageCount": 181`), []byte(`"packageCount": 180`), 1)
		}
		if err := os.WriteFile(config.CondaRuntimeCatalog, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadManagedPythonRuntime(config); err == nil {
			t.Fatalf("invalid catalog accepted, unknown=%v", unknown)
		}
	}
}

func TestCondaRuntimeStoredIdentityAliasesUseOneResolver(t *testing.T) {
	for before, after := range map[string]string{
		"claude-science-python": "synon-biomed-python-baseline",
		"claude-science-r":      "synon-biomed-r",
		"synon-biomed-python":   "synon-biomed-python",
		"custom-runtime":        "custom-runtime",
	} {
		if got := managedPythonName(Config{ManagedPythonEnvironment: before}); got != after {
			t.Fatalf("identity %q became %q, want %q", before, got, after)
		}
	}
}

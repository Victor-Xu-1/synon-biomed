package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagedPackageWitnessesUseMetadataAndCheckPayload(t *testing.T) {
	prefix := t.TempDir()
	write := func(name string, value any) {
		t.Helper()
		p := filepath.Join(prefix, "conda-meta", name+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("data", map[string]any{"name": "bioconductor-datasetfixture", "requested_spec": "bioconductor-datasetfixture", "version": "1.0", "files": []string{"bin/.bioconductor-datasetfixture-post-link.sh"}})
	write("meta", map[string]any{"name": "r-meta", "requested_spec": "r-meta", "files": []string{}})
	names, err := managedRPackageWitnesses(context.Background(), prefix)
	if err != nil || !reflect.DeepEqual(names, []string{"datasetfixture"}) {
		t.Fatalf("witnesses=%v err=%v", names, err)
	}
	write("library", map[string]any{"name": "r-fixture", "requested_spec": "r-fixture", "files": []string{"lib/R/library/FixtureNamespace/DESCRIPTION", "lib/R/library/FixtureNamespace/Meta/package.rds"}})
	if _, err := managedRPackageWitnesses(context.Background(), prefix); err == nil {
		t.Fatal("registered missing payload accepted")
	}
	description := filepath.Join(prefix, "lib/R/library/FixtureNamespace/DESCRIPTION")
	if err := os.MkdirAll(filepath.Dir(description), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(description, []byte("Package: FixtureNamespace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := managedRPackageWitnesses(context.Background(), prefix); err == nil {
		t.Fatal("missing declared installed-package metadata accepted")
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(description), "Meta"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(description), "Meta/package.rds"), []byte("metadata fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	names, err = managedRPackageWitnesses(context.Background(), prefix)
	if err != nil || !reflect.DeepEqual(names, []string{"FixtureNamespace", "datasetfixture"}) {
		t.Fatalf("witnesses=%v err=%v", names, err)
	}
	write("unsafe", map[string]any{"name": "r-unsafe", "files": []string{"../outside"}})
	if _, err := managedRPackageWitnesses(context.Background(), prefix); err == nil {
		t.Fatal("path traversal accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := managedRPackageWitnesses(ctx, prefix); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestManagedRInventoryDistinguishesResourceTreesFromInstalledPackages(t *testing.T) {
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "conda-meta"), 0700); err != nil {
		t.Fatal(err)
	}
	files := []string{"lib/R/library/ResourceBundle/DESCRIPTION", "lib/R/library/RuntimeModule/DESCRIPTION", "lib/R/library/RuntimeModule/Meta/package.rds"}
	for _, name := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(prefix, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prefix, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"name": "r-runtime", "requested_spec": "r-runtime", "files": files})
	if err := os.WriteFile(filepath.Join(prefix, "conda-meta/runtime.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	names, err := managedRPackageWitnesses(context.Background(), prefix)
	if err != nil || !reflect.DeepEqual(names, []string{"RuntimeModule"}) {
		t.Fatalf("resource directories treated as loadable packages: %v, %v", names, err)
	}
}

func TestManagedLegacyHookGenerationCannotBeReusedOrRecovered(t *testing.T) {
	root := t.TempDir()
	name := "runtime-fixture"
	packages := []string{"python=3.11=0=conda-forge"}
	generation := managedEnvironmentGenerationAtValidation(name, "python", packages, "", 0)
	current := managedEnvironmentGeneration(name, "python", packages, "")
	if generation == current {
		t.Fatal("new verification reuses old content identity")
	}
	prefix := filepath.Join(root, ".generations", name, generation)
	writeManagedEnvironmentHealthPython(t, prefix, "1.0.0", "1.0.0", "")
	if err := os.WriteFile(filepath.Join(prefix, "bin/.fixture-post-link.sh"), []byte("exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := managedEnvironmentMarker{SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: "python", Kind: "conda", Generation: generation, Packages: packages, OperationKey: strings.Repeat("a", 64)}
	if err := writeManagedEnvironmentMarker(filepath.Join(prefix, managedEnvironmentMarkerName), marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(prefix, filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{CondaEnvsPath: root})
	if _, found, err := m.ManagedEnvironmentActiveGeneration(name); found || !errors.Is(err, ErrManagedEnvironmentRebuildRequired) {
		t.Fatalf("legacy admission found=%v err=%v", found, err)
	}
	for _, fast := range []bool{true, false} {
		list, err := m.ListManagedEnvironments(context.Background(), ManagedEnvironmentQuery{SkipHealth: fast})
		if err != nil || len(list) != 0 {
			t.Fatalf("legacy reused fast=%v list=%v err=%v", fast, list, err)
		}
	}
	if _, found, err := m.findManagedEnvironmentOperationGeneration(context.Background(), filepath.Dir(prefix), marker.OperationKey); err != nil || found {
		t.Fatalf("legacy recovery found=%v err=%v", found, err)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Fatal("old generation was not preserved")
	}
}

func TestManagedHealthCacheBindsCompleteValidationIdentity(t *testing.T) {
	m := NewManager(Config{})
	generation := strings.Repeat("a", 64)
	good, bad := t.TempDir(), t.TempDir()
	writeManagedEnvironmentHealthPython(t, good, "1.0.0", "1.0.0", "")
	if err := m.validateManagedEnvironmentGeneration(context.Background(), generation, "python", good, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.validateManagedEnvironmentGeneration(context.Background(), generation, "python", bad, nil, nil); err == nil {
		t.Fatal("different prefix inherited cached health")
	}
	if err := m.validateManagedEnvironmentGeneration(context.Background(), generation, "python", good, nil, []string{"absent"}); err == nil {
		t.Fatal("stronger witness inherited interpreter-only health")
	}
}

func TestManagedExecutableSearchPathKeepsUtilitiesWithoutRelativeEntries(t *testing.T) {
	preferred := filepath.Join(t.TempDir(), "bin")
	host := t.TempDir()
	t.Setenv("PATH", strings.Join([]string{".", "relative", host, host, ""}, string(os.PathListSeparator)))
	if got := managedExecutableSearchPath(preferred); got != preferred+string(os.PathListSeparator)+host {
		t.Fatalf("path=%q", got)
	}
}

func TestManagedReadinessInventoryRejectsExternalSymlinksAndOversize(t *testing.T) {
	for _, target := range []string{"metadata", "payload", "directory"} {
		t.Run(target, func(t *testing.T) {
			prefix, outside := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(prefix, "conda-meta"), 0700); err != nil {
				t.Fatal(err)
			}
			record := []byte(`{"name":"r-fixture","files":["lib/R/library/Fixture/DESCRIPTION"]}`)
			if err := os.WriteFile(filepath.Join(prefix, "conda-meta/fixture.json"), record, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "record"), record, 0600); err != nil {
				t.Fatal(err)
			}
			switch target {
			case "metadata":
				if err := os.Remove(filepath.Join(prefix, "conda-meta/fixture.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "record"), filepath.Join(prefix, "conda-meta/fixture.json")); err != nil {
					t.Fatal(err)
				}
			case "payload":
				if err := os.MkdirAll(filepath.Join(prefix, "lib/R/library/Fixture"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "record"), filepath.Join(prefix, "lib/R/library/Fixture/DESCRIPTION")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.MkdirAll(filepath.Join(prefix, "lib/R"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(prefix, "lib/R/library")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := managedRPackageWitnesses(context.Background(), prefix); err == nil {
				t.Fatal("external inventory accepted")
			}
		})
	}
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "conda-meta"), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(prefix, "conda-meta/oversize.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8*1024*1024 + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := managedRPackageWitnesses(context.Background(), prefix); err == nil {
		t.Fatal("oversize metadata accepted")
	}
}

func TestManagedLegacyReadinessChecksBothExecutableLayouts(t *testing.T) {
	for _, hook := range []string{"bin/.fixture-pre-link.sh", "Scripts/.fixture-post-link.bat"} {
		t.Run(hook, func(t *testing.T) {
			prefix := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(prefix, hook)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prefix, hook), []byte("exit 1"), 0700); err != nil {
				t.Fatal(err)
			}
			if needs, err := managedEnvironmentNeedsRebuild(prefix, managedEnvironmentMarker{}); err != nil || !needs {
				t.Fatalf("needs=%v err=%v", needs, err)
			}
		})
	}
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "conda-meta"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "conda-meta/fixture.json"), []byte(`{"files":["bin/.fixture-post-link.sh"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if needs, err := managedEnvironmentNeedsRebuild(prefix, managedEnvironmentMarker{}); err != nil || !needs {
		t.Fatalf("removed hook needs=%v err=%v", needs, err)
	}
}

func TestManagedImportWitnessRequiresOwnSentinelAndSuccessfulExit(t *testing.T) {
	for _, script := range []string{"#!/bin/sh\nprintf '{\"ok\":true}'\n", "#!/bin/sh\nprintf 'SYNON_IMPORT_WITNESS_OK\\n'\nexit 3\n"} {
		prefix := t.TempDir()
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prefix, "bin/python"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := validateManagedEnvironmentImports(context.Background(), "python", prefix, []string{"json"}); err == nil {
			t.Fatal("invalid witness accepted")
		}
	}
}

func TestManagedWitnessRejectsForeignGenerationPointer(t *testing.T) {
	root, foreign := t.TempDir(), t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(root, "fixture")); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{CondaEnvsPath: root})
	if err := manager.VerifyManagedEnvironmentImports(context.Background(), "fixture", []string{"json"}); err == nil || !strings.Contains(err.Error(), "trusted generation root") {
		t.Fatalf("foreign witness=%v", err)
	}
}

func TestManagedInstallerInventoryRequiresPinnedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		valid        bool
	}{
		{"current", `{"log_history":[],"packages":[{"name":"fixture","version":"1","build_string":"0","channel":"test"}]}`, true},
		{"obsolete", `[{"name":"fixture","version":"1","build_string":"0","channel":"test"}]`, false},
		{"empty", `{"packages":[]}`, false},
		{"trailing", `{"packages":[{"name":"fixture","version":"1"}]}{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			executable := filepath.Join(root, "installer")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' '"+tc.result+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(Config{Micromamba: executable, CondaHome: root})
			packages, err := manager.inspectManagedEnvironmentPackages(context.Background(), root)
			if (err == nil) != tc.valid || tc.valid && len(packages) != 1 {
				t.Fatalf("packages=%v err=%v", packages, err)
			}
		})
	}
}

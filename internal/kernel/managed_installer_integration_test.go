package kernel

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Local package fixtures exercise the bundled installer protocol, including
// package-owned link scripts, without network or scientific task data.
func TestManagedInstallerRealLinkScriptLifecycle(t *testing.T) {
	if os.Getenv("SYNON_TEST_REAL_INSTALLER") != "1" || runtime.GOOS != "linux" {
		t.Skip("opt-in Linux installer integration")
	}
	_, file, _, _ := runtime.Caller(0)
	installer := filepath.Join(filepath.Dir(file), "..", "..", "assets", "optional", "micromamba", "linux-x86_64", "micromamba")
	if configured := os.Getenv("SYNON_TEST_MICROMAMBA"); configured != "" {
		installer = configured
	}
	for _, tc := range []struct {
		name, script string
		fails        bool
		cancel       bool
	}{
		{"success", "#!/bin/sh\nprintf 'complete' > \"$PREFIX/closure-receipt\"\n", false, false},
		{"failure", "#!/bin/sh\nprintf '{\"ok\":true}'\nexit 19\n", true, false},
		{"cancel", "#!/bin/sh\nsleep 30\nprintf 'complete' > \"$PREFIX/closure-receipt\"\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(Config{Micromamba: installer, CondaHome: filepath.Join(root, "cache"), CondaEnvsPath: filepath.Join(root, "envs")})
			archive := managedInstallerFixturePackage(t, root, tc.script)
			prefix := filepath.Join(root, "installed")
			timeout := 30 * time.Second
			if tc.cancel {
				timeout = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			started := time.Now()
			err := manager.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "--offline", "-p", prefix, archive)
			if tc.fails {
				if err == nil {
					t.Fatal("installer accepted a failed package link script")
				}
				if !tc.cancel && (!strings.Contains(err.Error(), "post-link") || !strings.Contains(err.Error(), "19")) {
					t.Fatalf("failure was not the declared hook exit: %v", err)
				}
				if tc.cancel {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
						t.Fatalf("installer did not settle cancellation promptly: %v", err)
					}
					if _, err := os.Stat(filepath.Join(prefix, "closure-receipt")); !os.IsNotExist(err) {
						t.Fatalf("cancelled hook completed: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(prefix, "closure-receipt"))
			if err != nil || string(data) != "complete" {
				t.Fatalf("package registered without required link effects: %q, %v", data, err)
			}
		})
	}
}

func managedInstallerFixturePackage(t *testing.T, root, script string) string {
	t.Helper()
	index, err := json.Marshal(map[string]any{"name": "closure-probe", "version": "1.0", "build": "0", "build_number": 0, "subdir": "linux-64", "depends": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, entry := range []struct{ name, body string }{
		{"info/index.json", string(index)},
		{"info/files", "bin/.closure-probe-post-link.sh\n"},
		{"bin/.closure-probe-post-link.sh", script},
	} {
		if err := w.WriteHeader(&tar.Header{Name: entry.name, Mode: 0700, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bzip2", "-c")
	command.Stdin = &data
	compressed, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "closure-probe-1.0-0.tar.bz2")
	if err := os.WriteFile(path, compressed, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagedRImportWitnessUsesRealRuntime(t *testing.T) {
	prefix := os.Getenv("SYNON_TEST_R_PREFIX")
	if prefix == "" {
		t.Skip("SYNON_TEST_R_PREFIX is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := validateManagedEnvironmentImports(ctx, "r", prefix, []string{"stats"}); err != nil {
		t.Fatalf("real base package rejected: %v", err)
	}
	if err := validateManagedEnvironmentImports(ctx, "r", prefix, []string{"NonexistentRuntimeWitnessFixture"}); err == nil {
		t.Fatal("absent R package accepted")
	}
}

func TestManagedInstallerRealPublicationAndRestartRecovery(t *testing.T) {
	if os.Getenv("SYNON_TEST_REAL_INSTALLER") != "1" || runtime.GOOS != "linux" {
		t.Skip("opt-in Linux installer integration")
	}
	_, file, _, _ := runtime.Caller(0)
	installer := filepath.Join(filepath.Dir(file), "..", "..", "assets/optional/micromamba/linux-x86_64/micromamba")
	if configured := os.Getenv("SYNON_TEST_MICROMAMBA"); configured != "" {
		installer = configured
	}
	root := t.TempDir()
	config := Config{Micromamba: installer, CondaHome: filepath.Join(root, "cache"), CondaEnvsPath: filepath.Join(root, "envs")}
	manager := NewManager(config)
	// The controlled hook creates a real isolated interpreter, not a fake smoke
	// response. No external package download or scientific workload is involved.
	archive := managedInstallerFixturePackage(t, root, "#!/bin/sh\nset -e\npython3 -m venv --without-pip \"$PREFIX\"\nprintf complete > \"$PREFIX/closure-receipt\"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	installCount := 0
	install := func(prefix string) error {
		installCount++
		return manager.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "--offline", "-p", prefix, archive)
	}
	seed := filepath.Join(root, "seed")
	if err := install(seed); err != nil {
		t.Fatal(err)
	}
	packages, err := manager.inspectManagedEnvironmentPackages(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	name, operation := "closure-fixture", strings.Repeat("a", 64)
	legacyGeneration := managedEnvironmentGenerationAtValidation(name, "python", packages, "", 0)
	legacyPrefix := filepath.Join(config.CondaEnvsPath, ".generations", name, legacyGeneration)
	if err := install(legacyPrefix); err != nil {
		t.Fatal(err)
	}
	marker := managedEnvironmentMarker{SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: "python", Kind: "conda", Generation: legacyGeneration, Packages: packages, OperationKey: operation}
	if err := writeManagedEnvironmentMarker(filepath.Join(legacyPrefix, managedEnvironmentMarkerName), marker); err != nil {
		t.Fatal(err)
	}
	if err := activateManagedEnvironment(config.CondaEnvsPath, name, legacyPrefix); err != nil {
		t.Fatal(err)
	}
	before := installCount
	created, err := manager.publishManagedEnvironment(ctx, name, "python", "create", operation, nil, "", false, []string{"json"}, nil, install)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "ready" || created.Generation == legacyGeneration || installCount != before+2 {
		t.Fatalf("publication=%+v installs=%d", created, installCount)
	}
	if _, err := os.Stat(legacyPrefix); err != nil {
		t.Fatal("legacy evidence lost", err)
	}
	if err := manager.VerifyManagedEnvironmentImports(ctx, name, []string{"json"}); err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(config)
	recovered, err := restarted.publishManagedEnvironment(ctx, name, "python", "create", operation, nil, "", false, []string{"json"}, nil, func(string) error { t.Fatal("restart reinstalled a verified generation"); return nil })
	if err != nil || recovered.Generation != created.Generation {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	failureRoot := t.TempDir()
	failureArchive := managedInstallerFixturePackage(t, failureRoot, "#!/bin/sh\nexit 19\n")
	_, err = manager.publishManagedEnvironment(ctx, "failed-fixture", "python", "create", strings.Repeat("b", 64), nil, "", false, nil, nil, func(prefix string) error {
		return manager.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "--offline", "-p", prefix, failureArchive)
	})
	if err == nil {
		t.Fatal("failed hook published")
	}
	if _, err := os.Lstat(filepath.Join(config.CondaEnvsPath, "failed-fixture")); !os.IsNotExist(err) {
		t.Fatalf("failed environment activated: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(config.CondaEnvsPath, ".generations/failed-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("failed installation left reusable directory %s", entry.Name())
		}
	}
}

// The caller supplies an exact hash-pinned public test distribution. This
// opt-in proves TLS and extraction after the build toolchain is detached.
func TestManagedInstallerRealHTTPSPackage(t *testing.T) {
	source, installer := os.Getenv("SYNON_TEST_INSTALLER_PACKAGE_URL"), os.Getenv("SYNON_TEST_MICROMAMBA")
	if os.Getenv("SYNON_TEST_REAL_INSTALLER") != "1" || source == "" || installer == "" {
		t.Skip("opt-in real HTTPS installer probe")
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.User != nil || !validSHA256(u.Fragment) {
		t.Fatal("probe requires a credential-free SHA256-pinned HTTPS distribution")
	}
	root := t.TempDir()
	manager := NewManager(Config{Micromamba: installer, CondaHome: filepath.Join(root, "cache"), CondaEnvsPath: filepath.Join(root, "envs")})
	explicit := filepath.Join(root, "explicit.txt")
	if err := os.WriteFile(explicit, []byte(fmt.Sprintf("@EXPLICIT\n%s\n", source)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	prefix := filepath.Join(root, "installed")
	if err := manager.runManagedEnvironmentCommand(ctx, "--no-rc", "create", "-y", "-p", prefix, "-f", explicit); err != nil {
		t.Fatal(err)
	}
	packages, err := manager.inspectManagedEnvironmentPackages(ctx, prefix)
	if err != nil || len(packages) != 1 {
		t.Fatalf("actual package inventory=%v err=%v", packages, err)
	}
}

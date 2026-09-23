package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func repositoryRootForCondaRuntimeTest(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func bundledManagedPythonConfig(t *testing.T) Config {
	t.Helper()
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	return Config{
		CondaRuntimeCatalog:      filepath.Join(optional, "conda-runtimes", "manifest.json"),
		ManagedPythonEnvironment: defaultManagedPythonEnvironment,
		ManifestPath:             filepath.Join(optional, "kernel-compute.manifest.json"),
	}
}

func TestBundledManagedPythonRuntimeContractIncludesPinnedChemistryCapability(t *testing.T) {
	runtime, err := loadManagedPythonRuntime(bundledManagedPythonConfig(t))
	if err != nil {
		t.Fatalf("load bundled managed Python runtime: %v", err)
	}
	if runtime.entry.Name != defaultManagedPythonEnvironment || runtime.entry.Generation != runtime.manifestDigest {
		t.Fatalf("runtime identity = %#v", runtime.entry)
	}
	if runtime.pythonVersion != "3.11.15" || runtime.rdkitVersion != "2024.03.5" {
		t.Fatalf("runtime versions python=%q rdkit=%q", runtime.pythonVersion, runtime.rdkitVersion)
	}
	required := map[string]string{}
	for _, item := range runtime.manifest.RequiredPackages {
		required[item.Name] = item.Version
	}
	if required["python"] != "3.11.*" || required["rdkit"] != "2024.03.5" || required["py3dmol"] != "2.5.4" {
		t.Fatalf("required packages = %#v", required)
	}
}

func TestManagedPythonRuntimeReadyRejectsBareExecutableWithoutGenerationEvidence(t *testing.T) {
	config := bundledManagedPythonConfig(t)
	config.CondaEnvsPath = t.TempDir()
	prefix := filepath.Join(config.CondaEnvsPath, defaultManagedPythonEnvironment, "bin")
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "python"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(config)
	if manager.RuntimeReady("python", defaultManagedPythonEnvironment) {
		t.Fatal("bare executable without content-addressed marker was accepted")
	}
}

func TestManagedEnvironmentRuntimePinsExecutableAndPrefixToOneResolvedGeneration(t *testing.T) {
	root := t.TempDir()
	generation := filepath.Join(root, ".generations", "test-python", "generation-a")
	if err := os.MkdirAll(filepath.Join(generation, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(generation, "bin", "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(generation, filepath.Join(root, "test-python")); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{CondaEnvsPath: root})
	resolvedPrefix, resolvedPython, err := manager.managedEnvironmentRuntime("test-python", "python")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPrefix != generation || resolvedPython != python {
		t.Fatalf("resolved prefix=%q python=%q", resolvedPrefix, resolvedPython)
	}
	environment := manager.runtimeEnvironmentAtPrefix("test-python", "python", t.TempDir(), "kernel-a", "", "", "", resolvedPrefix)
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	if !strings.Contains(joined, "\nCONDA_PREFIX="+generation+"\n") ||
		!strings.Contains(joined, "\nPATH="+managedExecutableSearchPath(filepath.Join(generation, "bin"))+"\n") {
		t.Fatalf("runtime environment does not use the resolved generation: %s", joined)
	}
}

func TestManagedPythonProvisioningIsSingleFlightAndWaiterCancellationDoesNotCancelInstall(t *testing.T) {
	config := bundledManagedPythonConfig(t)
	config.CondaEnvsPath = t.TempDir()
	config.Micromamba = filepath.Join(t.TempDir(), "micromamba")
	manager := NewManager(config)
	wake := manager.RuntimeWake()
	started := make(chan struct{})
	release := make(chan struct{})
	var attempts atomic.Int64
	install := func(context.Context) error {
		if attempts.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- manager.managedPythonProvisioning(cancelledCtx, nil, false, install, true)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("managed Python installation did not start")
	}
	details := manager.ManagedPythonProvisioningDetails()
	if details.Status != "installing" || details.StartedAt.IsZero() || details.LastProgressAt.IsZero() {
		t.Fatalf("shared provisioning details=%#v", details)
	}
	waiter := make(chan error, 1)
	go func() {
		waiter <- manager.managedPythonProvisioning(context.Background(), nil, false, install, true)
	}()
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("installation attempts after waiter cancellation=%d", attempts.Load())
	}
	close(release)
	if err := <-waiter; err != nil {
		t.Fatalf("shared installation result=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("shared installation attempts=%d", attempts.Load())
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("provisioning completion did not wake runtime scheduling")
	}
}

func TestManagedPythonProvisioningUsesServiceLifetimeWithoutInventedDeadline(t *testing.T) {
	config := bundledManagedPythonConfig(t)
	config.CondaEnvsPath = t.TempDir()
	config.Micromamba = filepath.Join(t.TempDir(), "micromamba")
	manager := NewManager(config)
	supervisor, cancelSupervisor := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.RunManagedEnvironmentSupervisor(supervisor) }()
	select {
	case <-manager.managedEnvironmentSupervisorReady:
	case <-time.After(time.Second):
		t.Fatal("managed environment supervisor did not start")
	}

	installStarted := make(chan struct{})
	installCancelled := make(chan error, 1)
	waiter, cancelWaiter := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- manager.managedPythonProvisioning(waiter, nil, false, func(ctx context.Context) error {
			if _, hasDeadline := ctx.Deadline(); hasDeadline {
				return errors.New("managed Python provisioning received an invented deadline")
			}
			close(installStarted)
			<-ctx.Done()
			installCancelled <- ctx.Err()
			return ctx.Err()
		}, true)
	}()
	select {
	case <-installStarted:
	case <-time.After(time.Second):
		t.Fatal("managed Python provisioning did not start")
	}
	cancelWaiter()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v", err)
	}
	select {
	case err := <-installCancelled:
		t.Fatalf("waiter cancellation stopped service-owned install: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancelSupervisor()
	if err := <-installCancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("service cancellation error=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("supervisor result=%v", err)
	}
}

func TestManagedPythonProvisioningFailureRequiresExplicitRetry(t *testing.T) {
	config := bundledManagedPythonConfig(t)
	config.CondaEnvsPath = t.TempDir()
	config.Micromamba = filepath.Join(t.TempDir(), "micromamba")
	manager := NewManager(config)
	failed := errors.New("install failed")
	var attempts atomic.Int64
	install := func(context.Context) error {
		if attempts.Add(1) == 1 {
			return failed
		}
		return nil
	}
	if err := manager.managedPythonProvisioning(context.Background(), nil, false, install, true); !errors.Is(err, failed) {
		t.Fatalf("first installation error=%v", err)
	}
	if err := manager.managedPythonProvisioning(context.Background(), nil, false, install, true); !errors.Is(err, failed) {
		t.Fatalf("implicit retry error=%v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("failed installation retried implicitly: attempts=%d", attempts.Load())
	}
	if status := manager.ManagedPythonProvisioningStatus(); status != "failed" {
		t.Fatalf("failed provisioning status=%q", status)
	}
	failedDetails := manager.ManagedPythonProvisioningDetails()
	if failedDetails.Status != "failed" || failedDetails.Error != failed.Error() || failedDetails.StartedAt.IsZero() {
		t.Fatalf("failed provisioning details=%#v", failedDetails)
	}
	if err := manager.managedPythonProvisioning(context.Background(), nil, true, install, true); err != nil {
		t.Fatalf("explicit retry error=%v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("explicit retry attempts=%d", attempts.Load())
	}
}

package kernel

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderKernelUsesConfinedCredentialFDAndDomainProxy(t *testing.T) {
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	state := t.TempDir()
	environments := filepath.Join(state, "conda", "envs")
	prefix := filepath.Join(environments, "provider-fixture")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(prefix, "bin", "python")); err != nil {
		t.Fatal(err)
	}

	providerRoot := t.TempDir()
	providerPath := filepath.Join(providerRoot, "provider.py")
	providerSource := `
import builtins
import hashlib
import os
import re

class FixtureProvider:
    secret_env_prefixes = ("FIXTURE_SECRET_",)
    token_scrub_regex = re.compile(r"super-secret-token")

    def __init__(self, repl=False):
        self.repl = repl

    def apply_auth(self, credentials):
        self.credentials = credentials

    def import_and_patch(self):
        builtins.PROVIDER_AUTH_SHA = hashlib.sha256(
            self.credentials["token_secret"].encode("utf-8")
        ).hexdigest()
        builtins.PROVIDER_SECRET_IN_ENV = self.credentials["token_secret"] in os.environ.values()
        builtins.PROVIDER_SECRET_IN_CMD = self.credentials["token_secret"].encode("utf-8") in open("/proc/self/cmdline", "rb").read()

    def install_unauth_hook(self, callback):
        self.unauth_callback = callback

PROVIDER = FixtureProvider
`
	if err := os.WriteFile(providerPath, []byte(providerSource), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(Config{
		Python:           python,
		CondaHome:        filepath.Join(state, "conda"),
		CondaEnvsPath:    environments,
		AssetRoot:        optional,
		ManifestPath:     filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 15 * time.Second,
		ShutdownTimeout:  2 * time.Second,
		InterruptGrace:   time.Second,
	})
	t.Cleanup(func() { _, _ = manager.CloseAll(context.Background()) })

	secret := "super-secret-token"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	workspace := t.TempDir()
	spec := SessionSpec{
		KernelID: "provider-fixture-kernel", FrameID: "provider-frame", RootFrameID: "provider-frame",
		KernelKind: "provider", Language: "python", Environment: "provider-fixture", WorkspaceDir: workspace,
	}
	runtimeSpec := ProviderRuntimeSpec{
		ProviderID: "fixture", Environment: "provider-fixture",
		BootstrapPath:    filepath.Join(optional, "compute", "provider_kernel_bootstrap.py"),
		EntrypointPath:   filepath.Join(optional, "compute", "operon_compute_provider", "__main__.py"),
		ProviderPath:     providerPath,
		EnvironmentsPath: providerRoot,
		InstallID:        "fixture-install",
		Credentials:      map[string]string{"token_secret": secret},
		EgressRules:      []ProviderEgressRule{{Host: "api.provider.test", Port: 443}},
		Dial: func(_ context.Context, host string, port int, _ bool) (net.Conn, error) {
			if host != "api.provider.test" || port != 443 {
				return nil, fmt.Errorf("unexpected target %s:%d", host, port)
			}
			client, remote := net.Pipe()
			go func() {
				defer remote.Close()
				_, _ = io.Copy(remote, remote)
			}()
			return client, nil
		},
	}
	worker, configHash, err := manager.StartProviderSession(context.Background(), spec, runtimeSpec)
	if err != nil {
		t.Fatal(err)
	}
	if configHash == "" || !manager.ProviderSessionActive(spec.KernelID, "fixture", configHash) {
		t.Fatalf("provider session was not registered: hash=%q", configHash)
	}

	code := `
import socket
print(PROVIDER_AUTH_SHA)
print("secret-in-env", PROVIDER_SECRET_IN_ENV)
print("secret-in-cmd", PROVIDER_SECRET_IN_CMD)
print("super-secret-token")
s = socket.create_connection(("127.0.0.1", 1080), timeout=2)
host = b"api.provider.test"
s.sendall(b"\x05\x01\x00")
assert s.recv(2) == b"\x05\x00"
s.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + b"\x01\xbb")
assert len(s.recv(10)) == 10
s.sendall(b"provider-runtime-ok")
print(s.recv(19).decode("ascii"))
s.close()
try:
    socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    print("unix-socket-opened")
except PermissionError:
    print("unix-socket-blocked")
`
	response, err := worker.Execute(context.Background(), code, "agent")
	if err != nil {
		t.Fatalf("provider execute: %v stderr=%s", err, response.Stderr)
	}
	if response.Error != "" || !strings.Contains(response.Stdout, digest) ||
		!strings.Contains(response.Stdout, "***") || strings.Contains(response.Stdout, secret) ||
		!strings.Contains(response.Stdout, "secret-in-env False") || !strings.Contains(response.Stdout, "secret-in-cmd False") ||
		!strings.Contains(response.Stdout, "provider-runtime-ok") || !strings.Contains(response.Stdout, "unix-socket-blocked") ||
		strings.Contains(response.Stdout, "unix-socket-opened") {
		t.Fatalf("provider response=%#v", response)
	}

	reused, reusedHash, err := manager.StartProviderSession(context.Background(), spec, runtimeSpec)
	if err != nil || reused != worker || reusedHash != configHash {
		t.Fatalf("provider reuse worker=%p reused=%p hash=%q/%q err=%v", worker, reused, configHash, reusedHash, err)
	}
	if err := manager.CloseProviderSession(context.Background(), spec.KernelID); err != nil {
		t.Fatal(err)
	}
	if manager.ProviderSessionActive(spec.KernelID, "fixture", configHash) {
		t.Fatal("closed provider session remained active")
	}
}

func TestRealModalProviderEnvironmentLoadsInsideConfinedKernel(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_PROVIDER_RUNTIME") != "1" {
		t.Skip("set SYNON_RUN_REAL_PROVIDER_RUNTIME=1 to provision and verify the real provider SDK runtime")
	}
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	skillRoot := filepath.Join(root, "skills", "synonbiomed", "remote-compute-modal")
	state := t.TempDir()
	manager := NewManager(Config{
		Python:     "/usr/bin/python3",
		Micromamba: filepath.Join(optional, "micromamba", "linux-x86_64", "micromamba"),
		CondaHome:  filepath.Join(state, "conda"), CondaEnvsPath: filepath.Join(state, "conda", "envs"),
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 30 * time.Second, ShutdownTimeout: 3 * time.Second,
	})
	startManagedEnvironmentSupervisor(t, manager)
	t.Cleanup(func() { _, _ = manager.CloseAll(context.Background()) })
	requirementsPath := filepath.Join(skillRoot, "requirements.lock")
	environment, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "compute-provider-modal", Language: "python", Packages: []string{"python=3.11", "pip"},
		ImportNames: []string{"modal", "python_socks"}, LockedRequirementsPath: requirementsPath,
		LockedRequirementsSHA256: "20c8541717b9dcf31bba3f8362e3e2c1adafe011cb2a4a1dbee36bce7d1ad0dd",
	})
	if err != nil {
		t.Fatalf("provision real provider environment: %v", err)
	}
	if environment.Status != "ready" {
		t.Fatalf("provider environment=%#v", environment)
	}
	workspace := t.TempDir()
	spec := SessionSpec{
		KernelID: "provider-modal-real", FrameID: "provider-modal-frame", RootFrameID: "provider-modal-frame",
		KernelKind: "provider", Language: "python", Environment: environment.Name, WorkspaceDir: workspace,
	}
	worker, _, err := manager.StartProviderSession(context.Background(), spec, ProviderRuntimeSpec{
		ProviderID: "modal", Environment: environment.Name,
		BootstrapPath:  filepath.Join(optional, "compute", "provider_kernel_bootstrap.py"),
		EntrypointPath: filepath.Join(optional, "compute", "operon_compute_provider", "__main__.py"),
		ProviderPath:   filepath.Join(skillRoot, "provider.py"), EnvironmentsPath: filepath.Join(skillRoot, "envs"),
		InstallID: "real-provider-import", Credentials: map[string]string{
			"token_id": "ak-offline-import", "token_secret": "as-offline-import",
		},
		EgressRules: []ProviderEgressRule{
			{Host: "api.modal.com", Port: 443}, {Suffix: ".w.modal.host", Port: 443},
			{Suffix: ".amazonaws.com", Port: 443}, {Suffix: ".r2.cloudflarestorage.com", Port: 443},
		},
	})
	if err != nil {
		t.Fatalf("start real provider kernel: %v", err)
	}
	response, err := worker.Execute(context.Background(), `import modal, python_socks; print(modal.__version__); print(hasattr(modal, "Sandbox"))`, "agent")
	if err != nil || response.Error != "" || !strings.Contains(response.Stdout, "True") {
		t.Fatalf("real provider import response=%#v err=%v", response, err)
	}
}

func TestRealInferenceProviderEnvironmentRoutesRequestsAndHTTPXThroughProxy(t *testing.T) {
	if os.Getenv("SYNON_RUN_REAL_PROVIDER_RUNTIME") != "1" {
		t.Skip("set SYNON_RUN_REAL_PROVIDER_RUNTIME=1 to provision and verify the real provider SDK runtime")
	}
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	state := t.TempDir()
	manager := NewManager(Config{
		Python:     "/usr/bin/python3",
		Micromamba: filepath.Join(optional, "micromamba", "linux-x86_64", "micromamba"),
		CondaHome:  filepath.Join(state, "conda"), CondaEnvsPath: filepath.Join(state, "conda", "envs"),
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:       filepath.Join(optional, "kernels", "kernel_worker.py"),
		ExecutionTimeout: 30 * time.Second, ShutdownTimeout: 3 * time.Second,
	})
	startManagedEnvironmentSupervisor(t, manager)
	t.Cleanup(func() { _, _ = manager.CloseAll(context.Background()) })
	requirementsPath := filepath.Join(optional, "compute", "inference_requirements.lock")
	environment, err := manager.CreateManagedEnvironment(context.Background(), CreateManagedEnvironmentInput{
		Name: "compute-provider-http", Language: "python", Packages: []string{"python=3.11", "pip"},
		ImportNames: []string{"httpx", "requests"}, LockedRequirementsPath: requirementsPath,
		LockedRequirementsSHA256: "318b702d84611dc1fa36a8a75aa237f6d8641bfe93b2b3a178955c5f8c876fef",
	})
	if err != nil {
		t.Fatalf("provision real inference environment: %v", err)
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("real-inference-ok"))
	}))
	defer endpoint.Close()
	workspace := t.TempDir()
	spec := SessionSpec{
		KernelID: "provider-http-real", FrameID: "provider-http-frame", RootFrameID: "provider-http-frame",
		KernelKind: "provider", Language: "python", Environment: environment.Name, WorkspaceDir: workspace,
	}
	worker, _, err := manager.StartProviderSession(context.Background(), spec, ProviderRuntimeSpec{
		ProviderID: "http-fixture", Environment: environment.Name,
		BootstrapPath:    filepath.Join(optional, "compute", "provider_kernel_bootstrap.py"),
		EntrypointPath:   filepath.Join(optional, "compute", "operon_compute_provider", "__main__.py"),
		ProviderPath:     filepath.Join(optional, "compute", "inference_provider.py"),
		EnvironmentsPath: filepath.Join(optional, "compute"), InstallID: "real-inference-import",
		Credentials: map[string]string{"base_url": endpoint.URL},
		EgressRules: []ProviderEgressRule{{
			Host: "127.0.0.1", Port: endpoint.Listener.Addr().(*net.TCPAddr).Port, AllowPrivate: true,
		}},
		ExtraEnvironment: map[string]string{"SYNON_PROVIDER_PROXY_LOCAL_TARGET": "1"},
	})
	if err != nil {
		t.Fatalf("start real inference kernel: %v", err)
	}
	response, err := worker.Execute(context.Background(), `import httpx, requests; print(requests.get(BASE_URL).text); print(httpx.get(BASE_URL).text)`, "agent")
	if err != nil || response.Error != "" || strings.Count(response.Stdout, "real-inference-ok") != 2 {
		t.Fatalf("real inference response=%#v err=%v", response, err)
	}
}

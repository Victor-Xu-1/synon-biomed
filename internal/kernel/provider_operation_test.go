package kernel

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProviderOperationRunsOneShotInsideConfinementWithCredentialStdin(t *testing.T) {
	root := repositoryRootForCondaRuntimeTest(t)
	optional := filepath.Join(root, "assets", "optional")
	state := t.TempDir()
	environments := filepath.Join(state, "conda", "envs")
	prefix := filepath.Join(environments, "provider-operation-fixture")
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
	provider := `
import os
import re

class FixtureProvider:
    secret_env_prefixes = ("FIXTURE_",)
    token_scrub_regex = re.compile(r"operation-secret")

    def __init__(self, repl=False):
        self.credentials = {}
        self.owner = None

    def apply_auth(self, credentials):
        self.credentials = credentials

    def import_and_patch(self):
        if self.credentials["token_secret"] in os.environ.values():
            raise RuntimeError("credential leaked into environment")
        if self.credentials["token_secret"].encode() in open("/proc/self/cmdline", "rb").read():
            raise RuntimeError("credential leaked into command line")

    def install_unauth_hook(self, callback):
        pass

    def create_sandbox(self, spec, install_id, tags=None):
        self.owner = install_id
        return "sandbox-fixture"

    def read_owner(self, sandbox_id):
        return self.owner if sandbox_id == "sandbox-fixture" else None

    def terminate(self, sandbox_id):
        pass

PROVIDER = FixtureProvider
`
	if err := os.WriteFile(providerPath, []byte(provider), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		Python: python, CondaHome: filepath.Join(state, "conda"), CondaEnvsPath: environments,
		AssetRoot: optional, ManifestPath: filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath: filepath.Join(optional, "kernels", "kernel_worker.py"),
	})
	result, err := manager.RunProviderOperation(context.Background(), ProviderOperationInput{
		Runtime: ProviderRuntimeSpec{
			ProviderID: "fixture", Environment: "provider-operation-fixture",
			BootstrapPath:  filepath.Join(optional, "compute", "provider_kernel_bootstrap.py"),
			EntrypointPath: filepath.Join(optional, "compute", "operon_compute_provider", "__main__.py"),
			ProviderPath:   providerPath, EnvironmentsPath: providerRoot, InstallID: "operation-install",
			Credentials: map[string]string{"token_secret": "operation-secret"},
			EgressRules: []ProviderEgressRule{{Host: "api.provider.test", Port: 443}},
			Dial: func(context.Context, string, int, bool) (net.Conn, error) {
				return nil, os.ErrPermission
			},
		},
		Operation: "create",
		Request: map[string]any{
			"spec": map[string]any{"image": "fixture"}, "install_id": "operation-install",
			"tags": map[string]string{"synonbiomed-job": "job-fixture"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["sandbox_id"] != "sandbox-fixture" {
		t.Fatalf("provider operation result=%#v", result)
	}
}

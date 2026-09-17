package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillCreatorUsesOneInProcessModelEvaluationAuthority(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "skills", "synonbiomed", "skill-creator", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "host.llm(requests, max_concurrency=4)") ||
		strings.Contains(text, "llm -p") || strings.Contains(text, "scripts.run_loop") {
		t.Fatalf("Skill creator model-evaluation authority drifted")
	}
	for _, retired := range []string{"run_eval.py", "run_loop.py", "improve_description.py"} {
		if _, err := os.Stat(filepath.Join(root, "skills", "synonbiomed", "skill-creator", "scripts", retired)); !os.IsNotExist(err) {
			t.Fatalf("retired competing evaluator %s still exists: %v", retired, err)
		}
	}
}

func TestRemoteComputeProviderAppliesIdentityAndKernelBounds(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	provider := filepath.Join(root, "skills", "synonbiomed", "remote-compute-modal", "provider.py")
	script := `
import importlib.util
import os
import sys
import types

sys.dont_write_bytecode = True
stub = types.ModuleType("operon_compute_provider")
stub.WORK = "/work"
class ByocError(Exception):
    def __init__(self, kind, msg=""):
        super().__init__(msg)
        self.kind = kind
        self.msg = msg
stub.ByocError = ByocError
stub.ExecResult = object
sys.modules["operon_compute_provider"] = stub

spec = importlib.util.spec_from_file_location("synon_modal_provider", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
assert module.IDLE_EXIT_S == 900
assert "egress-staging" in module.LEGACY_APP_NAMES

os.environ["OPERON_BYOC_INSTALL_ID"] = "install-owned"
os.environ["OPERON_BYOC_ORG_ID"] = "owner-user"
os.environ["OPERON_BYOC_FRAME_ID"] = "frame-owned"

class Sandbox:
    created = []
    def __init__(self, tags):
        self.tags = dict(tags)
        self.written = None
    @staticmethod
    def create(*args, **kwargs):
        Sandbox.created.append(dict(kwargs))
        return kwargs
    def get_tags(self):
        return dict(self.tags)
    def set_tags(self, tags, *args, **kwargs):
        self.written = dict(tags)
        return self.written

class Modal:
    pass
Modal.Sandbox = Sandbox

provider = module.ModalProvider(repl=True)
provider._install_gpu_guard(Modal)
Modal.Sandbox.create("bash")
created = Sandbox.created[-1]
assert created["tags"][module.OWNER_TAG] == "install-owned"
assert created["tags"]["synon-biomed-org"] == "owner-user"
assert created["tags"]["synon-biomed-frame"] == "frame-owned"

for kwargs in ({"gpu": "A100"}, {"timeout": 1801}):
    try:
        Modal.Sandbox.create("bash", **kwargs)
    except ByocError as exc:
        assert exc.kind == "invalid_request"
    else:
        raise AssertionError(f"unsafe repl create was accepted: {kwargs!r}")

owned = Sandbox({module.OWNER_TAG: "install-owned", "synon-biomed-frame": "frame-owned"})
owned.set_tags({module.OWNER_TAG: "foreign", "experiment": "r2"})
assert owned.written[module.OWNER_TAG] == "install-owned"
assert owned.written["synon-biomed-frame"] == "frame-owned"
assert owned.written["experiment"] == "r2"

foreign = Sandbox({"experiment": "foreign"})
foreign.set_tags({module.OWNER_TAG: "install-owned", "experiment": "changed"})
assert module.OWNER_TAG not in foreign.written

print("provider-alignment-ok")
`
	command := exec.Command(python, "-I", "-c", script, provider)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("provider alignment failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "provider-alignment-ok") {
		t.Fatalf("provider alignment marker missing: %s", output)
	}
}

func TestRemoteComputeProviderIncludesPinnedOpenMMEnvironment(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(root, "skills", "synonbiomed", "remote-compute-modal", "envs", "md_openmm_gpu.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		`"openmm=8.2.0"`, `"openmmforcefields=0.15.1"`, `"pdbfixer=1.12"`,
		`"mdtraj=1.11.1"`, `"openff-toolkit=0.18.0"`, `"cudatoolkit=11.8"`,
		`"numpy=2.4.6"`, `"gpu_default": "A100"`, `"egress_domains": []`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("OpenMM environment is missing pinned contract %q", required)
		}
	}
	if strings.Contains(strings.ToLower(text), "claude science") {
		t.Fatal("OpenMM environment imported an external product label")
	}
	script := `
import importlib.util
import sys
import types

sys.dont_write_bytecode = True
modal = types.ModuleType("modal")
class Image:
    def __init__(self):
        self.python_version = None
        self.packages = ()
        self.channels = ()
        self.commands = ()
    @staticmethod
    def micromamba(*, python_version):
        image = Image()
        image.python_version = python_version
        return image
    def micromamba_install(self, *packages, channels):
        self.packages = packages
        self.channels = tuple(channels)
        return self
    def run_commands(self, *commands):
        self.commands = commands
        return self
modal.Image = Image
modal.Volume = object
sys.modules["modal"] = modal

spec = importlib.util.spec_from_file_location("synon_md_openmm_gpu", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
image, volumes, environment = module.build(secrets={"ignored": "value"})
assert image.python_version == "3.11"
assert image.channels == ("conda-forge",)
assert "openmm=8.2.0" in image.packages
assert "cudatoolkit=11.8" in image.packages
assert image.commands and "getNumPlatforms" in image.commands[0]
assert volumes == {} and environment == {}
print("openmm-environment-ok")
`
	command := exec.Command(python, "-I", "-c", script, path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenMM environment contract failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "openmm-environment-ok") {
		t.Fatalf("OpenMM environment marker missing: %s", output)
	}
}

func TestInferenceProviderRejectsCredentialControlEnvironmentAliases(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	provider := filepath.Join(root, "assets", "optional", "compute", "inference_provider.py")
	script := `
import importlib.util
import os
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("synon_inference_provider", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
provider = module.InferenceProvider(repl=True)
os.environ.pop("HTTP_PROXY", None)
provider.apply_auth({
    "base_url": "https://inference.example/v1",
    "credential_name": "HTTP_PROXY",
    "credential_value": "credential-value",
})
assert "HTTP_PROXY" not in os.environ
assert os.environ["INFER_API_KEY"] == "credential-value"
provider.apply_auth({
    "base_url": "https://inference.example/v1",
    "credential_name": "NVIDIA_API_KEY",
    "credential_value": "credential-value",
})
assert os.environ["NVIDIA_API_KEY"] == "credential-value"
provider.import_and_patch()
assert BASE_URL == "https://inference.example/v1"
assert "NVIDIA_" in provider.secret_env_prefixes
print("inference-alignment-ok")
`
	command := exec.Command(python, "-I", "-c", script, provider)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inference provider alignment failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "inference-alignment-ok") {
		t.Fatalf("inference provider alignment marker missing: %s", output)
	}
}

package kernel

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Exercise the public creation boundary. This installer fixture only supplies
// interpreters explicitly requested by the runtime, so accidental dependencies
// cannot be satisfied by the test host or an unrelated environment.
func TestManagedEnvironmentCreatePhaseDependencies(t *testing.T) {
	for _, mode := range []string{"native-only", "pip-phases", "locked-pip"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "commands")
			installer := filepath.Join(root, "micromamba")
			script := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >>%q
mode=""; prefix=""; python="no"
for item in "$@"; do
  case "$item" in python=*) python="yes" ;; esac
done
while [ "$#" -gt 0 ]; do
  case "$1" in
    create|install|list) mode="$1" ;;
    -p) shift; prefix="$1" ;;
  esac
  shift
done
case "$mode" in
  create|install)
    mkdir -p "$prefix/bin"
    cat >"$prefix/bin/Rscript" <<'RS'
#!/bin/sh
case "$*" in
  *SYNON_IMPORT_WITNESS_OK*) printf 'SYNON_IMPORT_WITNESS_OK\n' ;;
  *) printf '{"ok":true}\n' ;;
esac
RS
    chmod 755 "$prefix/bin/Rscript"
    if [ "$python" = yes ]; then
      cat >"$prefix/bin/python" <<'PY'
#!/bin/sh
printf '%%s\n' "$*" >>%q
case "$*" in
  *"-m pip install"*) exit 0 ;;
  *) exit 73 ;;
esac
PY
      chmod 755 "$prefix/bin/python"
    fi
    ;;
  list) printf '{"packages":[{"name":"r-base","version":"4.4.3","build_string":"h1","channel":"conda-forge"}]}\n' ;;
  *) exit 74 ;;
esac
`, logPath, logPath)
			if err := os.WriteFile(installer, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(Config{Micromamba: installer, CondaHome: filepath.Join(root, "cache"), CondaEnvsPath: filepath.Join(root, "envs")})
			startManagedEnvironmentSupervisor(t, manager)
			input := CreateManagedEnvironmentInput{Name: "phase-probe", Language: "r", PythonVersion: "3.12", ImportNames: []string{"stats"}}
			if mode == "pip-phases" {
				input.PipPhases = [][]string{{"first==1.0"}, {"second==2.0"}}
			}
			if mode == "locked-pip" {
				content := []byte("probe==1.0 --hash=sha256:" + strings.Repeat("a", 64) + "\n")
				input.LockedRequirementsPath = filepath.Join(root, "requirements.lock")
				input.LockedRequirementsSHA256 = fmt.Sprintf("%x", sha256.Sum256(content))
				if err := os.WriteFile(input.LockedRequirementsPath, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			created, err := manager.CreateManagedEnvironment(context.Background(), input)
			if err != nil || created.Status != "ready" {
				t.Fatalf("public create did not publish a ready environment: %#v, %v", created, err)
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(calls)
			if mode == "native-only" {
				if strings.Contains(commands, "python") || strings.Contains(commands, "pip") {
					t.Fatalf("unused phase introduced interpreter dependency: %s", commands)
				}
			} else if !strings.Contains(commands, "python=3.12") || !strings.Contains(commands, " pip") || !strings.Contains(commands, "-I -m pip install") {
				t.Fatalf("phase dependencies or execution missing: %s", commands)
			}
			if mode == "pip-phases" && strings.Index(commands, "first==1.0") >= strings.Index(commands, "second==2.0") {
				t.Fatalf("phase ordering lost: %s", commands)
			}
			if mode == "locked-pip" && !strings.Contains(commands, "--require-hashes -r "+input.LockedRequirementsPath) {
				t.Fatalf("locked phase hash enforcement lost: %s", commands)
			}
			if mode == "native-only" {
				mutated, err := manager.InstallManagedPackages(context.Background(), MutateManagedPackagesInput{
					Environment: input.Name, Packages: []string{"bridge==1.0"}, UsePip: true,
				})
				if err != nil || mutated.Status != "ready" || mutated.Language != "r" {
					t.Fatalf("adding a new installer phase to an existing native runtime failed: %#v, %v", mutated, err)
				}
			}
		})
	}
}

func TestManagedPipInstallPlanStopsAtFailedOrCancelledStage(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "calls")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >>%q\nexit 17\n", logPath)
			if err := os.WriteFile(filepath.Join(root, "bin/python"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			plan := planManagedPipInstall([][]string{{"first==1.0"}, {"second==2.0"}}, nil, nil, nil, "")
			err := NewManager(Config{}).runManagedPipInstallPlan(ctx, root, plan)
			if err == nil {
				t.Fatal("failed phase accepted")
			}
			calls, readErr := os.ReadFile(logPath)
			if canceled {
				if !errors.Is(err, context.Canceled) || !os.IsNotExist(readErr) {
					t.Fatalf("cancelled plan started a process: %q, %v", calls, err)
				}
			} else if readErr != nil || !strings.Contains(string(calls), "first==1.0") || strings.Contains(string(calls), "second==2.0") {
				t.Fatalf("execution continued past a failed stage: %q, %v", calls, readErr)
			}
		})
	}
}

func TestManagedPipBootstrapPreservesInstalledInterpreterPins(t *testing.T) {
	for _, tc := range []struct{ inventory, want []string }{
		{nil, []string{"python=" + defaultManagedPythonVersion, "pip"}},
		{[]string{"python=3.13.2=h1=conda-forge"}, []string{"pip"}},
		{[]string{"python=3.10.15=h2=conda-forge", "pip=24.0=h1=conda-forge"}, nil},
		{[]string{"pip=25.0=h1=conda-forge"}, []string{"python=" + defaultManagedPythonVersion}},
	} {
		if got := managedPipBootstrapPackages(tc.inventory); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("bootstrap replaced installed dependencies: inventory=%v got=%v want=%v", tc.inventory, got, tc.want)
		}
	}
}

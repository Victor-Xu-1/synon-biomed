//go:build linux && amd64

package kernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseRMinorVersionMatchesWorkspaceContract(t *testing.T) {
	for _, test := range []struct {
		output string
		minor  string
	}{
		{"R version 4.5.1 (2025-06-13)", "4.5"},
		{"R version 4.5.2 Patched (2025-07-01)", "4.5"},
		{"R version 4.6.0 alpha (2026-02-01)", "4.6"},
		{"R version 4.6.0 beta (2026-03-01)", "4.6"},
		{"R version 4.6.0 RC (2026-04-01)", "4.6"},
		{"R version 4.7.0 Under development (unstable) (2027-01-02)", "4.7"},
	} {
		minor, ok := parseRMinorVersion(test.output)
		if !ok || minor != test.minor {
			t.Fatalf("output=%q minor=%q ok=%v", test.output, minor, ok)
		}
	}
	for _, output := range []string{
		"gcc version 4.5.1 (2025-06-13)",
		"R version 4.5.1 devel (2025-06-13)",
		"R version 4.5.1 (revision 123)",
		"R version 4.5 (2025-06-13)",
		"R version 4.5.1 (2025/06/13)",
	} {
		if minor, ok := parseRMinorVersion(output); ok {
			t.Fatalf("invalid output=%q minor=%q", output, minor)
		}
	}
}

func TestRPackageOperationMetadataMergesAndSortsWhileSkippingBadSidecarLines(t *testing.T) {
	root := t.TempDir()
	prefix := filepath.Join(root, "r")
	if err := os.Mkdir(prefix, 0o700); err != nil {
		t.Fatal(err)
	}
	main := `{"op_log":[{"timestamp":"2026-07-25T03:00:00Z","operation":"r_cran_install","packages":["jsonlite"],"result":"success"},{"timestamp":"2026-07-25T03:30:00Z","operation":"python_pip_install","packages":["numpy"],"result":"success","tool":"pip"}]}`
	sidecar := strings.Join([]string{
		`{"timestamp":"2026-07-25T02:00:00Z","operation":"r_github_install","packages":["org/pkg@main","org/other@dev"],"result":"success","github_ref":["org/pkg@main","org/other@dev"]}`,
		`not-json`,
		`{"timestamp":"2026-07-25T04:00:00Z","operation":"r_bioc_install","packages":["BiocGenerics"],"result":"error"}`,
	}, "\n") + "\n"
	for name, content := range map[string]string{".operon_metadata.json": main, ".operon_metadata.r.ndjson": sidecar} {
		if err := os.WriteFile(filepath.Join(prefix, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(Config{CondaEnvsPath: root})
	metadata, err := manager.ReadRuntimeEnvironmentMetadata("r")
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.OperationLog) != 3 {
		t.Fatalf("metadata=%#v", metadata)
	}
	want := []string{"r_github_install", "r_cran_install", "r_bioc_install"}
	for index, operation := range metadata.OperationLog {
		if operation.Operation != want[index] {
			t.Fatalf("operations=%#v", metadata.OperationLog)
		}
	}
	if strings.Join(metadata.OperationLog[0].GitHubRef, ",") != "org/pkg@main,org/other@dev" {
		t.Fatalf("github operation=%#v", metadata.OperationLog[0])
	}
}

func TestROperationLogRejectsFIFOAndHardlinkWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			prefix := filepath.Join(root, "r")
			if err := os.Mkdir(prefix, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(prefix, ".operon_metadata.r.ndjson")
			if kind == "fifo" {
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				sensitive := filepath.Join(root, "sensitive")
				if err := os.WriteFile(sensitive, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(sensitive, path); err != nil {
					t.Fatal(err)
				}
			}
			manager := NewManager(Config{CondaEnvsPath: root})
			started := time.Now()
			if _, _, err := manager.ensureROperationLog("r"); err == nil {
				t.Fatalf("%s operation log accepted", kind)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("%s validation blocked for %s", kind, elapsed)
			}
		})
	}
}

func TestRSharedLibraryRejectsSymlinkEscapeAndBuilderPublishesMinorSlice(t *testing.T) {
	root := t.TempDir()
	rscript := filepath.Join(root, "Rscript")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "R version 4.5.1 (2025-06-13)"
  exit 0
fi
while [ "$1" != "--args" ]; do shift; done
shift
target="$1"
shift
mkdir -p "$target"
for package in "$@"; do mkdir -p "$target/$package"; done
exit 0
`
	if err := os.WriteFile(rscript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "shared")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Mkdir(escape, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(base, "4.5")); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{RSharedLibsBase: base})
	if path, file, diagnostic := manager.rSharedLibrary(rscript); path != "" || file != nil || diagnostic != "R shared library is invalid" {
		t.Fatalf("escape path=%q file=%v diagnostic=%q", path, file, diagnostic)
	}
	if err := os.Remove(filepath.Join(base, "4.5")); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(Config{RSharedLibsBase: base, RSharedPackages: []string{"jsonlite", "ggplot2"}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.repairRSharedLibrary(ctx, rscript); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jsonlite", "ggplot2"} {
		if info, err := os.Stat(filepath.Join(base, "4.5", name)); err != nil || !info.IsDir() {
			t.Fatalf("shared package %s missing: %v", name, err)
		}
	}
}

func TestRKernelRestartNoticeIsGenerationBoundAndConsumedOnce(t *testing.T) {
	state := &workerLifecycle{
		spec: SessionSpec{Language: "r", Environment: "r-4.5"}, generation: 17,
		pendingRestart: formatRKernelRestartNotice("r-4.5", "was shut down after a cell timeout"), noticeGeneration: 17,
	}
	response := Response{ID: "cell", Stderr: "user stderr"}
	if stale := consumeRKernelRestartNotice(state, 16, response); stale.Stderr != "user stderr" || state.pendingRestart == "" {
		t.Fatalf("stale response=%#v state=%#v", stale, state)
	}
	first := consumeRKernelRestartNotice(state, 17, response)
	want := "[kernel restarted]\nThis cell ran on a fresh kernel process: the previous R kernel for environment 'r-4.5' was shut down after a cell timeout. Variables, imports, and other in-memory state from earlier cells are gone; workspace files on disk are unaffected. Re-run setup before relying on earlier state.\nuser stderr"
	if first.Stderr != want {
		t.Fatalf("restart stderr=%q\nwant=%q", first.Stderr, want)
	}
	if second := consumeRKernelRestartNotice(state, 17, Response{ID: "next"}); second.Stderr != "" {
		t.Fatalf("second response=%#v", second)
	}
	for reason, suffix := range map[string]string{
		"exited unexpectedly":                  "exited unexpectedly",
		"exited unexpectedly (exit code 0)":    "exited unexpectedly (exit code 0)",
		"exited unexpectedly with exit code 9": "exited unexpectedly with exit code 9",
		"was killed":                           "was killed",
		"was killed by SIGTERM":                "was killed by SIGTERM",
		"was killed — out of memory":           "was killed — out of memory",
		"was killed — possibly out of memory":  "was killed — possibly out of memory",
		"was shut down":                        "was shut down",
		"was aborted during startup":           "was aborted during startup",
	} {
		if notice := formatRKernelRestartNotice("r", reason); !strings.Contains(notice, suffix) {
			t.Fatalf("reason %q notice=%q", reason, notice)
		}
	}
	_ = fmt.Sprintf("%v", state)
}

func TestRKernelImmediateExitRecordsIdentityBoundExpiringRestartCause(t *testing.T) {
	manager := NewManager(Config{})
	key := "owner-a\x00project-a\x00frame-a\x00kernel-a"
	worker, err := manager.startWorker(
		"immediate-r", t.TempDir(), "/usr/bin/sh", []string{"-c", "exit 7"},
		[]string{"HOME=" + t.TempDir(), "PATH=/usr/bin"}, nil, nil, key, "r",
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.done:
	case <-time.After(3 * time.Second):
		t.Fatal("immediate R worker did not exit")
	}
	if wrong := manager.takeKernelRestart(key + "-other"); wrong != "" {
		t.Fatalf("cross-session restart reason=%q", wrong)
	}
	if reason := manager.takeKernelRestart(key); reason != "exited unexpectedly with exit code 7" {
		t.Fatalf("restart reason=%q", reason)
	}
	manager.recordKernelRestart(key, "was shut down")
	pending, ok := manager.peekKernelRestart(key)
	if !ok {
		t.Fatal("restart reason was not peekable")
	}
	manager.restartMu.Lock()
	manager.pendingRestart[key] = pendingKernelRestart{reason: "exited unexpectedly with exit code 9", recordedAt: pending.recordedAt.Add(time.Nanosecond)}
	manager.restartMu.Unlock()
	if manager.consumeKernelRestart(key, pending) {
		t.Fatal("older generation consumed the newer restart cause")
	}
	if reason := manager.takeKernelRestart(key); reason != "exited unexpectedly with exit code 9" {
		t.Fatalf("new generation restart reason=%q", reason)
	}
	manager.restartMu.Lock()
	manager.pendingRestart[key] = pendingKernelRestart{reason: "was shut down", recordedAt: time.Now().Add(-maxPendingRestartAge - time.Minute)}
	manager.restartMu.Unlock()
	if expired := manager.takeKernelRestart(key); expired != "" {
		t.Fatalf("expired restart reason=%q", expired)
	}
	if signal := unixKernelSignalName(unix.SIGTERM); signal != "SIGTERM" {
		t.Fatalf("signal=%q", signal)
	}
}

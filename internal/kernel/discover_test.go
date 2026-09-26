package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/assets"
)

func TestDiscoveryDiagnosticsAreStableAndPathFree(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "private", "kernel_worker.py")
	for _, fixture := range []struct {
		stage       DiscoveryStage
		wantCode    string
		wantMessage string
	}{
		{DiscoveryStageAssetRoot, "kernel_assets_unavailable", "verified kernel assets are unavailable"},
		{DiscoveryStagePython, "kernel_python_invalid", "kernel Python executable verification failed"},
		{DiscoveryStageEnvironment, "kernel_environment_invalid", "kernel environment path verification failed"},
		{DiscoveryStageMicromamba, "kernel_micromamba_invalid", "kernel micromamba verification failed"},
		{DiscoveryStageAssets, "kernel_assets_invalid", "kernel asset verification failed"},
	} {
		cause := errors.New("private failure at " + privatePath)
		err := discoveryFailure(fixture.stage, cause)
		var typed *DiscoveryError
		if !errors.As(err, &typed) || !errors.Is(err, cause) || typed.Stage != fixture.stage {
			t.Fatalf("typed discovery error=%#v", err)
		}
		diagnostic := DiagnoseDiscoveryError(err)
		if diagnostic.Code != fixture.wantCode || diagnostic.Message != fixture.wantMessage {
			t.Fatalf("stage %s diagnostic=%#v", fixture.stage, diagnostic)
		}
		if strings.Contains(diagnostic.Code+diagnostic.Message, privatePath) {
			t.Fatalf("public diagnostic leaked path: %#v", diagnostic)
		}
	}
}

func TestBundledOptionalAssetsAndMicromambaManifestVerify(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "assets", "optional"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := assets.Load(filepath.Join(root, "kernel-compute.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := assets.Verify(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != len(manifest.Files) || report.Checked < 2 {
		t.Fatalf("kernel asset report=%#v manifest=%#v", report, manifest)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the clean source package currently bundles micromamba for linux/amd64")
	}
	t.Setenv("SYNON_MICROMAMBA", "")
	micromamba, err := discoverMicromamba(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "micromamba", "linux-x86_64", "micromamba")
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if micromamba != want {
		t.Fatalf("micromamba=%q want=%q", micromamba, want)
	}
	info, err := os.Stat(micromamba)
	if err != nil || !executableRegularFile(info) {
		t.Fatalf("micromamba info=%#v err=%v", info, err)
	}
}

func TestDiscoverManagerRequiresBundledRGenerationWithoutHostPython(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed R discovery uses the Unix environment layout")
	}
	assetRoot, err := filepath.Abs(filepath.Join("..", "..", "assets", "optional"))
	if err != nil {
		t.Fatal(err)
	}
	envs := filepath.Join(t.TempDir(), "envs")
	rscript := filepath.Join(envs, "r", "bin", "Rscript")
	if err := os.MkdirAll(filepath.Dir(rscript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rscript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rscript, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_KERNEL_ASSET_ROOT", assetRoot)
	t.Setenv("SYNON_KERNEL_PYTHON", "")
	t.Setenv("SYNON_MICROMAMBA", "")
	t.Setenv("PATH", t.TempDir())
	manager, err := DiscoverManagerWithPathsAndProxy(filepath.Dir(envs), envs, "")
	if err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeReady("r", "r") || manager.RuntimeReady("python", "python") {
		t.Fatalf("legacy runtime was advertised before bundled provisioning: r=%t python=%t", manager.RuntimeReady("r", "r"), manager.RuntimeReady("python", "python"))
	}
	if _, err := manager.Start("python-kernel", t.TempDir()); err == nil || !strings.Contains(err.Error(), "Python executable is not configured") {
		t.Fatalf("unconfigured Python start error=%v", err)
	}
}

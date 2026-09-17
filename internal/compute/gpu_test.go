package compute

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseNvidiaSMIQueryMatchesV11GPUShape(t *testing.T) {
	info := parseNvidiaSMIQuery("NVIDIA A100, 40960, 8.0\nNVIDIA A100, 40960, 8.0\n")
	if !info.Available || info.GPUCount != 2 || info.GPUName == nil || *info.GPUName != "NVIDIA A100" ||
		info.GPUMemoryMB == nil || *info.GPUMemoryMB != 40960 || info.ComputeCapability == nil ||
		*info.ComputeCapability != "8.0" || info.CUDAVersion != nil {
		t.Fatalf("info=%#v", info)
	}
	legacy := parseNvidiaSMIQuery("NVIDIA T4, 15109\n")
	if !legacy.Available || legacy.ComputeCapability != nil {
		t.Fatalf("legacy=%#v", legacy)
	}
	empty := parseNvidiaSMIQuery("not valid\n")
	if empty.Available || empty.GPUName != nil || empty.GPUMemoryMB != nil || empty.ComputeCapability != nil ||
		empty.CUDAVersion != nil || empty.GPUCount != 0 {
		t.Fatalf("empty=%#v", empty)
	}
}

func TestNormalizeComputeCapabilityRejectsMalformedValues(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"12.0", "12.0"},
		{" 8.9 ", "8.9"},
		{"unknown", ""},
		{"8", ""},
		{"0.0", ""},
	} {
		if got := normalizedComputeCapability(test.input); got != test.want {
			t.Fatalf("normalizedComputeCapability(%q)=%q want=%q", test.input, got, test.want)
		}
	}
}

func TestResolveNvidiaSMIExecutableUsesWSLDriverPathOutsideServicePATH(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("WSL NVIDIA fallback is Linux-specific")
	}
	info, err := os.Stat(wslNvidiaSMIPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Skip("WSL NVIDIA executable is unavailable")
	}
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-path"))
	if got := resolveNvidiaSMIExecutable(); got != wslNvidiaSMIPath {
		t.Fatalf("resolved NVIDIA SMI=%q want=%q", got, wslNvidiaSMIPath)
	}
}

func TestParseCUDAVersionUsesNVCCAndNvidiaSMIShapes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"Cuda compilation tools, release 12.4, V12.4.131", "12.4"},
		{"NVIDIA-SMI 550.54.15 Driver Version: 550.54.15 CUDA Version: 12.4", "12.4"},
		{"no cuda version", ""},
	} {
		if got := parseCUDAVersion(test.input); got != test.want {
			t.Fatalf("parseCUDAVersion(%q)=%q want=%q", test.input, got, test.want)
		}
	}
}

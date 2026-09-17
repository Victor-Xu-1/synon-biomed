package compute

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var cudaVersionPattern = regexp.MustCompile(`(?i)(?:release|CUDA Version:)\s*([0-9]+(?:\.[0-9]+)*)`)

const wslNvidiaSMIPath = "/usr/lib/wsl/lib/nvidia-smi"

type GPUInfo struct {
	Available         bool    `json:"available"`
	GPUName           *string `json:"gpu_name"`
	GPUMemoryMB       *int64  `json:"gpu_memory_mb"`
	ComputeCapability *string `json:"compute_capability,omitempty"`
	CUDAVersion       *string `json:"cuda_version"`
	GPUCount          int     `json:"gpu_count"`
}

func UnavailableGPUInfo() GPUInfo { return GPUInfo{} }

func DetectHostGPU(ctx context.Context) GPUInfo {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nvidiaSMI := resolveNvidiaSMIExecutable()
	if nvidiaSMI == "" {
		return UnavailableGPUInfo()
	}
	output, err := exec.CommandContext(
		ctx, nvidiaSMI, "--query-gpu=name,memory.total,compute_cap", "--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		// Older NVIDIA-SMI releases do not advertise compute_cap as a query
		// field. Preserve the established GPU inventory rather than treating a
		// missing optional compatibility fact as an absent accelerator.
		output, err = exec.CommandContext(
			ctx, nvidiaSMI, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits",
		).Output()
	}
	if err != nil {
		return UnavailableGPUInfo()
	}
	info := parseNvidiaSMIQuery(string(output))
	if !info.Available {
		return info
	}
	if version := detectCUDAVersion(ctx, nvidiaSMI); version != "" {
		info.CUDAVersion = &version
	}
	return info
}

func resolveNvidiaSMIExecutable() string {
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		return path
	}
	info, err := os.Stat(wslNvidiaSMIPath)
	if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return wslNvidiaSMIPath
	}
	return ""
}

func parseNvidiaSMIQuery(output string) GPUInfo {
	var name string
	var memory int64
	var computeCapability string
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		candidateName := strings.TrimSpace(parts[0])
		value, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || candidateName == "" || value < 0 {
			continue
		}
		if count == 0 {
			name = candidateName
			memory = value
			if len(parts) >= 3 {
				computeCapability = normalizedComputeCapability(parts[2])
			}
		}
		count++
	}
	if count == 0 {
		return UnavailableGPUInfo()
	}
	info := GPUInfo{Available: true, GPUName: &name, GPUMemoryMB: &memory, GPUCount: count}
	if computeCapability != "" {
		info.ComputeCapability = &computeCapability
	}
	return info
}

func normalizedComputeCapability(value string) string {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return ""
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 1 || minor < 0 || minor > 99 {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}

func detectCUDAVersion(ctx context.Context, nvidiaSMI string) string {
	if output, err := exec.CommandContext(ctx, "nvcc", "--version").Output(); err == nil {
		if version := parseCUDAVersion(string(output)); version != "" {
			return version
		}
	}
	if nvidiaSMI != "" {
		if output, err := exec.CommandContext(ctx, nvidiaSMI).Output(); err == nil {
			return parseCUDAVersion(string(output))
		}
	}
	return ""
}

func parseCUDAVersion(output string) string {
	match := cudaVersionPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

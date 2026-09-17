package vmresources

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gibibyte = int64(1024 * 1024 * 1024)

type Limits struct {
	MaxMemoryGB      int
	HostCPUCount     int
	LaunchedMemoryGB int
	LaunchedCPUCount int
}

type Resources struct {
	MemoryGB         int    `json:"memoryGB"`
	CPUCount         int    `json:"cpuCount"`
	MaxMemoryGB      int    `json:"maxMemoryGB"`
	HostCPUCount     int    `json:"hostCpuCount"`
	LaunchedMemoryGB int    `json:"launchedMemoryGB"`
	LaunchedCPUCount int    `json:"launchedCpuCount"`
	IsRestarting     bool   `json:"isRestarting"`
	ConfigPath       string `json:"configPath"`
	Platform         string `json:"platform"`
}

type Controller struct {
	mu     sync.Mutex
	path   string
	limits Limits
}

func New(configPath string, limits Limits) *Controller {
	limits = normalizeLimits(limits)
	return &Controller{path: filepath.Clean(configPath), limits: limits}
}

func (c *Controller) Get() (Resources, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	memory, processors, err := c.readConfiguredLocked()
	if err != nil {
		return Resources{}, err
	}
	return c.resources(memory, processors), nil
}

func (c *Controller) Set(memoryGB, cpuCount int) (Resources, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if memoryGB < 1 || memoryGB > c.limits.MaxMemoryGB {
		return Resources{}, fmt.Errorf("memoryGB must be between 1 and %d", c.limits.MaxMemoryGB)
	}
	if cpuCount < 1 || cpuCount > c.limits.HostCPUCount {
		return Resources{}, fmt.Errorf("cpuCount must be between 1 and %d", c.limits.HostCPUCount)
	}
	raw, err := c.readRawLocked()
	if err != nil {
		return Resources{}, err
	}
	updated := updateWSL2Config(raw, memoryGB, cpuCount)
	if err := writeAtomic(c.path, []byte(updated)); err != nil {
		return Resources{}, err
	}
	return c.resources(memoryGB, cpuCount), nil
}

func (c *Controller) Path() string {
	return c.path
}

func (c *Controller) resources(memory, processors int) Resources {
	if memory < 1 {
		memory = c.limits.LaunchedMemoryGB
	}
	if processors < 1 {
		processors = c.limits.LaunchedCPUCount
	}
	platform := "local"
	if isWSL() {
		platform = "wsl2"
	}
	return Resources{
		MemoryGB: memory, CPUCount: processors,
		MaxMemoryGB: c.limits.MaxMemoryGB, HostCPUCount: c.limits.HostCPUCount,
		LaunchedMemoryGB: c.limits.LaunchedMemoryGB, LaunchedCPUCount: c.limits.LaunchedCPUCount,
		ConfigPath: c.path, Platform: platform,
	}
}

func (c *Controller) readConfiguredLocked() (int, int, error) {
	raw, err := c.readRawLocked()
	if err != nil {
		return 0, 0, err
	}
	return parseWSL2Config(raw)
}

func (c *Controller) readRawLocked() (string, error) {
	if strings.TrimSpace(c.path) == "" || !filepath.IsAbs(c.path) {
		return "", errors.New("VM resource config path must be absolute")
	}
	info, err := os.Lstat(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("VM resource config is not a regular file")
	}
	raw, err := os.ReadFile(c.path)
	return string(raw), err
}

func parseWSL2Config(raw string) (int, int, error) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	section := ""
	memory := 0
	processors := 0
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			continue
		}
		if section != "wsl2" || trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "memory":
			parsed, err := parseMemoryGB(strings.TrimSpace(value))
			if err != nil {
				return 0, 0, err
			}
			memory = parsed
		case "processors":
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 1 {
				return 0, 0, errors.New("invalid processors value in VM resource config")
			}
			processors = parsed
		}
	}
	return memory, processors, nil
}

func parseMemoryGB(value string) (int, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	multipliers := []struct {
		suffix string
		bytes  float64
	}{
		{"TB", 1024 * 1024 * 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"B", 1},
	}
	for _, multiplier := range multipliers {
		if !strings.HasSuffix(value, multiplier.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, multiplier.suffix))
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil || parsed <= 0 {
			return 0, errors.New("invalid memory value in VM resource config")
		}
		return max(1, int(math.Round(parsed*multiplier.bytes/float64(gibibyte)))), nil
	}
	return 0, errors.New("memory value must include KB, MB, GB, or TB")
}

func updateWSL2Config(raw string, memoryGB, cpuCount int) string {
	newline := "\n"
	if strings.Contains(raw, "\r\n") {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	hadTrailingNewline := strings.HasSuffix(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	if hadTrailingNewline {
		lines = lines[:len(lines)-1]
	}
	sectionStart, sectionEnd := -1, len(lines)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
			continue
		}
		section := strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
		if sectionStart >= 0 {
			sectionEnd = index
			break
		}
		if section == "wsl2" {
			sectionStart = index
		}
	}
	if sectionStart < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "[wsl2]", fmt.Sprintf("memory=%dGB", memoryGB), fmt.Sprintf("processors=%d", cpuCount))
	} else {
		memoryFound := false
		processorsFound := false
		for index := sectionStart + 1; index < sectionEnd; index++ {
			trimmed := strings.TrimSpace(lines[index])
			key, _, ok := strings.Cut(trimmed, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "memory":
				lines[index] = fmt.Sprintf("memory=%dGB", memoryGB)
				memoryFound = true
			case "processors":
				lines[index] = fmt.Sprintf("processors=%d", cpuCount)
				processorsFound = true
			}
		}
		insert := make([]string, 0, 2)
		if !memoryFound {
			insert = append(insert, fmt.Sprintf("memory=%dGB", memoryGB))
		}
		if !processorsFound {
			insert = append(insert, fmt.Sprintf("processors=%d", cpuCount))
		}
		if len(insert) > 0 {
			lines = append(lines[:sectionEnd], append(insert, lines[sectionEnd:]...)...)
		}
	}
	result := strings.Join(lines, newline)
	if hadTrailingNewline || result != "" {
		result += newline
	}
	return result
}

func writeAtomic(path string, value []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("VM resource config is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func normalizeLimits(limits Limits) Limits {
	if limits.LaunchedCPUCount < 1 {
		limits.LaunchedCPUCount = runtime.NumCPU()
	}
	if limits.HostCPUCount < limits.LaunchedCPUCount {
		limits.HostCPUCount = limits.LaunchedCPUCount
	}
	if limits.LaunchedMemoryGB < 1 {
		limits.LaunchedMemoryGB = 1
	}
	if limits.MaxMemoryGB < limits.LaunchedMemoryGB {
		limits.MaxMemoryGB = limits.LaunchedMemoryGB
	}
	return limits
}

func DetectLimits() Limits {
	limits := Limits{
		HostCPUCount: runtime.NumCPU(), LaunchedCPUCount: runtime.NumCPU(),
		LaunchedMemoryGB: detectLaunchedMemoryGB(),
	}
	limits.MaxMemoryGB = limits.LaunchedMemoryGB
	if isWSL() {
		if memory, cpus, ok := detectWindowsHostLimits(); ok {
			limits.MaxMemoryGB = max(limits.MaxMemoryGB, memory)
			limits.HostCPUCount = max(limits.HostCPUCount, cpus)
		}
	}
	return normalizeLimits(limits)
}

func detectLaunchedMemoryGB() int {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || value == "max" {
			continue
		}
		bytes, err := strconv.ParseInt(value, 10, 64)
		if err == nil && bytes > 0 && bytes < math.MaxInt64/2 {
			return max(1, int(math.Round(float64(bytes)/float64(gibibyte))))
		}
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "MemTotal:" {
				kilobytes, parseErr := strconv.ParseInt(fields[1], 10, 64)
				if parseErr == nil {
					return max(1, int(math.Round(float64(kilobytes*1024)/float64(gibibyte))))
				}
			}
		}
	}
	return 1
}

func detectWindowsHostLimits() (int, int, bool) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command",
		"[math]::Ceiling((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB); [Environment]::ProcessorCount")
	output, err := command.Output()
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 0, 0, false
	}
	memory, memoryErr := strconv.Atoi(fields[0])
	cpus, cpuErr := strconv.Atoi(fields[1])
	return memory, cpus, memoryErr == nil && cpuErr == nil && memory > 0 && cpus > 0
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft")
}

func DiscoverConfigPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("SYNON_WSL_CONFIG_PATH")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("SYNON_WSL_CONFIG_PATH must be absolute")
		}
		return filepath.Clean(configured), nil
	}
	if isWSL() {
		cmdPath, err := exec.LookPath("cmd.exe")
		if err == nil {
			output, commandErr := exec.Command(cmdPath, "/D", "/C", "echo", "%USERPROFILE%").Output()
			if commandErr == nil {
				windowsHome := strings.TrimSpace(string(output))
				if wslpath, lookupErr := exec.LookPath("wslpath"); lookupErr == nil {
					linuxHome, convertErr := exec.Command(wslpath, "-u", windowsHome).Output()
					if convertErr == nil {
						return filepath.Join(strings.TrimSpace(string(linuxHome)), ".wslconfig"), nil
					}
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".wslconfig"), nil
}

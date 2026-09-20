package runtimecontrol

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxCondaEnvironments = 256
	maxCondaScanWorkers  = 8
)

var condaEnvironmentName = regexp.MustCompile(`^[A-Za-z0-9_.@+-]+$`)

type UsageSection struct {
	TotalBytes *int64 `json:"totalBytes"`
}

type ArtifactUsageSection struct {
	TotalBytes *int64        `json:"totalBytes"`
	ByProject  []ProjectSize `json:"byProject"`
}

type ProjectSize struct {
	ProjectID string `json:"projectId"`
	Bytes     int64  `json:"bytes"`
}

type DiskUsage struct {
	Artifacts      ArtifactUsageSection `json:"artifacts"`
	Conda          UsageSection         `json:"conda"`
	Workspace      UsageSection         `json:"workspace"`
	ToolResults    UsageSection         `json:"toolResults"`
	Logs           UsageSection         `json:"logs"`
	Temp           UsageSection         `json:"temp"`
	ScannedAt      time.Time            `json:"scannedAt"`
	Accounting     string               `json:"accounting"`
	AvailableBytes *uint64              `json:"availableBytes"`
	Warnings       []string             `json:"warnings,omitempty"`
}

type CondaEnvironmentUsage struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type CondaDiskUsage struct {
	Envs       []CondaEnvironmentUsage `json:"envs"`
	PkgsBytes  int64                   `json:"pkgsBytes"`
	Truncated  bool                    `json:"truncated"`
	Warnings   []string                `json:"warnings,omitempty"`
	ScannedAt  time.Time               `json:"scannedAt"`
	Accounting string                  `json:"accounting"`
}

type StorageRoots struct {
	Artifacts, ToolResults, Logs, Temp string
}

type Scanner struct {
	root            string
	condaRoot       string
	condaEnvsRoot   string
	artifactsRoot   string
	toolResultsRoot string
	logsRoot        string
	tempRoot        string
	ttl             time.Duration
	now             func() time.Time

	diskMu  scanGate
	diskAt  time.Time
	disk    DiskUsage
	condaMu scanGate
	condaAt time.Time
	conda   CondaDiskUsage
}

func NewScanner(root, condaRoot string, ttl time.Duration) *Scanner {
	return NewScannerWithStorageRoots(root, condaRoot, filepath.Join(condaRoot, "envs"), filepath.Join(root, "artifacts"), filepath.Join(root, "tool-results"), ttl)
}

func NewScannerWithCondaEnvs(root, condaRoot, condaEnvsRoot string, ttl time.Duration) *Scanner {
	return NewScannerWithStorageRoots(root, condaRoot, condaEnvsRoot, filepath.Join(root, "artifacts"), filepath.Join(root, "tool-results"), ttl)
}

func NewScannerWithStorageRoots(root, condaRoot, condaEnvsRoot, artifactsRoot, toolResultsRoot string, ttl time.Duration) *Scanner {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	root = filepath.Clean(root)
	if strings.TrimSpace(artifactsRoot) == "" {
		artifactsRoot = filepath.Join(root, "artifacts")
	}
	if strings.TrimSpace(toolResultsRoot) == "" {
		toolResultsRoot = filepath.Join(root, "tool-results")
	}
	return &Scanner{
		root: root, condaRoot: filepath.Clean(condaRoot),
		condaEnvsRoot: filepath.Clean(condaEnvsRoot),
		artifactsRoot: filepath.Clean(artifactsRoot), toolResultsRoot: filepath.Clean(toolResultsRoot),
		logsRoot: filepath.Join(root, "shell_tasks"), tempRoot: filepath.Join(root, "tmp"),
		ttl: ttl, now: time.Now,
	}
}

func (s *Scanner) SetStorageRoots(roots StorageRoots) {
	if s == nil {
		return
	}
	_ = s.diskMu.lock(context.Background())
	defer s.diskMu.unlock()
	artifactsRoot, toolResultsRoot := roots.Artifacts, roots.ToolResults
	if strings.TrimSpace(artifactsRoot) == "" {
		artifactsRoot = filepath.Join(s.root, "artifacts")
	}
	if strings.TrimSpace(toolResultsRoot) == "" {
		toolResultsRoot = filepath.Join(s.root, "tool-results")
	}
	s.artifactsRoot = filepath.Clean(artifactsRoot)
	s.toolResultsRoot = filepath.Clean(toolResultsRoot)
	s.logsRoot, s.tempRoot = filepath.Join(s.root, "shell_tasks"), filepath.Join(s.root, "tmp")
	if roots.Logs != "" {
		s.logsRoot = filepath.Clean(roots.Logs)
	}
	if roots.Temp != "" {
		s.tempRoot = filepath.Clean(roots.Temp)
	}
	s.diskAt = time.Time{}
	s.disk = DiskUsage{}
}

func CondaRoot(root string) string {
	for _, key := range []string{"SYNON_CONDA_HOME", "MAMBA_ROOT_PREFIX"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			if filepath.IsAbs(value) {
				return filepath.Clean(value)
			}
		}
	}
	return filepath.Join(root, "conda")
}

func (s *Scanner) DiskUsage(ctx context.Context, refresh bool) (DiskUsage, error) {
	requestedAt := s.now()
	if err := s.diskMu.lock(ctx); err != nil {
		return DiskUsage{}, err
	}
	defer s.diskMu.unlock()
	now := s.now()
	if !s.diskAt.IsZero() && ((!refresh && now.Sub(s.diskAt) < s.ttl) || s.diskAt.After(requestedAt)) {
		return cloneDiskUsage(s.disk), nil
	}
	report := DiskUsage{Artifacts: ArtifactUsageSection{ByProject: []ProjectSize{}}}
	paths := []struct {
		paths []string
		set   func(int64)
	}{
		{[]string{s.artifactsRoot}, func(size int64) { report.Artifacts.TotalBytes = &size }},
		{[]string{s.condaRoot, s.condaEnvsRoot}, func(size int64) { report.Conda.TotalBytes = &size }},
		{[]string{filepath.Join(s.root, "workspace")}, func(size int64) { report.Workspace.TotalBytes = &size }},
		{[]string{s.toolResultsRoot}, func(size int64) { report.ToolResults.TotalBytes = &size }},
		{[]string{s.logsRoot}, func(size int64) { report.Logs.TotalBytes = &size }},
		{[]string{s.tempRoot}, func(size int64) { report.Temp.TotalBytes = &size }},
	}
	type scanResult struct {
		index int
		size  int64
		err   error
	}
	results := make(chan scanResult, len(paths))
	for index, item := range paths {
		go func(index int, roots []string) {
			size, err := scanDirectoryBytes(ctx, true, roots...)
			results <- scanResult{index: index, size: size, err: err}
		}(index, item.paths)
	}
	scans := make([]scanResult, len(paths))
	for range paths {
		result := <-results
		scans[result.index] = result
	}
	if err := ctx.Err(); err != nil {
		return DiskUsage{}, err
	}
	for index, item := range paths {
		if scans[index].err != nil {
			report.Warnings = append(report.Warnings, scans[index].err.Error())
			// A partial byte count is not a measured category total.
			continue
		}
		item.set(scans[index].size)
	}
	report.AvailableBytes = availableBytes(s.root)
	report.ScannedAt, report.Accounting = s.now().UTC(), usageAccounting
	s.diskAt, s.disk = report.ScannedAt, report
	return cloneDiskUsage(report), nil
}

func (s *Scanner) CondaDiskUsage(ctx context.Context, refresh bool) (CondaDiskUsage, error) {
	requestedAt := s.now()
	if err := s.condaMu.lock(ctx); err != nil {
		return CondaDiskUsage{}, err
	}
	defer s.condaMu.unlock()
	now := s.now()
	if !s.condaAt.IsZero() && ((!refresh && now.Sub(s.condaAt) < s.ttl) || s.condaAt.After(requestedAt)) {
		return cloneCondaDiskUsage(s.conda), nil
	}
	report := CondaDiskUsage{Envs: []CondaEnvironmentUsage{}}
	roots, warnings := environmentUsageRoots(s.condaEnvsRoot)
	report.Warnings = append(report.Warnings, warnings...)
	names, truncated := boundedEnvironmentNames(roots)
	report.Truncated = truncated
	type scanTarget struct {
		name  string
		paths []string
	}
	targets := make([]scanTarget, 0, len(names)+1)
	for _, name := range names {
		targets = append(targets, scanTarget{name: name, paths: roots[name]})
	}
	targets = append(targets, scanTarget{paths: []string{filepath.Join(s.condaRoot, "pkgs")}})
	type scanResult struct {
		size int64
		err  error
	}
	results := make([]scanResult, len(targets))
	jobs := make(chan int)
	workerCount := min(maxCondaScanWorkers, len(targets))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index].size, results[index].err = scanDirectoryBytes(ctx, true, targets[index].paths...)
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return CondaDiskUsage{}, err
	}
	for index, target := range targets {
		if results[index].err != nil {
			report.Warnings = append(report.Warnings, results[index].err.Error())
		}
		if target.name == "" {
			report.PkgsBytes = results[index].size
			continue
		}
		report.Envs = append(report.Envs, CondaEnvironmentUsage{Name: target.name, Bytes: results[index].size})
	}
	sort.Slice(report.Envs, func(i, j int) bool {
		if report.Envs[i].Bytes == report.Envs[j].Bytes {
			return report.Envs[i].Name < report.Envs[j].Name
		}
		return report.Envs[i].Bytes > report.Envs[j].Bytes
	})
	report.ScannedAt, report.Accounting = s.now().UTC(), usageAccounting
	s.condaAt, s.conda = report.ScannedAt, report
	return cloneCondaDiskUsage(report), nil
}

func cloneDiskUsage(value DiskUsage) DiskUsage {
	for _, total := range []**int64{&value.Artifacts.TotalBytes, &value.Conda.TotalBytes,
		&value.Workspace.TotalBytes, &value.ToolResults.TotalBytes, &value.Logs.TotalBytes, &value.Temp.TotalBytes} {
		if *total != nil {
			copied := **total
			*total = &copied
		}
	}
	value.Artifacts.ByProject = append([]ProjectSize{}, value.Artifacts.ByProject...)
	value.Warnings = append([]string(nil), value.Warnings...)
	if value.AvailableBytes != nil {
		available := *value.AvailableBytes
		value.AvailableBytes = &available
	}
	return value
}

func cloneCondaDiskUsage(value CondaDiskUsage) CondaDiskUsage {
	value.Envs = append([]CondaEnvironmentUsage{}, value.Envs...)
	value.Warnings = append([]string(nil), value.Warnings...)
	return value
}

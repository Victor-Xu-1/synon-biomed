package runtimecontrol

import (
	"errors"
	"io/fs"
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
	TotalBytes int64 `json:"totalBytes"`
}

type ArtifactUsageSection struct {
	TotalBytes int64         `json:"totalBytes"`
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
	AvailableBytes *uint64              `json:"availableBytes"`
	Warnings       []string             `json:"warnings,omitempty"`
}

type CondaEnvironmentUsage struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type CondaDiskUsage struct {
	Envs      []CondaEnvironmentUsage `json:"envs"`
	PkgsBytes int64                   `json:"pkgsBytes"`
	Truncated bool                    `json:"truncated"`
	Warnings  []string                `json:"warnings,omitempty"`
}

type Scanner struct {
	root            string
	condaRoot       string
	condaEnvsRoot   string
	artifactsRoot   string
	toolResultsRoot string
	ttl             time.Duration
	now             func() time.Time

	diskMu  sync.Mutex
	diskAt  time.Time
	disk    DiskUsage
	condaMu sync.Mutex
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
		ttl: ttl, now: time.Now,
	}
}

func (s *Scanner) SetStorageRoots(artifactsRoot, toolResultsRoot string) {
	if s == nil {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if strings.TrimSpace(artifactsRoot) == "" {
		artifactsRoot = filepath.Join(s.root, "artifacts")
	}
	if strings.TrimSpace(toolResultsRoot) == "" {
		toolResultsRoot = filepath.Join(s.root, "tool-results")
	}
	s.artifactsRoot = filepath.Clean(artifactsRoot)
	s.toolResultsRoot = filepath.Clean(toolResultsRoot)
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

func (s *Scanner) DiskUsage() DiskUsage {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	now := s.now()
	if !s.diskAt.IsZero() && now.Sub(s.diskAt) < s.ttl {
		return cloneDiskUsage(s.disk)
	}
	report := DiskUsage{Artifacts: ArtifactUsageSection{ByProject: []ProjectSize{}}}
	paths := []struct {
		path string
		set  func(int64)
	}{
		{s.artifactsRoot, func(size int64) { report.Artifacts.TotalBytes = size }},
		{s.condaRoot, func(size int64) { report.Conda.TotalBytes = size }},
		{filepath.Join(s.root, "workspace"), func(size int64) { report.Workspace.TotalBytes = size }},
		{s.toolResultsRoot, func(size int64) { report.ToolResults.TotalBytes = size }},
	}
	type scanResult struct {
		index int
		size  int64
		err   error
	}
	results := make(chan scanResult, len(paths))
	for index, item := range paths {
		go func(index int, path string) {
			size, err := directoryBytes(path)
			results <- scanResult{index: index, size: size, err: err}
		}(index, item.path)
	}
	scans := make([]scanResult, len(paths))
	for range paths {
		result := <-results
		scans[result.index] = result
	}
	for index, item := range paths {
		if scans[index].err != nil {
			report.Warnings = append(report.Warnings, scans[index].err.Error())
		}
		item.set(scans[index].size)
	}
	report.AvailableBytes = availableBytes(s.root)
	s.diskAt, s.disk = now, report
	return cloneDiskUsage(report)
}

func (s *Scanner) CondaDiskUsage() CondaDiskUsage {
	s.condaMu.Lock()
	defer s.condaMu.Unlock()
	now := s.now()
	if !s.condaAt.IsZero() && now.Sub(s.condaAt) < s.ttl {
		return cloneCondaDiskUsage(s.conda)
	}
	report := CondaDiskUsage{Envs: []CondaEnvironmentUsage{}}
	entries, err := os.ReadDir(s.condaEnvsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		report.Warnings = append(report.Warnings, err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && condaEnvironmentName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) > maxCondaEnvironments {
		report.Truncated = true
		names = names[:maxCondaEnvironments]
	}
	type scanTarget struct {
		name string
		path string
	}
	targets := make([]scanTarget, 0, len(names)+1)
	for _, name := range names {
		targets = append(targets, scanTarget{name: name, path: filepath.Join(s.condaEnvsRoot, name)})
	}
	targets = append(targets, scanTarget{path: filepath.Join(s.condaRoot, "pkgs")})
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
				results[index].size, results[index].err = directoryBytes(targets[index].path)
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
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
	s.condaAt, s.conda = now, report
	return cloneCondaDiskUsage(report)
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

func cloneDiskUsage(value DiskUsage) DiskUsage {
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

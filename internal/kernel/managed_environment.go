package kernel

import (
	"context"

	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"sort"

	"strings"
	"time"
)

const (
	managedEnvironmentMarkerName      = ".synon-managed-environment.json"
	managedEnvironmentMarkerVersion   = 2
	managedEnvironmentLegacyVersion   = 1
	defaultManagedPythonVersion       = "3.11"
	maxManagedEnvironmentInputBytes   = 64 * 1024
	maxManagedEnvironmentPackages     = 256
	maxManagedEnvironmentListBytes    = 32 * 1024 * 1024
	maxManagedLockedRequirementsBytes = 2 * 1024 * 1024
	maxManagedEnvironmentGenerations  = 4096
	managedEnvironmentHealthTimeout   = 20 * time.Second
)

var (
	// Conda package names may begin with an underscore (for example,
	// `_openmp_mutex`). Keep the boundary strict against separators and
	// control characters without rejecting valid solver-generated records.
	managedPackageName   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	managedPackageSpec   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(?:\[[A-Za-z0-9_,.-]+\])?(?:\s*(?:==|!=|~=|>=|<=|>|<|=)\s*[A-Za-z0-9*+_.!:-]+(?:\s*,\s*(?:==|!=|~=|>=|<=|>|<|=)\s*[A-Za-z0-9*+_.!:-]+)*)?$`)
	managedImportName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	managedPythonVersion = regexp.MustCompile(`^3\.[0-9]{1,2}$`)
)

// ManagedEnvironment is the public, bounded read model for an immutable
// environment generation. Root paths and installer output are deliberately
// excluded so an agent cannot turn discovery into host filesystem authority.
type ManagedEnvironment struct {
	Name       string   `json:"name"`
	Language   string   `json:"language"`
	Kind       string   `json:"kind"`
	Generation string   `json:"generation"`
	SpecDigest string   `json:"spec_digest,omitempty"`
	Packages   []string `json:"packages,omitempty"`
	Status     string   `json:"status"`
}

type ManagedEnvironmentQuery struct {
	// Name narrows inventory to one exact environment before any marker or
	// health work. Callers that already have a model-selected environment must
	// not scan the whole managed-environment catalog and then lose it to a
	// short preflight deadline.
	Name            string
	Language        string
	Dependencies    []string
	IncludePackages bool
	// RequiredAccelerator filters out environments whose resolved package
	// inventory explicitly identifies a CPU-only compute stack. Unknown stacks
	// remain discoverable; a concrete workload witness is still the final
	// readiness authority.
	RequiredAccelerator string
	// SkipHealth is an explicit fast-inventory mode for compatibility
	// selection. The normal inventory API keeps its strict health-check default;
	// the selected candidate is verified immediately before execution. This
	// prevents a large local catalog from consuming a legitimate tool timeout
	// while probing unrelated environments.
	SkipHealth bool
}

type CreateManagedEnvironmentInput struct {
	// RequireAbsent is an admission precondition for an explicitly named fork.
	// It is checked under the publication lock after idempotent receipt recovery.
	RequireAbsent            bool
	Name                     string
	Language                 string
	PythonVersion            string
	SourceEnvironment        string
	Packages                 []string
	Channels                 []string
	PipPhases                [][]string
	PipArgs                  []string
	PipFindLinks             []string
	PipExtraIndexURLs        []string
	ImportNames              []string
	RequiredAccelerator      string
	LockedRequirementsPath   string
	LockedRequirementsSHA256 string
	OperationID              string
}

type MutateManagedPackagesInput struct {
	Environment         string
	Packages            []string
	Channels            []string
	UsePip              bool
	PipArgs             []string
	PipFindLinks        []string
	PipExtraIndexURLs   []string
	RequiredAccelerator string
	ForkTo              string
	OperationID         string
}

type RegisterManagedEnvironmentInput struct {
	Name        string
	Language    string
	SourcePath  string
	VenvPath    string
	Create      bool
	Extras      []string
	Force       bool
	OperationID string
}

type DeleteManagedEnvironmentInput struct {
	Name        string
	OperationID string
	// Empty means the name was absent when admitted. Never deactivate a
	// successor generation created after this operation was admitted.
	ExpectedGeneration string
}

type managedEnvironmentMarker struct {
	ValidationRevision  int                     `json:"validationRevision,omitempty"`
	SchemaVersion       int                     `json:"schemaVersion"`
	Name                string                  `json:"name"`
	Language            string                  `json:"language"`
	Generation          string                  `json:"generation"`
	Packages            []string                `json:"packages"`
	Channels            []string                `json:"channels,omitempty"`
	CreatedAt           string                  `json:"createdAt"`
	Operation           string                  `json:"operation"`
	Kind                string                  `json:"kind,omitempty"`
	SourcePath          string                  `json:"sourcePath,omitempty"`
	RuntimePath         string                  `json:"runtimePath,omitempty"`
	OperationKey        string                  `json:"operationKey,omitempty"`
	SpecDigest          string                  `json:"specDigest,omitempty"`
	ImportNames         []string                `json:"importNames,omitempty"`
	PipReplay           []managedPipReplayPhase `json:"pipReplay,omitempty"`
	PipReplayRevision   int                     `json:"pipReplayRevision,omitempty"`
	PipReplayBaseDigest string                  `json:"pipReplayBaseDigest,omitempty"`
}

type micromambaPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build_string"`
	Channel string `json:"channel"`
}

// ListManagedEnvironments returns only atomically activated, verified
// generations. Broken pointers and unmarked host environments fail closed and
// are never advertised as execution capabilities.
func (m *Manager) ListManagedEnvironments(ctx context.Context, query ManagedEnvironmentQuery) ([]ManagedEnvironment, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	language, err := validateManagedLanguage(query.Language, true)
	if err != nil {
		return nil, err
	}
	dependencies, err := validateManagedPackageSpecs(query.Dependencies)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(query.Name)
	if name != "" && !ValidEnvironmentName(name) {
		return nil, errors.New("managed environment query name is invalid")
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, errors.New("managed environment root is unavailable")
	}
	result := make([]ManagedEnvironment, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ValidEnvironmentName(entry.Name()) || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if name != "" && entry.Name() != name {
			continue
		}
		// Dependency filtering needs the immutable marker's package inventory
		// even when the caller does not request that potentially large list in
		// the response. Filter first, then project packages away.
		environment, err := m.readManagedEnvironment(entry.Name(), query.IncludePackages || len(dependencies) > 0)
		if err != nil || environment.Status != "ready" || language != "" && environment.Language != language || !managedDependenciesSatisfied(environment.Packages, dependencies) {
			continue
		}
		if err := validateManagedRequiredAccelerator(environment.Packages, query.RequiredAccelerator); err != nil {
			continue
		}
		if !query.SkipHealth {
			if _, found, healthErr := m.ManagedEnvironmentActiveGeneration(entry.Name()); healthErr != nil || !found {
				continue
			}
		}
		if !query.IncludePackages {
			environment.Packages = nil
		}
		result = append(result, environment)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// InspectManagedEnvironment is the read-only preflight for task-requested
// software. It proves that the exact content-addressed environment is active
// and healthy before any installer is allowed to run. A missing environment is
// reported separately from a present but invalid environment so providers do
// not hide corruption by silently installing over it.
func (m *Manager) InspectManagedEnvironment(ctx context.Context, name string) (ManagedEnvironment, bool, error) {
	if m == nil {
		return ManagedEnvironment{}, false, errors.New("kernel manager is not configured")
	}
	if err := ctx.Err(); err != nil {
		return ManagedEnvironment{}, false, err
	}
	if err := validateManagedEnvironmentName(name); err != nil {
		return ManagedEnvironment{}, false, err
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return ManagedEnvironment{}, false, err
	}
	active := filepath.Join(root, name)
	if _, err := os.Lstat(active); errors.Is(err, os.ErrNotExist) {
		return ManagedEnvironment{}, false, nil
	} else if err != nil {
		return ManagedEnvironment{}, false, errors.New("managed environment preflight is unavailable")
	}
	generation, found, err := m.ManagedEnvironmentActiveGeneration(name)
	if err != nil {
		return ManagedEnvironment{}, true, fmt.Errorf("managed environment preflight failed: %w", err)
	}
	if !found {
		return ManagedEnvironment{}, true, errors.New("managed environment preflight found an invalid active installation")
	}
	environment, err := m.readManagedEnvironment(name, true)
	if err != nil || environment.Generation != generation || environment.Status != "ready" {
		return ManagedEnvironment{}, true, errors.New("managed environment preflight receipt is invalid")
	}
	return environment, true, nil
}

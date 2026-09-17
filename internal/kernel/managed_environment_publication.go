package kernel

import (
	"context"

	"errors"
	"fmt"

	"os"
	"path/filepath"

	"strings"
	"time"
)

// DeleteManagedEnvironment atomically removes only the active pointer. The
// immutable generation remains available to running or durable tasks and can
// be garbage-collected only by a later reference-aware maintenance pass.
func (m *Manager) DeleteManagedEnvironment(ctx context.Context, name string) error {
	if err := validateManagedEnvironmentName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return err
	}
	lockPath := filepath.Join(root, ".generations", name, ".install.lock")
	release, err := lockKernelFile(ctx, lockPath)
	if err != nil {
		return errors.New("managed environment is busy")
	}
	defer release()
	active := filepath.Join(root, name)
	info, err := os.Lstat(active)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("managed environment active pointer is invalid")
	}
	if err := os.Remove(active); err != nil {
		return errors.New("managed environment could not be deactivated")
	}
	m.notifyRuntimeChange()
	return nil
}

func (m *Manager) publishManagedEnvironment(
	ctx context.Context,
	name, language, operation, operationKey string,
	channels []string,
	specDigest string,
	importNames []string,
	validateResolved func([]string) error,
	install func(string) error,
) (ManagedEnvironment, error) {
	const totalMilestones int64 = 8
	if m == nil || strings.TrimSpace(m.config.Micromamba) == "" {
		return ManagedEnvironment{}, errors.New("managed environment installer is unavailable")
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return ManagedEnvironment{}, err
	}
	// One host-wide installer admission protects interactive responsiveness
	// across different environment names and across service/executor processes.
	// Individual installers still use bounded native threads; serializing the
	// rare mutation phase prevents several large solvers, extractors, or source
	// builds from multiplying that budget. Verified environments are reused by
	// the preflight path and do not acquire this lock.
	reportManagedEnvironmentMilestone(ctx, "waiting_for_installer", 0, totalMilestones)
	releaseInstaller, err := lockKernelFile(ctx, filepath.Join(root, ".installer.lock"))
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment installer admission was cancelled")
	}
	defer releaseInstaller()
	reportManagedEnvironmentMilestone(ctx, "preparing_environment", 1, totalMilestones)
	generationRoot := filepath.Join(root, ".generations", name)
	if err := os.MkdirAll(generationRoot, 0o700); err != nil {
		return ManagedEnvironment{}, errors.New("managed environment generation root cannot be created")
	}
	release, err := lockKernelFile(ctx, filepath.Join(generationRoot, ".install.lock"))
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment is busy")
	}
	defer release()
	if recovered, found, err := m.findManagedEnvironmentOperationGeneration(ctx, generationRoot, operationKey); err != nil {
		return ManagedEnvironment{}, err
	} else if found {
		reportManagedEnvironmentMilestone(ctx, "activating_environment", 7, totalMilestones)
		if err := activateManagedEnvironment(root, name, recovered.path); err != nil {
			return ManagedEnvironment{}, err
		}
		m.notifyRuntimeChange()
		reportManagedEnvironmentMilestone(ctx, "environment_ready", totalMilestones, totalMilestones)
		return recovered.environment, nil
	}
	staging, err := os.MkdirTemp(generationRoot, ".staging-")
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment staging directory cannot be created")
	}
	// Micromamba treats an existing empty -p target as an invalid prefix on
	// some versions. Keep the collision-resistant name, but let the installer
	// create the directory itself.
	if err := os.Remove(staging); err != nil {
		return ManagedEnvironment{}, errors.New("managed environment staging directory cannot be prepared")
	}
	defer os.RemoveAll(staging)
	reportManagedEnvironmentMilestone(ctx, "installing_dependencies", 1, totalMilestones)
	if err := install(staging); err != nil {
		return ManagedEnvironment{}, err
	}
	reportManagedEnvironmentMilestone(ctx, "dependencies_installed", 2, totalMilestones)
	reportManagedEnvironmentMilestone(ctx, "inspecting_environment", 2, totalMilestones)
	packages, err := m.inspectManagedEnvironmentPackages(ctx, staging)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if validateResolved != nil {
		if err := validateResolved(packages); err != nil {
			return ManagedEnvironment{}, err
		}
	}
	reportManagedEnvironmentMilestone(ctx, "dependency_inventory_verified", 3, totalMilestones)
	reportManagedEnvironmentMilestone(ctx, "validating_environment", 3, totalMilestones)
	if err := validateManagedEnvironmentPublication(ctx, language, staging, packages, importNames); err != nil {
		return ManagedEnvironment{}, err
	}
	reportManagedEnvironmentMilestone(ctx, "staging_environment_validated", 4, totalMilestones)
	generation := managedEnvironmentGeneration(name, language, packages, specDigest)
	marker := managedEnvironmentMarker{
		SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: language,
		Generation: generation, Packages: packages, Channels: append([]string(nil), channels...),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Operation: operation,
		Kind: "conda", OperationKey: operationKey, SpecDigest: specDigest,
		ImportNames: append([]string(nil), importNames...),
	}
	generationPath := filepath.Join(generationRoot, generation)
	if _, err := os.Stat(generationPath); errors.Is(err, os.ErrNotExist) {
		// Conda package linking performs prefix relocation against the exact
		// target passed to the installer. Renaming a completed prefix leaves
		// absolute paths embedded in launchers such as Rscript pointing at the
		// deleted staging directory. Resolve in staging to obtain the immutable
		// content identity, then install the same admitted transaction once more
		// at that final content-addressed path. The package cache makes this a
		// local relink for conda packages; pip phases are deliberately replayed
		// so untracked distributions are preserved as part of the generation.
		published := false
		defer func() {
			if !published {
				_ = os.RemoveAll(generationPath)
			}
		}()
		reportManagedEnvironmentMilestone(ctx, "materializing_generation", 4, totalMilestones)
		if err := install(generationPath); err != nil {
			return ManagedEnvironment{}, err
		}
		reportManagedEnvironmentMilestone(ctx, "generation_materialized", 5, totalMilestones)
		reportManagedEnvironmentMilestone(ctx, "validating_generation", 5, totalMilestones)
		publishedPackages, err := m.inspectManagedEnvironmentPackages(ctx, generationPath)
		if err != nil {
			return ManagedEnvironment{}, err
		}
		if strings.Join(publishedPackages, "\x00") != strings.Join(packages, "\x00") {
			return ManagedEnvironment{}, errors.New("managed environment resolution changed before final publication")
		}
		if validateResolved != nil {
			if err := validateResolved(publishedPackages); err != nil {
				return ManagedEnvironment{}, err
			}
		}
		if err := validateManagedEnvironmentPublication(ctx, language, generationPath, publishedPackages, importNames); err != nil {
			return ManagedEnvironment{}, err
		}
		reportManagedEnvironmentMilestone(ctx, "generation_validated", 6, totalMilestones)
		reportManagedEnvironmentMilestone(ctx, "publishing_environment", 6, totalMilestones)
		if err := writeManagedEnvironmentMarker(filepath.Join(generationPath, managedEnvironmentMarkerName), marker); err != nil {
			return ManagedEnvironment{}, errors.New("managed environment marker could not be written")
		}
		if err := syncDirectory(generationRoot); err != nil {
			return ManagedEnvironment{}, errors.New("managed environment generation could not be durably published")
		}
		published = true
		reportManagedEnvironmentMilestone(ctx, "environment_published", 7, totalMilestones)
	} else if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment generation cannot be inspected")
	} else {
		reportManagedEnvironmentMilestone(ctx, "validating_generation", 5, totalMilestones)
		existing, readErr := readManagedEnvironmentMarker(generationPath)
		if readErr != nil || !managedEnvironmentGenerationEquivalent(existing, marker) {
			return ManagedEnvironment{}, errors.New("managed environment generation conflicts with durable state")
		}
		if err := validateManagedEnvironmentPublication(ctx, existing.Language, generationPath, existing.Packages, existing.ImportNames); err != nil {
			return ManagedEnvironment{}, fmt.Errorf("existing managed environment generation is unhealthy: %w", err)
		}
		reportManagedEnvironmentMilestone(ctx, "generation_validated", 6, totalMilestones)
		reportManagedEnvironmentMilestone(ctx, "environment_published", 7, totalMilestones)
	}
	reportManagedEnvironmentMilestone(ctx, "activating_environment", 7, totalMilestones)
	if err := activateManagedEnvironment(root, name, generationPath); err != nil {
		return ManagedEnvironment{}, err
	}
	m.notifyRuntimeChange()
	reportManagedEnvironmentMilestone(ctx, "environment_ready", totalMilestones, totalMilestones)
	return ManagedEnvironment{Name: name, Language: language, Kind: "conda", Generation: generation, SpecDigest: specDigest, Packages: packages, Status: "ready"}, nil
}

func (m *Manager) publishRegisteredManagedEnvironment(ctx context.Context, name, language, sourcePath, venvPath, operationKey string) (ManagedEnvironment, error) {
	const totalMilestones int64 = 4
	reportManagedEnvironmentMilestone(ctx, "inspecting_environment", 0, totalMilestones)
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return ManagedEnvironment{}, err
	}
	generationRoot := filepath.Join(root, ".generations", name)
	if err := os.MkdirAll(generationRoot, 0o700); err != nil {
		return ManagedEnvironment{}, errors.New("registered environment generation root cannot be created")
	}
	release, err := lockKernelFile(ctx, filepath.Join(generationRoot, ".install.lock"))
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment is busy")
	}
	defer release()
	if recovered, found, err := m.findManagedEnvironmentOperationGeneration(ctx, generationRoot, operationKey); err != nil {
		return ManagedEnvironment{}, err
	} else if found {
		reportManagedEnvironmentMilestone(ctx, "activating_environment", 3, totalMilestones)
		if err := activateManagedEnvironment(root, name, recovered.path); err != nil {
			return ManagedEnvironment{}, err
		}
		m.notifyRuntimeChange()
		reportManagedEnvironmentMilestone(ctx, "environment_ready", totalMilestones, totalMilestones)
		return recovered.environment, nil
	}
	packages, err := inspectRegisteredPythonPackages(ctx, venvPath)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	reportManagedEnvironmentMilestone(ctx, "dependency_inventory_verified", 1, totalMilestones)
	reportManagedEnvironmentMilestone(ctx, "validating_environment", 1, totalMilestones)
	if err := validateManagedEnvironmentPublication(ctx, language, venvPath, packages, nil); err != nil {
		return ManagedEnvironment{}, err
	}
	reportManagedEnvironmentMilestone(ctx, "staging_environment_validated", 2, totalMilestones)
	reportManagedEnvironmentMilestone(ctx, "publishing_environment", 2, totalMilestones)
	generation := managedRegisteredEnvironmentGeneration(name, language, sourcePath, venvPath, packages)
	marker := managedEnvironmentMarker{
		SchemaVersion: managedEnvironmentMarkerVersion, Name: name, Language: language,
		Generation: generation, Packages: packages, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Operation: "register", Kind: "path-venv", SourcePath: sourcePath, RuntimePath: venvPath, OperationKey: operationKey,
	}
	generationPath := filepath.Join(generationRoot, generation)
	if _, err := os.Stat(generationPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(generationPath, 0o700); err != nil {
			return ManagedEnvironment{}, errors.New("registered environment generation could not be created")
		}
		if err := writeManagedEnvironmentMarker(filepath.Join(generationPath, managedEnvironmentMarkerName), marker); err != nil {
			_ = os.RemoveAll(generationPath)
			return ManagedEnvironment{}, errors.New("registered environment marker could not be written")
		}
		if err := syncDirectory(generationRoot); err != nil {
			return ManagedEnvironment{}, errors.New("registered environment generation could not be durably published")
		}
	} else if err != nil {
		return ManagedEnvironment{}, errors.New("registered environment generation cannot be inspected")
	} else if existing, readErr := readManagedEnvironmentMarker(generationPath); readErr != nil || !managedEnvironmentMarkerEquivalent(existing, marker) {
		return ManagedEnvironment{}, errors.New("registered environment generation conflicts with durable state")
	}
	reportManagedEnvironmentMilestone(ctx, "environment_published", 3, totalMilestones)
	reportManagedEnvironmentMilestone(ctx, "activating_environment", 3, totalMilestones)
	if err := activateManagedEnvironment(root, name, generationPath); err != nil {
		return ManagedEnvironment{}, err
	}
	m.notifyRuntimeChange()
	reportManagedEnvironmentMilestone(ctx, "environment_ready", totalMilestones, totalMilestones)
	return ManagedEnvironment{Name: name, Language: language, Kind: "path-venv", Generation: generation, Packages: packages, Status: "ready"}, nil
}

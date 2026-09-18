package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"errors"
	"fmt"

	"os"
	"path/filepath"
	"regexp"

	"strings"
)

func (m *Manager) CreateManagedEnvironment(ctx context.Context, input CreateManagedEnvironmentInput) (ManagedEnvironment, error) {
	if err := validateManagedEnvironmentName(input.Name); err != nil {
		return ManagedEnvironment{}, err
	}
	language, err := validateManagedLanguage(input.Language, false)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if language == "" {
		language = "python"
	}
	sourceEnvironment := strings.TrimSpace(input.SourceEnvironment)
	if sourceEnvironment != "" {
		if err := validateManagedEnvironmentName(sourceEnvironment); err != nil {
			return ManagedEnvironment{}, errors.New("source_environment is invalid")
		}
		if language != "python" || sourceEnvironment != m.ManagedPythonEnvironmentName() {
			return ManagedEnvironment{}, errors.New("source_environment must be the verified bundled Python runtime")
		}
	}
	packages, channels, pipPhases, err := normalizeManagedEnvironmentCreateInput(input.Packages, input.Channels, input.PipPhases)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipArgs, err := validateManagedPipArguments(input.PipArgs)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipFindLinks, err := validateManagedPipSourceURLs(input.PipFindLinks)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipExtraIndexURLs, err := validateManagedPipSourceURLs(input.PipExtraIndexURLs)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if err := validateManagedRequiredAccelerator(nil, input.RequiredAccelerator); err != nil {
		return ManagedEnvironment{}, err
	}
	importNames, err := validateManagedImportNames(input.ImportNames)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	lockedRequirementsPath, lockedRequirementsSHA256, err := validateManagedLockedRequirements(
		input.LockedRequirementsPath, input.LockedRequirementsSHA256,
	)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if lockedRequirementsPath != "" && len(pipPhases) > 0 {
		return ManagedEnvironment{}, errors.New("locked requirements cannot be combined with pip phases")
	}
	pipPlan := planManagedPipInstall(pipPhases, pipArgs, pipFindLinks, pipExtraIndexURLs, lockedRequirementsPath)
	arguments := []string{"--no-rc", "create", "-y"}
	if language == "python" || len(pipPlan) != 0 {
		if sourceEnvironment == "" {
			version := strings.TrimSpace(input.PythonVersion)
			if version == "" {
				version = defaultManagedPythonVersion
			}
			if !regexp.MustCompile(`^3\.[0-9]{1,2}(?:\.[0-9]{1,2})?$`).MatchString(version) {
				return ManagedEnvironment{}, errors.New("python_version must be a bounded Python 3 version")
			}
			// python_version is the canonical interpreter contract. A redundant
			// broad package spec such as python>=3.8 must not let the solver select
			// a newer incompatible runtime.
			packages = replaceManagedPackageRequirement(packages, "python", "python="+version)
			if !managedPackageSetContains(packages, "pip") {
				packages = append(packages, "pip")
			}
		}
	}
	if language == "r" {
		if !managedPackageSetContains(packages, "r-base") {
			packages = append([]string{"r-base"}, packages...)
		}
		if !managedPackageSetContains(packages, "r-jsonlite") {
			packages = append(packages, "r-jsonlite")
		}
		if !managedStringSetContains(channels, "bioconda") {
			channels = append(channels, "bioconda")
		}
	}
	specDigest := managedEnvironmentSpecDigest(pipPhases, importNames)
	if len(pipFindLinks) != 0 || len(pipExtraIndexURLs) != 0 || strings.TrimSpace(input.RequiredAccelerator) != "" {
		digest := sha256.Sum256([]byte(strings.Join([]string{
			specDigest,
			strings.Join(pipFindLinks, "\x00"),
			strings.Join(pipExtraIndexURLs, "\x00"),
			strings.ToLower(strings.TrimSpace(input.RequiredAccelerator)),
		}, "\x00pip-sources\x00")))
		specDigest = hex.EncodeToString(digest[:])
	}
	if lockedRequirementsSHA256 != "" {
		digest := sha256.Sum256([]byte(specDigest + "\x00locked-requirements\x00" + lockedRequirementsSHA256))
		specDigest = hex.EncodeToString(digest[:])
	}
	// Creation is an immutable, content-addressed publication. Coalesce and
	// recover by the complete normalized environment definition rather than by
	// an individual tool-call ID, so identical software requirements across
	// tasks reuse the same verified generation without rerunning installers.
	operationKey := managedEnvironmentOperationKey("create", struct {
		Name, Language, SourceEnvironment string
		Packages                          []string
		Channels                          []string
		PipPhases                         [][]string
		PipArgs                           []string
		PipFindLinks                      []string
		PipExtraIndexURLs                 []string
		ImportNames                       []string
		RequiredAccelerator               string
		LockedRequirementsSHA256          string
	}{
		input.Name, language, sourceEnvironment, packages, channels, pipPhases, pipArgs,
		pipFindLinks, pipExtraIndexURLs, importNames, strings.ToLower(strings.TrimSpace(input.RequiredAccelerator)),
		lockedRequirementsSHA256,
	})
	return m.runManagedEnvironmentOperation(ctx, operationKey, func(operationContext context.Context) (ManagedEnvironment, error) {
		sourcePrefix := ""
		if sourceEnvironment != "" {
			var sourceErr error
			sourcePrefix, sourceErr = m.ManagedPythonActivePrefix()
			if sourceErr != nil {
				return ManagedEnvironment{}, fmt.Errorf("source managed environment is not ready: %w", sourceErr)
			}
		}
		validateResolved := func(resolved []string) error {
			return validateManagedRequiredAccelerator(resolved, input.RequiredAccelerator)
		}
		return m.publishManagedEnvironment(operationContext, input.Name, language, "create", operationKey, channels, specDigest, input.RequireAbsent, importNames, validateResolved, func(staging string) error {
			commandArguments := []string{}
			if sourcePrefix != "" {
				commandArguments = []string{"--no-rc", "create", "-y", "-p", staging, "--clone", sourcePrefix}
			} else {
				commandArguments = append(append(append(arguments, "-p", staging), managedChannelArguments(channels)...), packages...)
			}
			if err := m.runManagedEnvironmentCommand(operationContext, commandArguments...); err != nil {
				return err
			}
			if sourcePrefix != "" && len(packages) != 0 {
				installArguments := append([]string{"--no-rc", "install", "-y", "-p", staging}, managedChannelArguments(channels)...)
				installArguments = append(installArguments, packages...)
				if err := m.runManagedEnvironmentCommand(operationContext, installArguments...); err != nil {
					return err
				}
			}
			return m.runManagedPipInstallPlan(operationContext, staging, pipPlan)
		})
	})
}

func replaceManagedPackageRequirement(packages []string, name, replacement string) []string {
	result := make([]string, 0, len(packages)+1)
	result = append(result, replacement)
	for _, item := range packages {
		if managedPackageRequirementName(strings.TrimSpace(item)) == strings.ToLower(strings.TrimSpace(name)) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func (m *Manager) InstallManagedPackages(ctx context.Context, input MutateManagedPackagesInput) (ManagedEnvironment, error) {
	return m.mutateManagedPackages(ctx, "install", input)
}

func (m *Manager) UninstallManagedPackages(ctx context.Context, input MutateManagedPackagesInput) (ManagedEnvironment, error) {
	return m.mutateManagedPackages(ctx, "uninstall", input)
}

// RegisterManagedEnvironment activates an existing user-managed virtual
// environment without copying or mutating it. The caller must separately
// prove that source_path and venv_path are covered by the owner's read-write
// host grant; the kernel layer still revalidates canonical paths and runtime
// executables before publishing the registration.
func (m *Manager) RegisterManagedEnvironment(ctx context.Context, input RegisterManagedEnvironmentInput) (ManagedEnvironment, error) {
	if err := validateManagedEnvironmentName(input.Name); err != nil {
		return ManagedEnvironment{}, err
	}
	language, err := validateManagedLanguage(input.Language, false)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if language != "python" {
		return ManagedEnvironment{}, errors.New("registered environments currently require python")
	}
	sourcePath, err := canonicalManagedRegistrationPath(input.SourcePath)
	if err != nil {
		return ManagedEnvironment{}, errors.New("registered environment source_path is invalid")
	}
	venvPath := strings.TrimSpace(input.VenvPath)
	if venvPath == "" {
		venvPath = filepath.Join(sourcePath, ".venv")
	}
	if input.Create {
		var parent string
		parent, err = canonicalManagedRegistrationPath(filepath.Dir(venvPath))
		if err == nil && filepath.IsAbs(venvPath) {
			venvPath = filepath.Join(parent, filepath.Base(venvPath))
		} else {
			return ManagedEnvironment{}, errors.New("registered environment venv_path parent is invalid")
		}
		if err != nil {
			return ManagedEnvironment{}, err
		}
	} else {
		venvPath, err = canonicalManagedRegistrationPath(venvPath)
		if err != nil {
			return ManagedEnvironment{}, errors.New("registered environment venv_path is invalid")
		}
	}
	if sourcePath == venvPath || hostPathContains(venvPath, sourcePath) {
		return ManagedEnvironment{}, errors.New("registered environment venv_path must not contain source_path")
	}
	requestKey := managedEnvironmentRequestKey(input.OperationID, "register", struct {
		Name, Language, SourcePath, VenvPath string
		Create, Force                        bool
		Extras                               []string
	}{
		input.Name, language, sourcePath, venvPath, input.Create, input.Force, append([]string(nil), input.Extras...),
	})
	return m.runManagedEnvironmentOperation(ctx, requestKey, func(operationContext context.Context) (ManagedEnvironment, error) {
		var prepare func(context.Context) error
		if input.Create {
			prepare = func(ctx context.Context) error {
				_, err := m.createRegisteredEnvironment(ctx, sourcePath, venvPath, input.Extras, input.Force)
				return err
			}
		}
		return m.publishRegisteredManagedEnvironment(operationContext, input.Name, language, sourcePath, venvPath, requestKey, prepare)
	})
}

func (m *Manager) createRegisteredEnvironment(ctx context.Context, sourcePath, requestedVenv string, extras []string, force bool) (string, error) {
	if m == nil || strings.TrimSpace(m.config.Python) == "" {
		return "", errors.New("registered environment creation requires the managed Python runtime")
	}
	if !filepath.IsAbs(requestedVenv) {
		return "", errors.New("registered environment venv_path must be absolute")
	}
	parent, err := canonicalManagedRegistrationPath(filepath.Dir(requestedVenv))
	if err != nil {
		return "", errors.New("registered environment venv_path parent is invalid")
	}
	venvPath := filepath.Join(parent, filepath.Base(filepath.Clean(requestedVenv)))
	if info, statErr := os.Lstat(venvPath); statErr == nil {
		if !force {
			return "", errors.New("registered environment venv_path already exists; set force=true to recreate it")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("registered environment force refuses a non-directory or symlink venv_path")
		}
		if err := os.RemoveAll(venvPath); err != nil {
			return "", errors.New("registered environment existing venv_path could not be removed")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", errors.New("registered environment venv_path could not be inspected")
	}
	if err := m.runManagedEnvironmentProcess(ctx, m.config.Python, "-m", "venv", "--system-site-packages", venvPath); err != nil {
		return "", fmt.Errorf("create registered environment: %w", err)
	}
	python, err := managedPythonExecutableAtPrefix(venvPath)
	if err != nil {
		_ = os.RemoveAll(venvPath)
		return "", errors.New("registered environment Python executable is unavailable")
	}
	for _, extra := range extras {
		if !managedPackageName.MatchString(strings.TrimSpace(extra)) {
			_ = os.RemoveAll(venvPath)
			return "", errors.New("registered environment extras contain an invalid name")
		}
	}
	editable := sourcePath
	if len(extras) > 0 {
		editable += "[" + strings.Join(extras, ",") + "]"
	}
	if err := m.runManagedEnvironmentProcess(ctx, python, "-I", "-m", "pip", "install", "-e", editable); err != nil {
		_ = os.RemoveAll(venvPath)
		return "", fmt.Errorf("install registered environment project: %w", err)
	}
	return canonicalManagedRegistrationPath(venvPath)
}

func (m *Manager) mutateManagedPackages(ctx context.Context, operation string, input MutateManagedPackagesInput) (ManagedEnvironment, error) {
	if err := validateManagedEnvironmentName(input.Environment); err != nil {
		return ManagedEnvironment{}, err
	}
	var packages []string
	var err error
	if input.UsePip {
		packages, err = validateManagedPipRequirementSpecs(input.Packages)
	} else {
		packages, err = validateManagedPackageSpecs(input.Packages)
	}
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if len(packages) == 0 {
		return ManagedEnvironment{}, errors.New("at least one package is required")
	}
	channels, err := validateManagedChannels(input.Channels)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipArgs, err := validateManagedPipArguments(input.PipArgs)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipFindLinks, err := validateManagedPipSourceURLs(input.PipFindLinks)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	pipExtraIndexURLs, err := validateManagedPipSourceURLs(input.PipExtraIndexURLs)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if err := validateManagedRequiredAccelerator(nil, input.RequiredAccelerator); err != nil {
		return ManagedEnvironment{}, err
	}
	if input.Environment == m.ManagedPythonEnvironmentName() {
		if operation != "install" {
			return ManagedEnvironment{}, errors.New("the bundled Python baseline is additive-only; install into a derived environment")
		}
		targetName := strings.TrimSpace(input.ForkTo)
		if targetName == "" {
			digest := managedEnvironmentOperationKey("bundled-package-fork", struct {
				Packages            []string
				Channels            []string
				PipArgs             []string
				PipFindLinks        []string
				PipExtraIndexURLs   []string
				UsePip              bool
				RequiredAccelerator string
			}{packages, channels, pipArgs, pipFindLinks, pipExtraIndexURLs, input.UsePip, strings.ToLower(strings.TrimSpace(input.RequiredAccelerator))})
			targetName = "synon-pkg-" + digest[:24]
		} else {
			if err := validateManagedEnvironmentName(targetName); err != nil {
				return ManagedEnvironment{}, err
			}
		}
		create := CreateManagedEnvironmentInput{
			Name: targetName, Language: "python", SourceEnvironment: input.Environment,
			Channels: channels, PipArgs: pipArgs, PipFindLinks: pipFindLinks,
			PipExtraIndexURLs: pipExtraIndexURLs, RequiredAccelerator: input.RequiredAccelerator,
			OperationID: input.OperationID, RequireAbsent: strings.TrimSpace(input.ForkTo) != "",
		}
		if input.UsePip {
			create.PipPhases = [][]string{packages}
		} else {
			create.Packages = packages
		}
		return m.CreateManagedEnvironment(ctx, create)
	}
	targetName := input.Environment
	if strings.TrimSpace(input.ForkTo) != "" {
		if err := validateManagedEnvironmentName(input.ForkTo); err != nil {
			return ManagedEnvironment{}, err
		}
		targetName = input.ForkTo
	}
	source, err := m.readManagedEnvironment(input.Environment, true)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	operationRequest := struct {
		SourceGeneration, TargetName    string
		Packages, Channels, PipArgs     []string
		PipFindLinks, PipExtraIndexURLs []string
		UsePip                          bool
		RequiredAccelerator             string
	}{
		source.Generation, targetName, packages, channels, pipArgs, pipFindLinks, pipExtraIndexURLs,
		input.UsePip, strings.ToLower(strings.TrimSpace(input.RequiredAccelerator)),
	}
	if strings.TrimSpace(input.OperationID) != "" {
		// The durable tool-call identity is stable across recovery even after
		// this operation has activated a new source generation. The exact
		// request fields still fence accidental identity reuse.
		operationRequest.SourceGeneration = ""
	}
	operationKey := managedEnvironmentRequestKey(input.OperationID, operation, operationRequest)
	if source.Kind == "path-venv" {
		if !input.UsePip {
			return ManagedEnvironment{}, errors.New("registered Python environments require use_pip package mutation")
		}
		return m.publishRegisteredEnvironmentFork(ctx, source, targetName, operation, operationKey, strings.TrimSpace(input.ForkTo) != "", channels, packages, pipArgs, pipFindLinks, pipExtraIndexURLs)
	}
	sourcePrefix, err := filepath.EvalSymlinks(filepath.Join(m.config.CondaEnvsPath, input.Environment))
	if err != nil {
		return ManagedEnvironment{}, errors.New("managed environment generation is unavailable")
	}
	authorityMigrations := plannedManagedPackageAuthorityMigrations(source.Packages, packages, input.UsePip)
	// Micromamba clone does not reliably carry pip-owned distributions into
	// the successor prefix. Preserve that verified inventory for both pip and
	// conda mutations; otherwise an unrelated conda install can silently drop
	// a pip package and then fail the additive-resolution check.
	restorePipRequirements, err := managedPipRequirementsFromInventory(source.Packages, packages)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	return m.runManagedEnvironmentOperation(ctx, operationKey, func(operationContext context.Context) (ManagedEnvironment, error) {
		var validateResolved func([]string) error
		if operation == "install" {
			validateResolved = func(resolved []string) error {
				if err := validateAdditiveManagedPackageResolution(source.Packages, resolved, authorityMigrations); err != nil {
					return err
				}
				return validateManagedRequiredAccelerator(resolved, input.RequiredAccelerator)
			}
		}
		return m.publishManagedEnvironment(operationContext, targetName, source.Language, operation, operationKey, channels, "", strings.TrimSpace(input.ForkTo) != "", nil, validateResolved, func(staging string) error {
			if err := m.runManagedEnvironmentCommand(operationContext, "--no-rc", "create", "-y", "-p", staging, "--clone", sourcePrefix); err != nil {
				return err
			}
			if input.UsePip && operation == "install" {
				if missing := managedPipBootstrapPackages(source.Packages); len(missing) != 0 {
					if err := m.runManagedEnvironmentCommand(operationContext, managedCondaMutationArguments("install", staging, channels, missing)...); err != nil {
						return err
					}
				}
			}
			if len(authorityMigrations) != 0 {
				arguments := []string{"-I", "-m", "pip", "uninstall", "-y"}
				for _, migration := range authorityMigrations {
					arguments = append(arguments, migration.PipDistribution)
				}
				if err := m.runManagedPipCommand(operationContext, staging, arguments); err != nil {
					return err
				}
			}
			if input.UsePip {
				if operation == "uninstall" {
					arguments := append([]string{"-I", "-m", "pip", "uninstall", "-y"}, packages...)
					if err := m.runManagedPipCommand(operationContext, staging, arguments); err != nil {
						return err
					}
				}
				if len(restorePipRequirements) > 0 {
					restore := planManagedPipInstall([][]string{restorePipRequirements}, []string{"--no-deps"}, pipFindLinks, pipExtraIndexURLs, "")
					if err := m.runManagedPipInstallPlan(operationContext, staging, restore); err != nil {
						return err
					}
				}
				if operation == "uninstall" {
					return nil
				}
				options := append([]string{"--upgrade-strategy", "only-if-needed"}, pipArgs...)
				return m.runManagedPipInstallPlan(operationContext, staging, planManagedPipInstall([][]string{packages}, options, pipFindLinks, pipExtraIndexURLs, ""))
			}
			arguments := managedCondaMutationArguments(operation, staging, channels, packages)
			if err := m.runManagedEnvironmentCommand(operationContext, arguments...); err != nil {
				return err
			}
			if len(restorePipRequirements) == 0 {
				return nil
			}
			return m.runManagedPipInstallPlan(operationContext, staging, planManagedPipInstall([][]string{restorePipRequirements}, []string{"--no-deps"}, pipFindLinks, pipExtraIndexURLs, ""))
		})
	})
}

func managedCondaMutationArguments(operation, prefix string, channels, packages []string) []string {
	arguments := []string{"--no-rc", operation, "-y", "-p", prefix}
	if operation == "install" {
		arguments = append(arguments, "--freeze-installed")
		arguments = append(arguments, managedChannelArguments(channels)...)
	}
	return append(arguments, packages...)
}

// publishRegisteredEnvironmentFork converts an authorized external venv into
// a server-owned immutable generation before changing packages. The registered
// venv is read-only input: failed installs cannot corrupt it, and running
// kernels keep their pinned generation while future kernels atomically adopt
// the verified fork.
func (m *Manager) publishRegisteredEnvironmentFork(
	ctx context.Context,
	source ManagedEnvironment,
	targetName, operation, operationKey string,
	requireAbsent bool,
	channels, requested, pipArgs, pipFindLinks, pipExtraIndexURLs []string,
) (ManagedEnvironment, error) {
	marker, err := m.activeManagedEnvironmentMarker(source.Name)
	if err != nil || marker.Kind != "path-venv" {
		return ManagedEnvironment{}, errors.New("registered environment authority is unavailable")
	}
	pythonVersion, err := inspectRegisteredPythonVersion(ctx, marker.RuntimePath)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	requirements, err := registeredEnvironmentRequirements(source.Packages, requested, operation)
	if err != nil {
		return ManagedEnvironment{}, err
	}
	requestDigest := managedEnvironmentSpecDigest([][]string{requirements}, nil)
	return m.runManagedEnvironmentOperation(ctx, operationKey, func(operationContext context.Context) (ManagedEnvironment, error) {
		return m.publishManagedEnvironment(
			operationContext, targetName, "python", "registered-"+operation, operationKey,
			channels, requestDigest, requireAbsent, nil, nil,
			func(staging string) error {
				arguments := []string{"--no-rc", "create", "-y", "-p", staging}
				arguments = append(arguments, managedChannelArguments(channels)...)
				arguments = append(arguments, "python="+pythonVersion, "pip")
				if err := m.runManagedEnvironmentCommand(operationContext, arguments...); err != nil {
					return err
				}
				if len(requirements) == 0 {
					return nil
				}
				return m.runManagedPipInstallPlan(operationContext, staging, planManagedPipInstall([][]string{requirements}, pipArgs, pipFindLinks, pipExtraIndexURLs, ""))
			},
		)
	})
}

func (m *Manager) activeManagedEnvironmentMarker(name string) (managedEnvironmentMarker, error) {
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return managedEnvironmentMarker{}, err
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return managedEnvironmentMarker{}, errors.New("managed environment generation is unavailable")
	}
	marker, err := readManagedEnvironmentMarker(prefix)
	if err != nil || marker.Name != name || marker.Generation != filepath.Base(prefix) {
		return managedEnvironmentMarker{}, errors.New("managed environment generation marker is invalid")
	}
	return marker, nil
}

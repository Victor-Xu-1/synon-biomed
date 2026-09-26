package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// managedPipReplayPhase preserves the successful install's source and build
// policy. A flat package inventory cannot reconstruct pip-only distributions
// after micromamba clones a Conda prefix.
type managedPipReplayPhase struct {
	Requirements   []string `json:"requirements"`
	Options        []string `json:"options,omitempty"`
	FindLinks      []string `json:"findLinks,omitempty"`
	ExtraIndexURLs []string `json:"extraIndexUrls,omitempty"`
}

// ManagedPipRestorationError identifies a failure while reconstructing an
// already-verified generation. Callers must not interpret its build error as
// evidence that the active source environment lost the same dependency.
type ManagedPipRestorationError struct {
	Legacy                 bool
	BeforeRequestedPackage bool
	Cause                  error
}

func (e *ManagedPipRestorationError) Error() string {
	if e == nil || e.Cause == nil {
		return "previous pip packages could not be restored"
	}
	return "previous pip packages could not be restored: " + e.Cause.Error()
}

func (e *ManagedPipRestorationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func managedPipReplayOptions(options []string) []string {
	result := make([]string, 0, len(options))
	for _, option := range options {
		switch option {
		case "--no-cache-dir", "--force-reinstall", "--upgrade":
			// These affect one invocation, not the resolved environment. Replaying
			// them would redownload or reinstall otherwise identical distributions.
		default:
			result = append(result, option)
		}
	}
	return result
}

func cloneManagedPipReplay(phases []managedPipReplayPhase) []managedPipReplayPhase {
	if len(phases) == 0 {
		return nil
	}
	result := make([]managedPipReplayPhase, len(phases))
	for index, phase := range phases {
		result[index] = managedPipReplayPhase{
			Requirements:   append([]string(nil), phase.Requirements...),
			Options:        append([]string(nil), phase.Options...),
			FindLinks:      append([]string(nil), phase.FindLinks...),
			ExtraIndexURLs: append([]string(nil), phase.ExtraIndexURLs...),
		}
	}
	return result
}

func managedPipReplayEqual(left, right []managedPipReplayPhase) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !slices.Equal(left[index].Requirements, right[index].Requirements) ||
			!slices.Equal(left[index].Options, right[index].Options) ||
			!slices.Equal(left[index].FindLinks, right[index].FindLinks) ||
			!slices.Equal(left[index].ExtraIndexURLs, right[index].ExtraIndexURLs) {
			return false
		}
	}
	return true
}

func managedPipReplayWithout(phases []managedPipReplayPhase, removed []string) []managedPipReplayPhase {
	excluded := make(map[string]struct{}, len(removed))
	for _, requirement := range removed {
		excluded[managedPipDistributionKey(requirement)] = struct{}{}
	}
	result := cloneManagedPipReplay(phases)
	for index := range result {
		result[index].Requirements = slices.DeleteFunc(result[index].Requirements, func(requirement string) bool {
			_, drop := excluded[managedPipDistributionKey(requirement)]
			return drop
		})
	}
	return slices.DeleteFunc(result, func(phase managedPipReplayPhase) bool { return len(phase.Requirements) == 0 })
}

func validateManagedPipUninstallResolution(previous, resolved, removed []string) error {
	excluded := make(map[string]struct{}, len(removed))
	for _, requirement := range removed {
		excluded[managedPipDistributionKey(requirement)] = struct{}{}
	}
	kept := make([]string, 0, len(previous))
	for _, item := range previous {
		name, authority, ok := managedInventoryPackage(item)
		if ok && authority == "pypi" {
			if _, drop := excluded[managedPipDistributionKey(name)]; drop {
				continue
			}
		}
		kept = append(kept, item)
	}
	for _, item := range resolved {
		name, authority, ok := managedInventoryPackage(item)
		if ok && authority == "pypi" {
			if _, removed := excluded[managedPipDistributionKey(name)]; removed {
				return fmt.Errorf("pip uninstall left %s in the successor generation", name)
			}
		}
	}
	return validateAdditiveManagedPackageResolution(kept, resolved, nil)
}

func validateManagedPipReplay(phases []managedPipReplayPhase) error {
	for _, phase := range phases {
		if len(phase.Requirements) == 0 {
			return errors.New("managed environment pip replay phase is empty")
		}
		if _, err := validateManagedPipRequirementSpecs(phase.Requirements); err != nil {
			return errors.New("managed environment pip replay requirements are invalid")
		}
		if _, err := validateManagedPipArguments(phase.Options); err != nil {
			return errors.New("managed environment pip replay options are invalid")
		}
		if _, err := validateManagedPipSourceURLs(phase.FindLinks); err != nil {
			return errors.New("managed environment pip replay links are invalid")
		}
		if _, err := validateManagedPipSourceURLs(phase.ExtraIndexURLs); err != nil {
			return errors.New("managed environment pip replay indexes are invalid")
		}
	}
	return nil
}

func managedPipReplaySpecDigest(specDigest string, phases []managedPipReplayPhase) (string, error) {
	if len(phases) == 0 {
		return specDigest, nil
	}
	if err := validateManagedPipReplay(phases); err != nil {
		return "", err
	}
	raw, err := json.Marshal(phases)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(specDigest+"\x00pip-replay\x00"), raw...))
	return hex.EncodeToString(digest[:]), nil
}

func (m *Manager) runManagedPipReplay(
	ctx context.Context, prefix string, phases []managedPipReplayPhase, constraints []string,
) error {
	if len(phases) == 0 {
		return nil
	}
	if err := validateManagedPipReplay(phases); err != nil {
		return err
	}
	constraintPath := ""
	if len(constraints) != 0 {
		file, err := os.CreateTemp(prefix, ".synon-pip-constraints-*.txt")
		if err != nil {
			return err
		}
		constraintPath = file.Name()
		defer os.Remove(constraintPath)
		if _, err := file.WriteString(strings.Join(constraints, "\n") + "\n"); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	for _, phase := range phases {
		options := append([]string(nil), phase.Options...)
		if constraintPath != "" {
			options = append(options, "-c", constraintPath)
		}
		plan := planManagedPipInstall([][]string{phase.Requirements}, options, phase.FindLinks, phase.ExtraIndexURLs, "")
		if err := m.runManagedPipInstallPlan(ctx, prefix, plan); err != nil {
			return err
		}
	}
	return nil
}

var managedMissingBuildModule = regexp.MustCompile(`No module named ['"]([A-Za-z_][A-Za-z0-9_.]*)['"]`)

// Old generations recorded package names but not the source/build phase that
// produced them. A failed build can identify an already-installed provider;
// restore that provider first, then retry the remaining pinned inventory with
// the build environment explicitly prepared. Each retry must remove one
// requirement, so a failing installer cannot spin indefinitely.
func (m *Manager) restoreLegacyManagedPipInventory(
	ctx context.Context, prefix, sourcePrefix string, requirements, findLinks, extraIndexes []string,
) ([]managedPipReplayPhase, error) {
	remaining := append([]string(nil), requirements...)
	constraints := append([]string(nil), requirements...)
	var completed []managedPipReplayPhase
	for len(remaining) != 0 {
		options := []string{"--no-deps"}
		if len(completed) != 0 {
			options = append(options, "--no-build-isolation")
		}
		phase := managedPipReplayPhase{
			Requirements: append([]string(nil), remaining...), Options: options,
			FindLinks: append([]string(nil), findLinks...), ExtraIndexURLs: append([]string(nil), extraIndexes...),
		}
		err := m.runManagedPipReplay(ctx, prefix, []managedPipReplayPhase{phase}, constraints)
		if err == nil {
			return append(completed, phase), nil
		}
		match := managedMissingBuildModule.FindStringSubmatch(err.Error())
		if len(match) != 2 {
			return nil, err
		}
		provider, providerErr := managedPipRestoreProvider(ctx, sourcePrefix, match[1], remaining)
		if providerErr != nil {
			return nil, fmt.Errorf("legacy pip restoration cannot locate build provider for %s: %w", match[1], err)
		}
		providerPhase := managedPipReplayPhase{
			Requirements: []string{provider}, Options: []string{"--no-deps"},
			FindLinks: append([]string(nil), findLinks...), ExtraIndexURLs: append([]string(nil), extraIndexes...),
		}
		if err := m.runManagedPipReplay(ctx, prefix, []managedPipReplayPhase{providerPhase}, constraints); err != nil {
			return nil, fmt.Errorf("legacy pip build provider could not be restored: %w", err)
		}
		completed = append(completed, providerPhase)
		remaining = slices.DeleteFunc(remaining, func(requirement string) bool { return requirement == provider })
	}
	return completed, nil
}

func managedPipRestoreProvider(ctx context.Context, sourcePrefix, module string, requirements []string) (string, error) {
	if len(module) > 128 {
		return "", errors.New("build provider module name is too long")
	}
	module = strings.Split(module, ".")[0]
	for _, requirement := range requirements {
		if managedPipDistributionKey(requirement) == managedPipDistributionKey(module) {
			return requirement, nil
		}
	}
	python, err := managedPythonExecutableAtPrefix(sourcePrefix)
	if err != nil {
		return "", err
	}
	const script = "import importlib.metadata,json,sys; print(json.dumps(importlib.metadata.packages_distributions().get(sys.argv[1], [])))"
	command := newWorkerProcessCommand(ctx, python, "-I", "-B", "-c", script, module)
	command.Env = managedEnvironmentRuntimeEnv(sourcePrefix)
	stdout := newTailBuffer(maxDiagnosticBytes)
	command.Stdout = stdout
	if err := runWorkerProcess(command); err != nil {
		return "", err
	}
	var providers []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &providers); err != nil || len(providers) != 1 {
		return "", errors.New("build provider is ambiguous or unavailable")
	}
	for _, requirement := range requirements {
		if managedPipDistributionKey(requirement) == managedPipDistributionKey(providers[0]) {
			return requirement, nil
		}
	}
	return "", errors.New("build provider is outside the source inventory")
}

package kernel

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func registeredEnvironmentRequirements(existing, requested []string, operation string) ([]string, error) {
	requirements := make(map[string]string, len(existing)+len(requested))
	for _, item := range existing {
		name, requirement, err := registeredEnvironmentRequirement(item)
		if err != nil {
			return nil, err
		}
		if name == "pip" || name == "setuptools" || name == "wheel" {
			continue
		}
		requirements[name] = requirement
	}
	for _, item := range requested {
		name := managedPackageRequirementName(item)
		if name == "" {
			return nil, errors.New("managed package requirement is invalid")
		}
		delete(requirements, name)
		if operation == "install" {
			requirements[name] = item
		}
	}
	result := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		result = append(result, requirement)
	}
	sort.Strings(result)
	return result, nil
}

func registeredEnvironmentRequirement(item string) (string, string, error) {
	if !strings.HasSuffix(item, "==pip") {
		return "", "", errors.New("registered environment package inventory is invalid")
	}
	payload := strings.TrimSuffix(item, "==pip")
	separator := strings.IndexByte(payload, '=')
	if separator <= 0 || separator == len(payload)-1 {
		return "", "", errors.New("registered environment package inventory is invalid")
	}
	name := strings.ToLower(payload[:separator])
	version := payload[separator+1:]
	if !managedPackageName.MatchString(name) || strings.ContainsAny(version, "\x00\r\n") {
		return "", "", errors.New("registered environment package inventory is invalid")
	}
	return name, name + "==" + version, nil
}

func managedPackageRequirementName(requirement string) string {
	requirement = strings.TrimSpace(requirement)
	if index := strings.Index(requirement, "::"); index >= 0 {
		requirement = strings.TrimSpace(requirement[index+2:])
	}
	for index, character := range requirement {
		if strings.ContainsRune("[<>=!~ ", character) {
			return strings.ToLower(requirement[:index])
		}
	}
	return strings.ToLower(requirement)
}

type managedInstalledPackage struct {
	Name    string
	Version string
	Build   string
	Channel string
}

var managedVersionToken = regexp.MustCompile(`[0-9]+|[a-z]+`)

func managedDependencySatisfied(installed []string, dependency string) bool {
	requestedChannel, name, constraints, ok := parseManagedDependencyRequirement(dependency)
	if !ok {
		return false
	}
	for _, item := range installed {
		candidate, valid := parseManagedInstalledPackage(item)
		if !valid || candidate.Name != name && managedPipDistributionKey(candidate.Name) != managedPipDistributionKey(name) {
			continue
		}
		if requestedChannel != "" && managedChannelIdentity(candidate.Channel) != managedChannelIdentity(requestedChannel) {
			continue
		}
		if managedVersionSatisfies(candidate.Version, constraints) {
			return true
		}
	}
	return false
}

func parseManagedDependencyRequirement(value string) (channel, name string, constraints []string, ok bool) {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, "::"); index >= 0 {
		if index == 0 || strings.Count(value, "::") != 1 {
			return "", "", nil, false
		}
		channel = strings.ToLower(strings.TrimSpace(value[:index]))
		value = strings.TrimSpace(value[index+2:])
		if !managedChannelName.MatchString(channel) {
			return "", "", nil, false
		}
	}
	name = managedPackageRequirementName(value)
	if name == "" || !managedPackageName.MatchString(name) || len(value) < len(name) {
		return "", "", nil, false
	}
	remainder := strings.TrimSpace(value[len(name):])
	if strings.HasPrefix(remainder, "[") {
		closing := strings.IndexByte(remainder, ']')
		if closing < 0 {
			return "", "", nil, false
		}
		remainder = strings.TrimSpace(remainder[closing+1:])
	}
	if remainder == "" {
		return channel, name, nil, true
	}
	constraints = strings.Split(remainder, ",")
	for index := range constraints {
		constraints[index] = strings.TrimSpace(constraints[index])
		if _, _, valid := splitManagedVersionConstraint(constraints[index]); !valid {
			return "", "", nil, false
		}
	}
	return channel, name, constraints, true
}

func parseManagedInstalledPackage(value string) (managedInstalledPackage, bool) {
	parts := strings.SplitN(strings.TrimSpace(value), "=", 4)
	if len(parts) != 3 && len(parts) != 4 {
		return managedInstalledPackage{}, false
	}
	result := managedInstalledPackage{
		Name: strings.ToLower(strings.TrimSpace(parts[0])), Version: strings.TrimSpace(parts[1]),
	}
	if len(parts) == 3 {
		result.Channel = strings.ToLower(strings.TrimSpace(parts[2]))
	} else {
		result.Build = strings.ToLower(strings.TrimSpace(parts[2]))
		result.Channel = strings.ToLower(strings.TrimSpace(parts[3]))
	}
	if !managedPackageName.MatchString(result.Name) || result.Version == "" || result.Channel == "" {
		return managedInstalledPackage{}, false
	}
	return result, true
}

func managedChannelIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, "/")))
	if parsed, err := url.Parse(value); err == nil && parsed != nil && parsed.Hostname() != "" {
		value = strings.Trim(strings.TrimSpace(parsed.Path), "/")
	}
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		value = value[index+1:]
	}
	if value == "pip" {
		return "pypi"
	}
	return value
}

func managedVersionSatisfies(installed string, constraints []string) bool {
	for _, constraint := range constraints {
		operator, wanted, ok := splitManagedVersionConstraint(constraint)
		if !ok || !managedVersionConstraintSatisfied(installed, operator, wanted) {
			return false
		}
	}
	return true
}

func splitManagedVersionConstraint(value string) (operator, version string, ok bool) {
	value = strings.TrimSpace(value)
	for _, candidate := range []string{"==", "!=", ">=", "<=", "~=", ">", "<", "="} {
		if strings.HasPrefix(value, candidate) {
			version = strings.TrimSpace(strings.TrimPrefix(value, candidate))
			return candidate, version, version != ""
		}
	}
	return "", "", false
}

func managedVersionConstraintSatisfied(installed, operator, wanted string) bool {
	installed = strings.ToLower(strings.TrimSpace(installed))
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	if strings.Contains(wanted, "*") {
		prefix := strings.TrimSuffix(wanted, "*")
		equal := strings.HasPrefix(installed, prefix)
		if operator == "!=" {
			return !equal
		}
		return (operator == "=" || operator == "==") && equal
	}
	comparison := compareManagedVersions(installed, wanted)
	switch operator {
	case "=":
		return comparison == 0 || strings.HasPrefix(installed, wanted+".")
	case "==":
		return comparison == 0
	case "!=":
		return comparison != 0
	case ">=":
		return comparison >= 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case "<":
		return comparison < 0
	case "~=":
		return comparison >= 0 && managedCompatibleRelease(installed, wanted)
	default:
		return false
	}
}

func compareManagedVersions(left, right string) int {
	leftTokens := managedVersionToken.FindAllString(strings.ToLower(left), -1)
	rightTokens := managedVersionToken.FindAllString(strings.ToLower(right), -1)
	for index := 0; index < max(len(leftTokens), len(rightTokens)); index++ {
		leftToken, rightToken := "0", "0"
		if index < len(leftTokens) {
			leftToken = leftTokens[index]
		}
		if index < len(rightTokens) {
			rightToken = rightTokens[index]
		}
		leftNumber, leftErr := strconv.ParseUint(leftToken, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightToken, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		case leftErr == nil:
			return 1
		case rightErr == nil:
			return -1
		default:
			if leftToken < rightToken {
				return -1
			}
			if leftToken > rightToken {
				return 1
			}
		}
	}
	return 0
}

func managedCompatibleRelease(installed, wanted string) bool {
	installedParts := strings.Split(installed, ".")
	wantedParts := strings.Split(wanted, ".")
	if len(installedParts) == 0 || len(wantedParts) == 0 || installedParts[0] != wantedParts[0] {
		return false
	}
	return len(wantedParts) < 3 || len(installedParts) > 1 && installedParts[1] == wantedParts[1]
}

func validateManagedRequiredAccelerator(packages []string, requirement string) error {
	requirement = strings.ToLower(strings.TrimSpace(requirement))
	if requirement == "" || requirement == "none" || requirement == "optional" {
		return nil
	}
	if requirement != "required" {
		return errors.New("managed environment accelerator requirement is invalid")
	}
	if managedEnvironmentExplicitlyCPUOnly(packages) {
		return errors.New("managed environment resolved an explicitly CPU-only compute stack while an accelerator is required")
	}
	return nil
}

func managedEnvironmentExplicitlyCPUOnly(packages []string) bool {
	frameworkSeen := false
	frameworkAcceleratorSeen := false
	frameworkCPUSeen := false
	for _, value := range packages {
		item, ok := parseManagedInstalledPackage(value)
		if !ok {
			continue
		}
		joined := item.Name + " " + item.Version + " " + item.Build
		switch item.Name {
		case "torch", "pytorch", "pytorch-cpu", "libtorch", "torchvision", "torchaudio",
			"tensorflow", "tensorflow-base", "tensorflow-gpu", "jaxlib", "onnxruntime", "onnxruntime-gpu":
			frameworkSeen = true
			if item.Name == "tensorflow-gpu" || item.Name == "onnxruntime-gpu" ||
				strings.Contains(joined, "cuda") || strings.Contains(joined, "cudnn") ||
				strings.Contains(joined, "rocm") || strings.Contains(joined, "gpu_cuda") ||
				strings.Contains(item.Version, "+cu") || strings.Contains(item.Version, "+rocm") {
				frameworkAcceleratorSeen = true
			}
			if item.Name == "pytorch-cpu" || strings.Contains(joined, "cpu_") ||
				strings.Contains(joined, "_cpu") || strings.Contains(joined, "-cpu") {
				frameworkCPUSeen = true
			}
		case "pytorch-cuda":
			frameworkSeen = true
			frameworkAcceleratorSeen = true
		}
	}
	if frameworkAcceleratorSeen || !frameworkSeen {
		return false
	}
	return frameworkCPUSeen
}

type managedPackageAuthorityMigration struct {
	PipDistribution string
	CondaPackage    string
}

// plannedManagedPackageAuthorityMigrations detects a generic ownership change:
// a conda mutation requests a distribution currently owned by pip. The
// immutable successor must remove the pip copy before installing and verifying
// the conda copy, otherwise both package managers can expose competing files.
func plannedManagedPackageAuthorityMigrations(
	existing, requested []string,
	usePip bool,
) []managedPackageAuthorityMigration {
	if usePip {
		return nil
	}
	requestedConda := make(map[string]string, len(requested))
	for _, requirement := range requested {
		name := managedPackageRequirementName(strings.TrimSpace(requirement))
		key := managedPipDistributionKey(name)
		if key != "" {
			requestedConda[key] = name
		}
	}
	if len(requestedConda) == 0 {
		return nil
	}
	migrations := make([]managedPackageAuthorityMigration, 0, len(requestedConda))
	seen := map[string]struct{}{}
	for _, item := range existing {
		name, authority, ok := managedInventoryPackage(item)
		if !ok || authority != "pypi" {
			continue
		}
		key := managedPipDistributionKey(name)
		condaName, requested := requestedConda[key]
		if !requested {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		migrations = append(migrations, managedPackageAuthorityMigration{
			PipDistribution: name,
			CondaPackage:    condaName,
		})
	}
	return migrations
}

func validateAdditiveManagedPackageResolution(existing, resolved []string, migrations []managedPackageAuthorityMigration) error {
	allowedMigrations := map[string]string{}
	for _, migration := range migrations {
		allowedMigrations[managedPipDistributionKey(migration.PipDistribution)] = migration.CondaPackage
	}
	resolvedSet := make(map[string]struct{}, len(resolved))
	for _, item := range resolved {
		resolvedSet[item] = struct{}{}
	}
	for _, item := range existing {
		if _, preserved := resolvedSet[item]; !preserved {
			name, authority, valid := managedInventoryPackage(item)
			if valid && authority == "pypi" {
				if _, migrating := allowedMigrations[managedPipDistributionKey(name)]; migrating {
					continue
				}
			}
			name = strings.SplitN(item, "=", 2)[0]
			if !managedPackageName.MatchString(name) {
				name = "an existing package"
			}
			return fmt.Errorf("additive package install would change %s; create a dedicated environment for different versions", name)
		}
	}
	for _, condaName := range allowedMigrations {
		found := false
		for _, item := range resolved {
			name, authority, valid := managedInventoryPackage(item)
			if valid && authority != "pypi" &&
				managedPipDistributionKey(name) == managedPipDistributionKey(condaName) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("package authority migration did not install conda package %s", condaName)
		}
	}
	return nil
}

func managedInventoryPackage(item string) (string, string, bool) {
	parts := strings.SplitN(item, "=", 4)
	if len(parts) != 4 {
		return "", "", false
	}
	name := strings.ToLower(strings.TrimSpace(parts[0]))
	authority := strings.ToLower(strings.TrimSpace(parts[3]))
	if !managedPackageName.MatchString(name) || authority == "" {
		return "", "", false
	}
	return name, authority, true
}

// managedPipRequirementsFromInventory reconstructs the pip distributions in
// an immutable source generation before applying the next mutation. A conda
// clone preserves conda packages, but micromamba does not guarantee that pip
// distributions remain installed or represented in the cloned prefix. The
// source marker is the verified package authority, so carry its pinned PyPI
// inventory forward while allowing the current request to replace or remove
// packages with the same normalized distribution name.
func managedPipRequirementsFromInventory(installed, requested []string) ([]string, error) {
	excluded := make(map[string]struct{}, len(requested))
	for _, requirement := range requested {
		name := managedPipDistributionKey(requirement)
		if name == "" {
			return nil, errors.New("managed package requirement is invalid")
		}
		excluded[name] = struct{}{}
	}
	requirements := make([]string, 0)
	for _, item := range installed {
		parts := strings.SplitN(item, "=", 4)
		if len(parts) != 4 || !strings.EqualFold(strings.TrimSpace(parts[3]), "pypi") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		version := strings.TrimSpace(parts[1])
		key := managedPipDistributionKey(name)
		if !managedPackageName.MatchString(name) || version == "" || key == "" || strings.ContainsAny(version, "\x00\r\n") {
			return nil, errors.New("managed environment pip package inventory is invalid")
		}
		if key == "pip" || key == "setuptools" || key == "wheel" {
			continue
		}
		if _, replaced := excluded[key]; replaced {
			continue
		}
		requirement := name + "==" + version
		if !managedPackageSpec.MatchString(requirement) {
			return nil, errors.New("managed environment pip package inventory is invalid")
		}
		requirements = append(requirements, requirement)
	}
	sort.Strings(requirements)
	return requirements, nil
}

func managedPipDistributionKey(requirement string) string {
	requirement = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(requirement), "pip::"))
	name := managedPackageRequirementName(requirement)
	if match := managedPipNamedDirectReference.FindStringSubmatch(requirement); len(match) == 3 {
		name = managedPackageRequirementName(strings.TrimSpace(match[1]))
	} else if validManagedPipHTTPSRequirement(requirement) {
		name = managedPipSourceDistributionName(requirement)
	}
	if name == "" {
		return ""
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

func managedPipSourceDistributionName(requirement string) string {
	parsed, err := url.Parse(strings.TrimSpace(requirement))
	if err != nil || parsed == nil {
		return ""
	}
	if fragment, err := url.ParseQuery(parsed.Fragment); err == nil {
		if egg := strings.ToLower(strings.TrimSpace(fragment.Get("egg"))); managedPackageName.MatchString(egg) {
			return egg
		}
	}
	base := path.Base(strings.TrimSuffix(parsed.Path, "/"))
	if marker := strings.Index(strings.ToLower(base), ".git@"); marker >= 0 {
		base = base[:marker]
	} else {
		base = strings.TrimSuffix(base, ".git")
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if managedPackageName.MatchString(base) {
		return base
	}
	return ""
}

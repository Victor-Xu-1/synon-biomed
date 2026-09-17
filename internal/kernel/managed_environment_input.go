package kernel

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var (
	managedPipNamedDirectReference = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9._-]*(?:\[[A-Za-z0-9_,.-]+\])?)\s+@\s+(.+)$`)
	managedChannelName             = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

func validateManagedEnvironmentName(value string) error {
	if !ValidEnvironmentName(strings.TrimSpace(value)) || strings.HasPrefix(strings.TrimSpace(value), ".") {
		return errors.New("environment name must be a bounded path-free identifier")
	}
	return nil
}

func validateManagedLanguage(value string, optional bool) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" && optional {
		return "", nil
	}
	if value != "python" && value != "r" {
		return "", errors.New("language must be python or r")
	}
	return value, nil
}

func validateManagedPackageSpecs(values []string) ([]string, error) {
	if len(values) > maxManagedEnvironmentPackages {
		return nil, errors.New("package request exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		channel := ""
		if strings.Contains(value, "::") {
			if strings.Count(value, "::") != 1 {
				return nil, errors.New("package specification contains an invalid channel prefix")
			}
			parts := strings.SplitN(value, "::", 2)
			channel = strings.ToLower(strings.TrimSpace(parts[0]))
			value = strings.TrimSpace(parts[1])
			if !managedChannelName.MatchString(channel) {
				return nil, errors.New("channel name is invalid")
			}
		}
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") || !managedPackageSpec.MatchString(value) {
			return nil, fmt.Errorf("package specification at index %d is invalid: %q", index, boundedManagedPackageDiagnostic(value))
		}
		if channel != "" {
			value = channel + "::" + value
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

// validateManagedPipRequirementSpecs accepts the bounded distribution
// requirements used by conda plus HTTPS-backed PEP 508/VCS sources. Pip is
// invoked with an argv slice, never a shell, but source references still stay
// restricted to credential-free HTTPS so an agent cannot smuggle filesystem,
// SSH, option, or inline-secret authority through a package string.
func validateManagedPipRequirementSpecs(values []string) ([]string, error) {
	if len(values) > maxManagedEnvironmentPackages {
		return nil, errors.New("pip package request exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("pip package specification at index %d is invalid: %q", index, boundedManagedPackageDiagnostic(value))
		}
		if !managedPackageSpec.MatchString(value) && !validManagedPipHTTPSRequirement(value) {
			return nil, fmt.Errorf("pip package specification at index %d is invalid: %q", index, boundedManagedPackageDiagnostic(value))
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func boundedManagedPackageDiagnostic(value string) string {
	const limit = 160
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func validManagedPipHTTPSRequirement(value string) bool {
	source := strings.TrimSpace(value)
	if match := managedPipNamedDirectReference.FindStringSubmatch(source); len(match) == 3 {
		if !managedPackageSpec.MatchString(strings.TrimSpace(match[1])) {
			return false
		}
		source = strings.TrimSpace(match[2])
	}
	if strings.ContainsAny(source, " \t\\") {
		return false
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed == nil || parsed.User != nil || strings.TrimSpace(parsed.Hostname()) == "" ||
		strings.TrimSpace(parsed.Path) == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "git+https":
		return true
	default:
		return false
	}
}

func normalizeManagedPackageInput(packages, channels []string) ([]string, []string, error) {
	normalizedPackages := make([]string, 0, len(packages))
	channelSet := make([]string, 0, len(channels))
	seenChannel := map[string]struct{}{}
	for _, raw := range channels {
		value := strings.ToLower(strings.TrimSpace(raw))
		if _, duplicate := seenChannel[value]; duplicate {
			continue
		}
		seenChannel[value] = struct{}{}
		channelSet = append(channelSet, value)
	}
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		if index := strings.Index(value, "::"); index > 0 {
			channel := strings.ToLower(strings.TrimSpace(value[:index]))
			rest := strings.TrimSpace(value[index+2:])
			if _, duplicate := seenChannel[channel]; !duplicate {
				seenChannel[channel] = struct{}{}
				channelSet = append(channelSet, channel)
			}
			// Keep the package-scoped channel selector in the solver request. A
			// global channel list alone is not equivalent: when two channels
			// publish the same framework name it can silently select a CPU build
			// instead of the explicitly requested accelerator build.
			value = channel + "::" + rest
		}
		normalizedPackages = append(normalizedPackages, value)
	}
	validatedPackages, err := validateManagedPackageSpecs(normalizedPackages)
	if err != nil {
		return nil, nil, err
	}
	validatedChannels, err := validateManagedChannels(channelSet)
	if err != nil {
		return nil, nil, err
	}
	return validatedPackages, validatedChannels, nil
}

// normalizeManagedEnvironmentCreateInput accepts the concise provider syntax
// commonly emitted by scientific agents while preserving distinct installer
// authorities. Trusted conda channel prefixes remain conda requirements;
// pip:: requirements are moved into an explicit pip phase instead of being
// mistaken for an untrusted conda channel.
func normalizeManagedEnvironmentCreateInput(packages, channels []string, pipPhases [][]string) ([]string, []string, [][]string, error) {
	condaPackages := make([]string, 0, len(packages))
	prefixedPipPackages := make([]string, 0)
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		if strings.HasPrefix(value, "pip::") {
			prefixedPipPackages = append(prefixedPipPackages, strings.TrimSpace(strings.TrimPrefix(value, "pip::")))
			continue
		}
		condaPackages = append(condaPackages, value)
	}
	normalizedPipPhases := make([][]string, 0, len(pipPhases)+1)
	if len(prefixedPipPackages) != 0 {
		normalizedPipPhases = append(normalizedPipPhases, prefixedPipPackages)
	}
	normalizedPipPhases = append(normalizedPipPhases, pipPhases...)

	normalizedPackages, normalizedChannels, err := normalizeManagedPackageInput(condaPackages, channels)
	if err != nil {
		return nil, nil, nil, err
	}
	normalizedPipPhases, err = validateManagedPipPhases(normalizedPipPhases)
	if err != nil {
		return nil, nil, nil, err
	}
	normalizedPipPhases = completeManagedPipRuntimeClosure(normalizedPipPhases)
	return normalizedPackages, normalizedChannels, normalizedPipPhases, nil
}

func completeManagedPipRuntimeClosure(phases [][]string) [][]string {
	hasMeeko := false
	hasGemmi := false
	hasSafeMol := false
	for _, phase := range phases {
		for _, requirement := range phase {
			switch managedPipDistributionKey(requirement) {
			case "meeko":
				hasMeeko = true
			case "gemmi":
				hasGemmi = true
			case "safe_mol":
				hasSafeMol = true
			}
		}
	}
	needsGemmi := hasMeeko && !hasGemmi
	if !needsGemmi && !hasSafeMol {
		return phases
	}
	completed := make([][]string, len(phases))
	for index := range phases {
		completed[index] = append([]string(nil), phases[index]...)
	}
	if needsGemmi {
		if len(completed) == 0 {
			completed = [][]string{{"gemmi"}}
		} else {
			completed[0] = append(completed[0], "gemmi")
		}
	}
	if hasSafeMol {
		// safe-mol 0.1.x imports generation constraints that were removed in
		// Transformers 5. Keep the compatibility rule inside the declarative
		// environment identity, not as a mutable post-activation repair. Add the
		// fence to the safe-mol phase and every later phase that explicitly
		// touches Transformers so an incompatible request fails dependency
		// resolution instead of activating a broken generation.
		for index, phase := range completed {
			touchesCompatibilityBoundary := false
			hasCompatibilityFence := false
			for _, requirement := range phase {
				switch managedPipDistributionKey(requirement) {
				case "safe_mol", "transformers":
					touchesCompatibilityBoundary = true
				}
				if strings.ReplaceAll(strings.ToLower(strings.TrimSpace(requirement)), " ", "") == "transformers<5" {
					hasCompatibilityFence = true
				}
			}
			if touchesCompatibilityBoundary && !hasCompatibilityFence {
				completed[index] = append(completed[index], "transformers<5")
			}
		}
	}
	return completed
}

func validateManagedPipPhases(values [][]string) ([][]string, error) {
	result := make([][]string, 0, len(values))
	total := 0
	for phaseIndex, phase := range values {
		if len(phase) == 0 {
			return nil, errors.New("pip phase must contain at least one package")
		}
		packages, err := validateManagedPipRequirementSpecs(phase)
		if err != nil {
			return nil, fmt.Errorf("pip phase %d: %w", phaseIndex, err)
		}
		total += len(packages)
		if total > maxManagedEnvironmentPackages {
			return nil, errors.New("pip phase request exceeds the supported package count")
		}
		result = append(result, packages)
	}
	return result, nil
}

func validateManagedImportNames(values []string) ([]string, error) {
	if len(values) > maxManagedEnvironmentPackages {
		return nil, errors.New("import witness exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if len(value) == 0 || len(value) > 256 || !managedImportName.MatchString(value) {
			return nil, errors.New("import witness name is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateManagedChannels(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{"conda-forge"}, nil
	}
	if len(values) > 8 {
		return nil, errors.New("channel request exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "bioconda" {
			// bioconda builds assume conda-forge as the base channel; keep both
			// so solve failures do not silently fall back to the default.
			if _, duplicate := seen["conda-forge"]; !duplicate {
				seen["conda-forge"] = struct{}{}
				result = append(result, "conda-forge")
			}
		}
		// A conda channel name is an anaconda.org namespace, not arbitrary
		// executable input. Accept bounded public namespaces (for example pyg,
		// rapidsai, or dglteam) instead of a product-maintained allowlist that
		// makes otherwise valid scientific environments impossible to solve.
		if !managedChannelName.MatchString(value) {
			return nil, errors.New("channel name is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if _, usesDefaults := seen["defaults"]; usesDefaults && len(result) != 1 {
		return nil, errors.New("defaults cannot be mixed with community conda channels")
	}
	return result, nil
}

func validateManagedPipSourceURLs(values []string) ([]string, error) {
	if len(values) > 8 {
		return nil, errors.New("pip source URL request exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("pip source URL is invalid")
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil ||
			strings.TrimSpace(parsed.Hostname()) == "" || parsed.Fragment != "" {
			return nil, errors.New("pip source URL must be credential-free public HTTPS")
		}
		host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
			return nil, errors.New("pip source URL must be credential-free public HTTPS")
		}
		if address := net.ParseIP(host); address != nil &&
			(address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast()) {
			return nil, errors.New("pip source URL must be credential-free public HTTPS")
		}
		canonical := parsed.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func managedPipSourceArguments(findLinks, extraIndexes []string) []string {
	result := make([]string, 0, 2*(len(findLinks)+len(extraIndexes)))
	for _, value := range findLinks {
		result = append(result, "--find-links", value)
	}
	for _, value := range extraIndexes {
		result = append(result, "--extra-index-url", value)
	}
	return result
}

func managedStringSetContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validateManagedPipArguments(values []string) ([]string, error) {
	allowed := map[string]struct{}{
		"--no-build-isolation": {}, "--no-deps": {}, "--pre": {},
		"--upgrade": {}, "--force-reinstall": {}, "--no-cache-dir": {},
	}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, ok := allowed[value]; !ok {
			return nil, errors.New("pip argument is not allowed")
		}
		result = append(result, value)
	}
	return result, nil
}

func managedChannelArguments(channels []string) []string {
	result := make([]string, 0, len(channels)*2)
	for _, channel := range channels {
		result = append(result, "-c", channel)
	}
	return result
}

func managedPackageSetContains(packages []string, name string) bool {
	name = strings.ToLower(name)
	for _, item := range packages {
		candidate := managedPackageRequirementName(strings.TrimSpace(item))
		if candidate == name {
			return true
		}
	}
	return false
}

func managedDependenciesSatisfied(installed, dependencies []string) bool {
	if len(dependencies) == 0 {
		return true
	}
	for _, dependency := range dependencies {
		if !managedDependencySatisfied(installed, dependency) {
			return false
		}
	}
	return true
}

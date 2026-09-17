package compute

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var byocAppNamePattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,63}$")
var modalEnvironmentPattern = regexp.MustCompile("^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$")
var domainLabelPattern = regexp.MustCompile("^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")

type BYOCCleanupResult struct {
	Owned      int      `json:"owned"`
	Terminated []string `json:"terminated"`
	Failed     []string `json:"failed"`
	Skipped    bool     `json:"skipped"`
	Reason     string   `json:"reason,omitempty"`
}

func ValidateBYOCAppName(value string) error {
	if !byocAppNamePattern.MatchString(value) {
		return errors.New("appName must be 1-64 lowercase letters, digits, dot, underscore, or dash")
	}
	return nil
}

func ValidateModalEnvironment(value string) error {
	if value == "" {
		return nil
	}
	if !modalEnvironmentPattern.MatchString(value) {
		return errors.New("environmentName must be 1-64 letters, digits, dot, underscore, or dash")
	}
	return nil
}

func NormalizeBYOCEgressPolicy(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	mode, ok := value["mode"].(string)
	if !ok {
		return nil, errors.New("egressPolicy.mode is required")
	}
	switch mode {
	case "unrestricted", "blocked":
		if len(value) != 1 {
			return nil, fmt.Errorf("egressPolicy mode %q does not accept extra fields", mode)
		}
		return map[string]any{"mode": mode}, nil
	case "allowlist":
		if len(value) != 3 {
			return nil, errors.New("allowlist egressPolicy requires only mode, mirror, and additional")
		}
		mirror, ok := value["mirror"].(bool)
		if !ok {
			return nil, errors.New("egressPolicy.mirror must be boolean")
		}
		raw, ok := value["additional"].([]any)
		if !ok {
			if stringsValue, stringsOK := value["additional"].([]string); stringsOK {
				raw = make([]any, len(stringsValue))
				for index := range stringsValue {
					raw[index] = stringsValue[index]
				}
			} else {
				return nil, errors.New("egressPolicy.additional must be an array")
			}
		}
		if len(raw) > 64 {
			return nil, errors.New("egressPolicy.additional exceeds 64 domains")
		}
		additional := make([]string, 0, len(raw))
		seen := map[string]struct{}{}
		for _, item := range raw {
			domain, ok := item.(string)
			if !ok {
				return nil, errors.New("egressPolicy.additional entries must be strings")
			}
			domain = strings.ToLower(strings.TrimSpace(domain))
			if err := validateDomainPattern(domain); err != nil {
				return nil, err
			}
			if _, exists := seen[domain]; !exists {
				seen[domain] = struct{}{}
				additional = append(additional, domain)
			}
		}
		return map[string]any{"mode": mode, "mirror": mirror, "additional": additional}, nil
	default:
		return nil, errors.New("egressPolicy.mode must be unrestricted, blocked, or allowlist")
	}
}

func validateDomainPattern(value string) error {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/:@?#") {
		return fmt.Errorf("invalid domain pattern %q", value)
	}
	if strings.HasPrefix(value, "*.") {
		value = strings.TrimPrefix(value, "*.")
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return fmt.Errorf("invalid domain pattern %q", value)
	}
	for _, label := range labels {
		if !domainLabelPattern.MatchString(label) {
			return fmt.Errorf("invalid domain pattern %q", value)
		}
	}
	return nil
}

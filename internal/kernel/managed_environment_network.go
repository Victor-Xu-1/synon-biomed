package kernel

import (
	"strings"

	"synon-go/internal/networktls"
)

// Only the resolved product configuration chooses the installer's route.
// The ordinary runtime environment remains separate and has no proxy grant.
func managedEnvironmentInstallerNetworkEnv(environment []string, configuredProxy string) ([]string, error) {
	proxy, err := networktls.NormalizeProxyURL(configuredProxy)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		result = append(result, entry)
	}
	if proxy != "" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			result = append(result, key+"="+proxy)
		}
	}
	return result, nil
}

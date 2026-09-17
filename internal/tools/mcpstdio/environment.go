package mcpstdio

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/subprocess"
)

const (
	maxMCPCommandBytes      = subprocess.MaxCommandBytes
	maxMCPArgumentCount     = subprocess.MaxArgumentCount
	maxMCPArgumentBytes     = subprocess.MaxArgumentBytes
	maxMCPArgumentsBytes    = subprocess.MaxArgumentsBytes
	maxMCPConfiguredEnv     = subprocess.MaxConfiguredEnv
	maxMCPEnvironmentBytes  = subprocess.MaxEnvironmentBytes
	maxMCPEnvironmentValue  = subprocess.MaxEnvironmentValue
	maxMCPEnvironmentKeyLen = subprocess.MaxEnvironmentKeyLen
)

func validateMCPProcessSpec(command string, args []string) error {
	return subprocess.ValidateSpec("MCP subprocess", command, args)
}

func buildMCPEnvironment(layers ...map[string]string) ([]string, error) {
	return subprocess.BuildEnvironment("MCP subprocess", layers...)
}

func trustedMCPEnvironment(config ServerConfig, layers ...map[string]string) map[string]string {
	environment := map[string]string{
		"PYTHONDONTWRITEBYTECODE": "1",
		"SYNON_MCP_X509_STRICT":   "1",
	}
	if config.TrustedTLS != nil {
		if !config.TrustedTLS.Strict {
			environment["SYNON_MCP_X509_STRICT"] = "0"
		}
		if bundle := strings.TrimSpace(config.TrustedTLS.CABundle); bundle != "" {
			bundle = filepath.Clean(bundle)
			environment["SSL_CERT_FILE"] = bundle
			environment["REQUESTS_CA_BUNDLE"] = bundle
			environment["CURL_CA_BUNDLE"] = bundle
		}
		if proxy := trustedMCPProxyURL(config.TrustedTLS.ProxyURL); proxy != "" {
			environment["HTTP_PROXY"] = proxy
			environment["HTTPS_PROXY"] = proxy
			environment["http_proxy"] = proxy
			environment["https_proxy"] = proxy
			environment["NO_PROXY"] = "localhost,127.0.0.1,::1"
			environment["no_proxy"] = "localhost,127.0.0.1,::1"
		}
	}
	for _, layer := range layers {
		for key, value := range layer {
			environment[key] = value
		}
	}
	return environment
}

func trustedMCPProxyURL(configured string) string {
	candidates := []string{configured}
	if strings.TrimSpace(configured) == "" {
		for _, key := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy"} {
			candidates = append(candidates, os.Getenv(key))
		}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.ContainsAny(candidate, "\x00\r\n") {
			continue
		}
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Scheme == "http" && parsed.Hostname() != "" &&
			(parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == "" {
			return candidate
		}
	}
	return ""
}

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"synon-go/internal/config"
	"synon-go/internal/vmrestart"
)

func newVMRestartManager(cfg config.Config) (*vmrestart.Manager, error) {
	distro := strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME"))
	if distro == "" {
		return nil, nil
	}
	userName := strings.TrimSpace(os.Getenv("USER"))
	if userName == "" {
		output, err := exec.Command("id", "-un").Output()
		if err != nil {
			return nil, errors.New("resolve WSL user for VM restart")
		}
		userName = strings.TrimSpace(string(output))
	}
	if userName == "" {
		return nil, errors.New("WSL user is required for VM restart")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	workingDirectory := filepath.Dir(executable)
	if configured := strings.TrimSpace(os.Getenv("SYNON_WORKING_DIR")); configured != "" {
		workingDirectory, err = filepath.Abs(configured)
		if err != nil {
			return nil, err
		}
	}
	environment := collectVMRestartEnvironment(os.Environ())
	environment["SYNON_HOME"] = cfg.HomeDir
	if configPath := strings.TrimSpace(environment["SYNON_CONFIG"]); configPath != "" && !filepath.IsAbs(configPath) {
		if absolute, absoluteErr := filepath.Abs(configPath); absoluteErr == nil {
			environment["SYNON_CONFIG"] = absolute
		}
	}
	unitDirectory := strings.TrimSpace(os.Getenv("SYNON_VM_RESTART_UNIT_DIR"))
	if unitDirectory != "" {
		unitDirectory, err = filepath.Abs(unitDirectory)
		if err != nil {
			return nil, err
		}
	}
	return vmrestart.New(vmrestart.Options{
		HomeDir: cfg.HomeDir, Executable: executable, WorkingDirectory: workingDirectory,
		UnitDirectory: unitDirectory, Distro: distro, User: userName, ServiceName: "synon-go.service",
		Environment: environment,
	})
}

func collectVMRestartEnvironment(values []string) map[string]string {
	allowedExact := map[string]bool{
		"PATH": true, "LANG": true, "LC_ALL": true,
		"USER": true, "WSL_DISTRO_NAME": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	allowedPrefixes := []string{"SYNON_", "FEISHU_", "WECHAT_"}
	environment := map[string]string{}
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		allowed := allowedExact[key]
		if !allowed {
			for _, prefix := range allowedPrefixes {
				if strings.HasPrefix(key, prefix) {
					allowed = true
					break
				}
			}
		}
		if allowed {
			environment[key] = value
		}
	}
	return environment
}

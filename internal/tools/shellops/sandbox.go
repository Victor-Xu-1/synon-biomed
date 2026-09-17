package shellops

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	sandboxRuntimeDirectory = ".synon-runtime"
	sandboxHelperEnv        = "SYNON_INTERNAL_SANDBOX_HELPER"
	sandboxPayloadEnv       = "SYNON_INTERNAL_SANDBOX_PAYLOAD"
)

type sandboxCommand struct {
	Command       string   `json:"command"`
	Args          []string `json:"args"`
	Root          string   `json:"root"`
	Workdir       string   `json:"workdir"`
	Environment   []string `json:"environment"`
	ReadOnlyPaths []string `json:"readOnlyPaths"`
}

func prepareSandboxCommand(root, workdir, command string, args []string, extra map[string]string) (string, []string, []string, SandboxEvidence, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", nil, nil, SandboxEvidence{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, nil, SandboxEvidence{}, fmt.Errorf("resolve sandbox root: %w", err)
	}
	command, err = resolveSandboxExecutable(command, workdir)
	if err != nil {
		return "", nil, nil, SandboxEvidence{}, err
	}
	environment, readOnlyPaths, err := sandboxEnvironment(root, workdir, command, extra)
	if err != nil {
		return "", nil, nil, SandboxEvidence{}, err
	}
	request := sandboxCommand{
		Command: command, Args: append([]string(nil), args...), Root: root, Workdir: workdir,
		Environment: environment, ReadOnlyPaths: readOnlyPaths,
	}
	return wrapSandboxCommand(request)
}

func resolveSandboxExecutable(command, workdir string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("sandbox command is required")
	}
	var resolved string
	var err error
	if filepath.IsAbs(command) {
		resolved = filepath.Clean(command)
	} else if strings.ContainsAny(command, `/\`) {
		resolved, err = filepath.Abs(filepath.Join(workdir, command))
	} else {
		resolved, err = exec.LookPath(command)
	}
	if err != nil {
		return "", fmt.Errorf("resolve sandbox executable %q: %w", command, err)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = evaluated
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect sandbox executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("sandbox executable must be a regular file")
	}
	return resolved, nil
}

func sandboxEnvironment(root, workdir, command string, extra map[string]string) ([]string, []string, error) {
	runtimeRoot := filepath.Join(root, sandboxRuntimeDirectory)
	home := filepath.Join(runtimeRoot, "home")
	tmp := filepath.Join(runtimeRoot, "tmp")
	cache := filepath.Join(runtimeRoot, "cache")
	config := filepath.Join(runtimeRoot, "config")
	data := filepath.Join(runtimeRoot, "data")
	for _, directory := range []string{runtimeRoot, home, tmp, cache, config, data} {
		if err := ensureSandboxDirectory(root, directory); err != nil {
			return nil, nil, err
		}
	}
	values := map[string]string{
		"HOME": home, "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
		"XDG_CACHE_HOME": cache, "XDG_CONFIG_HOME": config, "XDG_DATA_HOME": data,
		"PWD": workdir,
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !sandboxInheritedEnvironmentKey(key) || unsafeEnvironmentValue(value) {
			continue
		}
		values[key] = value
	}
	for rawKey, value := range extra {
		key := strings.TrimSpace(rawKey)
		if err := validateSandboxEnvironmentEntry(key, value); err != nil {
			return nil, nil, err
		}
		values[key] = value
	}
	if strings.TrimSpace(values["PATH"]) == "" {
		values["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	readOnly := []string{filepath.Dir(command)}
	for _, directory := range filepath.SplitList(values["PATH"]) {
		if strings.TrimSpace(directory) == "" {
			continue
		}
		if absolute, err := filepath.Abs(directory); err == nil {
			readOnly = append(readOnly, absolute)
		}
	}
	return environment, uniqueSandboxPaths(readOnly), nil
}

func ensureSandboxDirectory(root, path string) error {
	if err := ensureInside(root, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create sandbox runtime directory: %w", err)
		}
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return errors.New("sandbox runtime path must be a real directory")
	case info.Mode().Perm()&0o077 != 0:
		return os.Chmod(path, 0o700)
	default:
		return nil
	}
}

func sandboxInheritedEnvironmentKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "PATH" || upper == "LANG" || upper == "TERM" || upper == "TZ" || upper == "COLORTERM" || upper == "NO_COLOR" || upper == "SSL_CERT_FILE" || upper == "SSL_CERT_DIR" {
		return true
	}
	return strings.HasPrefix(upper, "LC_")
}

func validateSandboxEnvironmentEntry(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00\r\n") || unsafeEnvironmentValue(value) {
		return errors.New("sandbox environment contains an invalid key or value")
	}
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "SYNON_INTERNAL_") || sensitiveSandboxEnvironmentKey(upper) {
		return fmt.Errorf("sandbox environment variable %q is not allowed", key)
	}
	return nil
}

func sensitiveSandboxEnvironmentKey(upper string) bool {
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL", "COOKIE", "AUTHORIZATION", "PRIVATE_KEY"} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

func unsafeEnvironmentValue(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n") || len(value) > 64*1024
}

func uniqueSandboxPaths(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "." || value == "" {
			continue
		}
		if evaluated, err := filepath.EvalSymlinks(value); err == nil {
			value = evaluated
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func SandboxStatus() SandboxEvidence {
	return platformSandboxStatus()
}

package desktopopen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"synon-go/internal/subprocess"
)

type Result struct {
	PID      int
	Source   string
	Provider string
}

type provider struct {
	name string
	open func(context.Context, string) (Result, error)
}

func Available() bool {
	_, ok := detectProvider()
	return ok
}

func ProviderName() (string, bool) {
	provider, ok := detectProvider()
	return provider.name, ok
}

func Open(ctx context.Context, target string) (Result, error) {
	provider, ok := detectProvider()
	if !ok {
		return Result{}, errors.New("desktop file-open provider is not available")
	}
	return provider.open(ctx, target)
}

func detectProvider() (provider, bool) {
	if configured, ok := detectConfiguredProvider(); ok {
		return configured, true
	}
	switch runtime.GOOS {
	case "windows":
		if candidate, ok := detectPowerShellProvider("windows-powershell-file-open"); ok {
			return candidate, true
		}
	case "darwin":
		if candidate, ok := detectCommandProvider("macos-open", "open", nil); ok {
			return candidate, true
		}
	case "linux":
		if isWSL() {
			if candidate, ok := detectPowerShellProvider("wsl-windows-powershell-file-open"); ok {
				return candidate, true
			}
		}
		for _, candidate := range []struct {
			name   string
			prefix []string
		}{
			{name: "xdg-open"}, {name: "gio", prefix: []string{"open"}},
		} {
			if detected, ok := detectCommandProvider(
				"linux-"+candidate.name+"-open", candidate.name, candidate.prefix,
			); ok {
				return detected, true
			}
		}
		if candidate, ok := detectPowerShellProvider("wsl-windows-powershell-file-open"); ok {
			return candidate, true
		}
	}
	return provider{}, false
}

func detectConfiguredProvider() (provider, bool) {
	command := strings.TrimSpace(os.Getenv("SYNON_DESKTOP_FILE_OPEN_COMMAND"))
	if command == "" {
		return provider{}, false
	}
	commandPath, ok := findExecutable(command)
	if !ok {
		return provider{}, false
	}
	var arguments []string
	if raw := strings.TrimSpace(os.Getenv("SYNON_DESKTOP_FILE_OPEN_ARGS_JSON")); raw != "" {
		if json.Unmarshal([]byte(raw), &arguments) != nil || len(arguments) > 32 {
			return provider{}, false
		}
		for _, argument := range arguments {
			if len(argument) > 8192 || strings.ContainsRune(argument, 0) {
				return provider{}, false
			}
		}
	}
	return commandProvider("configured-command", commandPath, arguments), true
}

func detectCommandProvider(name, command string, prefix []string) (provider, bool) {
	commandPath, ok := findExecutable(command)
	if !ok {
		return provider{}, false
	}
	return commandProvider(name, commandPath, prefix), true
}

func commandProvider(name, commandPath string, prefix []string) provider {
	return provider{name: name, open: func(ctx context.Context, target string) (Result, error) {
		arguments := append(append([]string(nil), prefix...), target)
		return startDetached(ctx, commandPath, arguments, name)
	}}
}

func detectPowerShellProvider(name string) (provider, bool) {
	path, ok := findExecutable(
		"powershell.exe", "powershell",
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell",
	)
	if !ok {
		return provider{}, false
	}
	return provider{name: name, open: func(ctx context.Context, target string) (Result, error) {
		return openWithPowerShell(ctx, path, target, name)
	}}, true
}

func startDetached(ctx context.Context, executable string, arguments []string, source string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	command := exec.Command(executable, arguments...)
	if err := configureCommand("desktop file open", command); err != nil {
		return Result{}, err
	}
	if err := command.Start(); err != nil {
		return Result{}, err
	}
	pid := 0
	if command.Process != nil {
		pid = command.Process.Pid
		if err := command.Process.Release(); err != nil {
			return Result{}, err
		}
	}
	return Result{PID: pid, Source: source, Provider: source}, nil
}

func configureCommand(kind string, command *exec.Cmd) error {
	if err := subprocess.ValidateSpec(kind, command.Path, command.Args[1:]); err != nil {
		return err
	}
	environment, err := subprocess.BuildEnvironment(kind)
	if err != nil {
		return err
	}
	command.Env = environment
	command.WaitDelay = 2 * time.Second
	return nil
}

func findExecutable(candidates ...string) (string, bool) {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
		if filepath.IsAbs(candidate) {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, true
			}
		}
	}
	return "", false
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	text := strings.ToLower(string(data))
	return strings.Contains(text, "microsoft") || strings.Contains(text, "wsl")
}

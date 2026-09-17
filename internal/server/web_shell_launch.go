package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"synon-go/internal/desktopopen"
)

func webShellToolInstalled(tool string) bool {
	tool = strings.ToLower(strings.TrimSpace(tool))
	switch tool {
	case "vscode":
		return firstWebShellExecutable("code", "code-insiders") != ""
	case "terminal":
		if runtime.GOOS == "darwin" {
			return firstWebShellExecutable("open") != ""
		}
		return firstWebShellExecutable(
			"wt.exe", "x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal",
		) != ""
	case "explorer":
		return desktopopen.Available()
	case "officecli":
		_, _, err := resolveWebOfficeCLI()
		return err == nil
	default:
		_, err := exec.LookPath(tool)
		return err == nil
	}
}

func defaultWebShellLauncher(ctx context.Context, action, target, tool string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch action {
	case "open-file", "open-external":
		_, err := desktopopen.Open(ctx, target)
		return err
	case "show-item-in-folder":
		return revealWebShellPath(ctx, target)
	case "open-folder-with":
		return openWebShellFolderWith(ctx, target, tool)
	default:
		return fmt.Errorf("unsupported shell action %q", action)
	}
}

func openWebShellFolderWith(ctx context.Context, target, tool string) error {
	switch tool {
	case "explorer":
		_, err := desktopopen.Open(ctx, target)
		return err
	case "vscode":
		command := firstWebShellExecutable("code", "code-insiders")
		if command == "" {
			return errors.New("VS Code is not installed or not on PATH")
		}
		return startWebShellDetached(ctx, command, []string{target})
	case "terminal":
		return openWebShellTerminal(ctx, target)
	default:
		return fmt.Errorf("unsupported folder tool %q", tool)
	}
}

func firstWebShellExecutable(candidates ...string) string {
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func revealWebShellPath(ctx context.Context, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		_, err = desktopopen.Open(ctx, target)
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		command := firstWebShellExecutable("open")
		if command != "" {
			return startWebShellDetached(ctx, command, []string{"-R", target})
		}
	case "windows":
		command := firstWebShellExecutable("explorer.exe", "explorer")
		if command != "" {
			return startWebShellDetached(ctx, command, []string{"/select," + target})
		}
	default:
		if isWSLRuntime() {
			command := firstWebShellExecutable("explorer.exe")
			if command != "" {
				native, convertErr := webShellWindowsPath(ctx, target)
				if convertErr != nil {
					return convertErr
				}
				return startWebShellDetached(ctx, command, []string{"/select," + native})
			}
		}
		for _, candidate := range []struct {
			name string
			args []string
		}{
			{name: "nautilus", args: []string{"--select", target}},
			{name: "dolphin", args: []string{"--select", target}},
		} {
			if command := firstWebShellExecutable(candidate.name); command != "" {
				return startWebShellDetached(ctx, command, candidate.args)
			}
		}
	}
	_, err = desktopopen.Open(ctx, filepath.Dir(target))
	return err
}

func openWebShellTerminal(ctx context.Context, target string) error {
	if isWSLRuntime() {
		if command := firstWebShellExecutable("wt.exe"); command != "" {
			native, err := webShellWindowsPath(ctx, target)
			if err != nil {
				return err
			}
			return startWebShellDetached(ctx, command, []string{"-d", native})
		}
	}
	if runtime.GOOS == "darwin" {
		if command := firstWebShellExecutable("open"); command != "" {
			return startWebShellDetached(ctx, command, []string{"-a", "Terminal", target})
		}
	}
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{name: "x-terminal-emulator", args: []string{"--working-directory=" + target}},
		{name: "gnome-terminal", args: []string{"--working-directory=" + target}},
		{name: "konsole", args: []string{"--workdir", target}},
		{name: "xfce4-terminal", args: []string{"--working-directory", target}},
	} {
		if command := firstWebShellExecutable(candidate.name); command != "" {
			return startWebShellDetached(ctx, command, candidate.args)
		}
	}
	return errors.New("terminal application is not installed or not on PATH")
}

func startWebShellDetached(ctx context.Context, command string, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	process := exec.Command(command, args...)
	process.Stdin = nil
	process.Stdout = nil
	process.Stderr = nil
	if err := process.Start(); err != nil {
		return err
	}
	if process.Process != nil {
		return process.Process.Release()
	}
	return nil
}

func webShellWindowsPath(ctx context.Context, target string) (string, error) {
	wslpath := firstWebShellExecutable("wslpath")
	if wslpath == "" {
		return "", errors.New("wslpath is required to open this path in Windows")
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stdout := &cappedCommandBuffer{max: 64 << 10}
	stderr := &cappedCommandBuffer{max: 8 << 10}
	command := exec.CommandContext(runCtx, wslpath, "-w", target)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("convert WSL path: %s", detail)
	}
	converted := strings.TrimSpace(stdout.String())
	if converted == "" {
		return "", errors.New("wslpath returned an empty Windows path")
	}
	return converted, nil
}

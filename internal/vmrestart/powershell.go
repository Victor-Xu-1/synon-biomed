package vmrestart

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"
)

func PowerShellRestartScript(request HostRestartRequest) (string, error) {
	request.Distro = strings.TrimSpace(request.Distro)
	request.User = strings.TrimSpace(request.User)
	request.ServiceName = strings.TrimSpace(request.ServiceName)
	if request.Distro == "" || request.User == "" || !serviceNamePattern.MatchString(request.ServiceName) {
		return "", errors.New("invalid WSL host restart request")
	}
	for name, value := range map[string]string{"distro": request.Distro, "user": request.User} {
		if len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("invalid WSL %s", name)
		}
	}
	if request.ShutdownDelaySeconds < 1 {
		request.ShutdownDelaySeconds = 2
	}
	if request.ShutdownDelaySeconds > 30 {
		return "", errors.New("WSL shutdown delay is too large")
	}
	if request.StartAttempts < 1 {
		request.StartAttempts = 20
	}
	if request.StartAttempts > 120 {
		return "", errors.New("WSL service start attempts are too large")
	}
	distro := powerShellSingleQuote(request.Distro)
	user := powerShellSingleQuote(request.User)
	service := powerShellSingleQuote(request.ServiceName)
	return strings.Join([]string{
		"$ErrorActionPreference = 'Continue'",
		"$wsl = Join-Path $env:SystemRoot 'System32\\wsl.exe'",
		"Start-Sleep -Seconds " + fmt.Sprint(request.ShutdownDelaySeconds),
		"& $wsl --shutdown",
		"Start-Sleep -Seconds 2",
		"$started = $false",
		"for ($attempt = 1; $attempt -le " + fmt.Sprint(request.StartAttempts) + "; $attempt++) {",
		"  & $wsl --distribution " + distro + " --user " + user + " --exec systemctl --user start " + service,
		"  if ($LASTEXITCODE -eq 0) { $started = $true; break }",
		"  Start-Sleep -Seconds 1",
		"}",
		"if (-not $started) { exit 1 }",
		"exit 0",
	}, "\r\n"), nil
}

func LaunchPowerShellHost(ctx context.Context, request HostRestartRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	script, err := PowerShellRestartScript(request)
	if err != nil {
		return err
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return errors.New("powershell.exe is required for WSL VM restart")
	}
	encoded := encodePowerShell(script)
	command := exec.Command(powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", encoded,
	)
	configureDetachedProcess(command)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	bytes := make([]byte, 0, len(encoded)*2)
	for _, value := range encoded {
		bytes = append(bytes, byte(value), byte(value>>8))
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func powerShellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

package desktopopen

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"synon-go/internal/subprocess"
)

const defaultOpenTimeout = 10 * time.Second

func openWithPowerShell(
	ctx context.Context,
	powerShellPath string,
	target string,
	source string,
) (Result, error) {
	nativeTarget := target
	if runtime.GOOS == "linux" && isWSL() {
		if converted, ok := windowsPath(ctx, target); ok {
			nativeTarget = converted
		}
	}
	payload, err := json.Marshal(map[string]string{"target": nativeTarget, "source": source})
	if err != nil {
		return Result{}, err
	}
	script := fmt.Sprintf(powerShellOpenScript, base64.StdEncoding.EncodeToString(payload))
	output, err := runOutput(ctx, powerShellPath, []string{
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePowerShell(script),
	}, defaultOpenTimeout)
	if err != nil {
		return Result{}, fmt.Errorf("desktop file open via PowerShell: %w", err)
	}
	output = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(output), "\ufeff"))
	var decoded struct {
		PID    int    `json:"pid"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return Result{}, fmt.Errorf("decode PowerShell file-open result: %w", err)
	}
	if strings.TrimSpace(decoded.Source) == "" {
		decoded.Source = source
	}
	return Result{PID: decoded.PID, Source: decoded.Source, Provider: source}, nil
}

func WindowsPath(ctx context.Context, target string) (string, bool) {
	return windowsPath(ctx, target)
}

func windowsPath(ctx context.Context, target string) (string, bool) {
	wslpath, ok := findExecutable("wslpath")
	if !ok {
		return "", false
	}
	output, err := runOutput(ctx, wslpath, []string{"-w", target}, defaultOpenTimeout)
	if err != nil {
		return "", false
	}
	converted := strings.TrimSpace(output)
	return converted, converted != ""
}

func runOutput(
	ctx context.Context,
	executable string,
	arguments []string,
	timeout time.Duration,
) (string, error) {
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runContext, executable, arguments...)
	if err := configureCommand("desktop file open", command); err != nil {
		return "", err
	}
	stdout := &boundedBuffer{max: 64 << 10}
	stderr := &boundedBuffer{max: 16 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	control, err := subprocess.Prepare(command)
	if err != nil {
		return "", err
	}
	defer control.Close()
	if err := command.Start(); err != nil {
		return "", err
	}
	if err := control.Attach(command); err != nil {
		_ = control.Kill(command)
		_ = command.Wait()
		return "", err
	}
	err = command.Wait()
	if errors.Is(runContext.Err(), context.DeadlineExceeded) {
		return stdout.String(), fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, message)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

type boundedBuffer struct {
	data []byte
	max  int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	length := len(value)
	remaining := buffer.max - len(buffer.data)
	if remaining > 0 {
		if remaining < len(value) {
			value = value[:remaining]
		}
		buffer.data = append(buffer.data, value...)
	}
	return length, nil
}

func (buffer *boundedBuffer) String() string {
	return string(buffer.data)
}

func encodePowerShell(command string) string {
	encoded := utf16.Encode([]rune(command))
	data := make([]byte, len(encoded)*2)
	for index, value := range encoded {
		binary.LittleEndian.PutUint16(data[index*2:], value)
	}
	return base64.StdEncoding.EncodeToString(data)
}

const powerShellOpenScript = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$payloadJson = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(%q))
$payload = $payloadJson | ConvertFrom-Json
$process = Start-Process -FilePath ([string]$payload.target) -PassThru
$pidValue = 0
if ($null -ne $process) {
  try { $pidValue = [int]$process.Id } catch { $pidValue = 0 }
}
[Console]::Out.Write((@{
  pid = $pidValue
  source = [string]$payload.source
} | ConvertTo-Json -Compress))
`

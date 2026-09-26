package shellops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"synon-go/internal/executionprep"
)

const (
	defaultTimeout = 10 * time.Second
	maxOutputBytes = 256 * 1024
)

func timeoutFromSeconds(seconds int64) time.Duration {
	timeout := defaultTimeout
	if seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	return timeout
}

func timeoutFromMilliseconds(milliseconds int64) time.Duration {
	timeout := defaultTimeout
	if milliseconds > 0 {
		timeout = time.Duration(milliseconds) * time.Millisecond
	}
	return timeout
}

type Result struct {
	Command         string          `json:"command"`
	Args            []string        `json:"args"`
	Workdir         string          `json:"workdir"`
	ExitCode        int             `json:"exitCode"`
	Stdout          string          `json:"stdout"`
	Stderr          string          `json:"stderr"`
	StdoutBytes     int             `json:"stdoutBytes"`
	StderrBytes     int             `json:"stderrBytes"`
	StdoutTruncated bool            `json:"stdoutTruncated"`
	StderrTruncated bool            `json:"stderrTruncated"`
	Sandbox         SandboxEvidence `json:"sandbox"`
}

type SandboxEvidence struct {
	Platform       string `json:"platform"`
	Mode           string `json:"mode"`
	Filesystem     string `json:"filesystem"`
	Network        string `json:"network"`
	Environment    string `json:"environment"`
	ResourceLimits bool   `json:"resourceLimits"`
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
}

type SafetyError struct {
	Rule    string
	Message string
}
type RunningCommand struct {
	cmd     *exec.Cmd
	process *sandboxProcess
	stdout  *boundedOutput
	stderr  *boundedOutput
	command string
	args    []string
	workdir string
	sandbox SandboxEvidence
}

func (r *RunningCommand) Kill() error {
	if r == nil || r.process == nil {
		return nil
	}
	return r.process.kill()
}

func (r *RunningCommand) Wait() (Result, error) {
	if r == nil || r.cmd == nil {
		return Result{}, errors.New("running shell command is not started")
	}
	err := r.cmd.Wait()
	if r.process != nil {
		r.process.close()
	}
	result := Result{Command: r.command, Args: append([]string(nil), r.args...), Workdir: r.workdir, Stdout: r.stdout.String(), Stderr: r.stderr.String(), StdoutBytes: r.stdout.Total(), StderrBytes: r.stderr.Total(), StdoutTruncated: r.stdout.Truncated(), StderrTruncated: r.stderr.Truncated(), Sandbox: r.sandbox, ExitCode: 0}
	if r.cmd.ProcessState != nil {
		result.ExitCode = r.cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("shell command failed with exit code %d", result.ExitCode)
	}
	return result, nil
}

func (e SafetyError) Error() string {
	if e.Rule == "" {
		return "shell command denied by sandbox policy"
	}
	return fmt.Sprintf("shell command denied by sandbox policy (%s): %s", e.Rule, e.Message)
}

func CheckSafety(toolName string, command string, args []string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if toolName == "shell_exec" {
		return checkShellExecSafety(command, args)
	}
	return checkShellCommandSafety(command)
}

func Execute(ctx context.Context, root string, command string, args []string, workdir string, timeoutSeconds int64) (Result, error) {
	return ExecuteWithInput(ctx, root, command, args, workdir, timeoutSeconds, "")
}

func ExecuteWithInput(ctx context.Context, root string, command string, args []string, workdir string, timeoutSeconds int64, stdin string) (Result, error) {
	return ExecuteWithInputEnv(ctx, root, command, args, workdir, timeoutSeconds, stdin, nil)
}

func ExecuteWithInputEnv(ctx context.Context, root string, command string, args []string, workdir string, timeoutSeconds int64, stdin string, env map[string]string) (Result, error) {
	return executeWithInputEnvTimeout(ctx, root, command, args, workdir, timeoutFromSeconds(timeoutSeconds), stdin, env)
}

func executeWithInputEnvTimeout(ctx context.Context, root string, command string, args []string, workdir string, timeout time.Duration, stdin string, env map[string]string) (Result, error) {
	if strings.TrimSpace(command) == "" {
		return Result{}, errors.New("shell_exec.command is required")
	}
	resolvedWorkdir, relWorkdir, err := resolveWorkdir(root, workdir)
	if err != nil {
		return Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executable, commandArgs, commandEnv, sandbox, err := prepareSandboxCommand(root, resolvedWorkdir, command, args, env)
	if err != nil {
		return Result{Command: command, Args: append([]string(nil), args...), Workdir: filepath.ToSlash(relWorkdir), Sandbox: sandbox}, err
	}
	cmd := exec.CommandContext(runCtx, executable, commandArgs...)
	cmd.Dir = resolvedWorkdir
	cmd.Env = commandEnv
	stdout := newBoundedOutput(maxOutputBytes)
	stderr := newBoundedOutput(maxOutputBytes)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process, startErr := startSandboxProcess(cmd)
	if startErr != nil {
		err = startErr
	} else {
		err = cmd.Wait()
		process.close()
	}
	result := Result{
		Command:         command,
		Args:            append([]string(nil), args...),
		Workdir:         filepath.ToSlash(relWorkdir),
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutBytes:     stdout.Total(),
		StderrBytes:     stderr.Total(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		Sandbox:         sandbox,
		ExitCode:        0,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			return result, cause
		}
		return result, ctx.Err()
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("shell_exec timed out after %s", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("shell_exec failed with exit code %d", result.ExitCode)
	}
	return result, nil
}

func ExecuteShellCommand(ctx context.Context, root string, shellName string, command string, workdir string, timeoutMillis int64) (Result, error) {
	return ExecuteShellCommandWithInput(ctx, root, shellName, command, workdir, timeoutMillis, "")
}

func ExecuteShellCommandWithInput(ctx context.Context, root string, shellName string, command string, workdir string, timeoutMillis int64, stdin string) (Result, error) {
	return ExecuteShellCommandWithInputEnv(ctx, root, shellName, command, workdir, timeoutMillis, stdin, nil)
}

func ExecuteShellCommandWithInputEnv(ctx context.Context, root string, shellName string, command string, workdir string, timeoutMillis int64, stdin string, env map[string]string) (Result, error) {
	timeout := timeoutFromMilliseconds(timeoutMillis)
	executable, args, err := shellExecutionCommand(ctx, shellName, command)
	if err != nil {
		if shellName == "PowerShell" && executionprep.ObservationFromContext(ctx) == nil {
			if result, compatErr := executePowerShellCompatWithTimeout(ctx, root, command, workdir, timeout, stdin, env); compatErr == nil {
				return result, nil
			}
		}
		return Result{Command: shellName, Args: []string{command}}, err
	}
	return executeWithInputEnvTimeout(ctx, root, executable, args, workdir, timeout, stdin, env)
}

func StartShellCommand(ctx context.Context, root string, shellName string, command string, workdir string) (*RunningCommand, error) {
	return StartShellCommandWithEnv(ctx, root, shellName, command, workdir, nil)
}

func StartShellCommandWithEnv(ctx context.Context, root string, shellName string, command string, workdir string, env map[string]string) (*RunningCommand, error) {
	executable, args, err := shellExecutionCommand(ctx, shellName, command)
	if err != nil {
		return nil, err
	}
	resolvedWorkdir, relWorkdir, err := resolveWorkdir(root, workdir)
	if err != nil {
		return nil, err
	}
	stdout := newBoundedOutput(maxOutputBytes)
	stderr := newBoundedOutput(maxOutputBytes)
	actualExecutable, actualArgs, commandEnv, sandbox, err := prepareSandboxCommand(root, resolvedWorkdir, executable, args, env)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, actualExecutable, actualArgs...)
	cmd.Dir = resolvedWorkdir
	cmd.Env = commandEnv
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process, err := startSandboxProcess(cmd)
	if err != nil {
		return nil, err
	}
	return &RunningCommand{cmd: cmd, process: process, stdout: &stdout, stderr: &stderr, command: executable, args: append([]string(nil), args...), workdir: filepath.ToSlash(relWorkdir), sandbox: sandbox}, nil
}

func shellExecutor(shellName string, command string) (string, []string, error) {
	if strings.TrimSpace(command) == "" {
		return "", nil, errors.New("shell command is required")
	}
	switch shellName {
	case "Bash":
		if runtime.GOOS == "windows" {
			if executable, ok := firstExistingExecutable(windowsBashCandidates()); ok {
				return executable, []string{"-lc", command}, nil
			}
		}
		executable, err := exec.LookPath("bash")
		if err != nil {
			return "", nil, errors.New("Bash requires bash to be installed and available on PATH")
		}
		return executable, []string{"-lc", command}, nil
	case "PowerShell":
		for _, candidate := range []string{"pwsh", "powershell"} {
			executable, err := exec.LookPath(candidate)
			if err == nil {
				return executable, []string{"-NoProfile", "-Command", command}, nil
			}
		}
		return "", nil, errors.New("PowerShell requires pwsh or powershell to be installed and available on PATH")
	case "Shell":
		if runtime.GOOS == "windows" {
			executable := os.Getenv("COMSPEC")
			if executable == "" {
				executable = "cmd"
			}
			return executable, []string{"/C", command}, nil
		}
		return "/bin/sh", []string{"-lc", command}, nil
	default:
		return "", nil, fmt.Errorf("unsupported shell tool: %s", shellName)
	}
}

func windowsBashCandidates() []string {
	return []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
		`C:\msys64\usr\bin\bash.exe`,
	}
}

func firstExistingExecutable(candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func checkShellExecSafety(command string, args []string) error {
	tokens := append([]string{filepath.Base(strings.TrimSpace(command))}, args...)
	return checkShellSegments([][]string{tokens})
}

func checkShellCommandSafety(command string) error {
	if err := checkShellCommandSubstitutionSafety(command); err != nil {
		return err
	}
	return checkShellSegments(splitShellSafetySegments(command))
}

func checkShellCommandSubstitutionSafety(command string) error {
	for _, nested := range shellCommandSubstitutions(command) {
		if downloadsRemoteScriptToShell(nested) {
			return SafetyError{Rule: "remote-script-substitution", Message: "remote downloads executed through command substitution are not allowed"}
		}
	}
	return nil
}

func checkShellSegments(segments [][]string) error {
	for index, segment := range segments {
		if len(segment) == 0 {
			continue
		}
		command, rest := unwrapShellSafetyCommand(segment)
		lowerCommand := strings.ToLower(command)
		switch lowerCommand {
		case "sudo", "su", "doas", "runas":
			return SafetyError{Rule: "privilege-escalation", Message: fmt.Sprintf("%s is not allowed in shell tools", command)}
		case "rm", "remove-item", "del", "erase":
			if dangerousRecursiveRemoval(rest) {
				return SafetyError{Rule: "destructive-removal", Message: "recursive removal targets a root, parent, home, or system path"}
			}
		case "chmod", "chown", "chgrp", "icacls", "takeown":
			if dangerousRecursiveSystemMutation(rest) {
				return SafetyError{Rule: "system-permission-mutation", Message: "recursive permission or ownership mutation targets a root, parent, home, or system path"}
			}
		case "git":
			if len(rest) > 0 && strings.EqualFold(strings.TrimSpace(rest[0]), "config") &&
				(containsFoldedShellArgument(rest[1:], "--global") || containsFoldedShellArgument(rest[1:], "--system")) {
				return SafetyError{Rule: "global-vcs-configuration", Message: "task shell tools cannot mutate global or system Git configuration"}
			}
		case "mkfs", "fdisk", "parted", "diskpart", "mount", "umount":
			return SafetyError{Rule: "device-or-mount-mutation", Message: fmt.Sprintf("%s is not allowed in shell tools", command)}
		}
		if downloadsRemoteScript(segment) && nextSegmentExecutesShell(segments, index) {
			return SafetyError{Rule: "remote-script-pipe", Message: "remote downloads piped directly into a shell are not allowed"}
		}
	}
	return nil
}

func containsFoldedShellArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if strings.EqualFold(strings.TrimSpace(argument), expected) {
			return true
		}
	}
	return false
}

func unwrapShellSafetyCommand(segment []string) (string, []string) {
	tokens := append([]string(nil), segment...)
	for len(tokens) > 0 {
		command := strings.ToLower(filepath.Base(tokens[0]))
		switch command {
		case "env":
			tokens = tokens[1:]
			for len(tokens) > 0 && strings.Contains(tokens[0], "=") && !strings.HasPrefix(tokens[0], "-") {
				tokens = tokens[1:]
			}
		case "command", "builtin", "time", "nice", "nohup":
			tokens = tokens[1:]
		default:
			return filepath.Base(tokens[0]), tokens[1:]
		}
	}
	return "", nil
}

func splitShellSafetySegments(command string) [][]string {
	tokens := shellSafetyTokens(command)
	segments := [][]string{}
	current := []string{}
	for _, token := range tokens {
		switch token {
		case ";", "&&", "||", "|":
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}
		default:
			current = append(current, token)
		}
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

func shellSafetyTokens(command string) []string {
	tokens := []string{}
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\r', '\n':
			flush()
		case ';', '|', '&':
			flush()
			token := string(r)
			if (r == '|' || r == '&') && len(tokens) > 0 && tokens[len(tokens)-1] == token {
				tokens[len(tokens)-1] = token + token
			} else {
				tokens = append(tokens, token)
			}
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func dangerousRecursiveRemoval(args []string) bool {
	recursive := false
	force := false
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "" {
			continue
		}
		if strings.HasPrefix(lower, "-") {
			if lower == "-r" || lower == "-rf" || lower == "-fr" || strings.Contains(lower, "r") {
				recursive = true
			}
			if lower == "-f" || lower == "-rf" || lower == "-fr" || strings.Contains(lower, "f") {
				force = true
			}
			continue
		}
		if recursive && (force || lower == "." || lower == "..") && dangerousShellTarget(lower) {
			return true
		}
	}
	return false
}

func dangerousRecursiveSystemMutation(args []string) bool {
	recursive := false
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "" {
			continue
		}
		if strings.HasPrefix(lower, "-") {
			if lower == "-r" || strings.Contains(lower, "r") || lower == "/t" {
				recursive = true
			}
			continue
		}
		if recursive && dangerousShellTarget(lower) {
			return true
		}
	}
	return false
}

func dangerousShellTarget(target string) bool {
	target = strings.Trim(target, "\"'")
	if target == "." || target == ".." || strings.Contains(target, "../") || strings.Contains(target, `..\`) {
		return true
	}
	if target == "~" || strings.HasPrefix(target, "~/") || strings.Contains(target, "$home") || strings.Contains(target, "${home}") {
		return true
	}
	normalized := strings.ReplaceAll(target, `\`, `/`)
	for _, prefix := range []string{"/", "/*", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/mnt", "/opt", "/proc", "/root", "/sbin", "/sys", "/usr", "/var", "c:/", "c:/*", "c:/windows", "c:/program files", "c:/users"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") {
			return true
		}
	}
	return false
}

func downloadsRemoteScript(segment []string) bool {
	command, rest := unwrapShellSafetyCommand(segment)
	switch strings.ToLower(command) {
	case "curl", "wget", "iwr", "irm", "invoke-webrequest", "invoke-restmethod":
		for _, arg := range rest {
			lower := strings.ToLower(arg)
			if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
				return true
			}
		}
	}
	return false
}

func downloadsRemoteScriptToShell(command string) bool {
	segments := splitShellSafetySegments(command)
	for index, segment := range segments {
		if downloadsRemoteScript(segment) && nextSegmentExecutesShell(segments, index) {
			return true
		}
	}
	return false
}

func nextSegmentExecutesShell(segments [][]string, index int) bool {
	if index+1 >= len(segments) {
		return false
	}
	command, _ := unwrapShellSafetyCommand(segments[index+1])
	switch strings.ToLower(command) {
	case "sh", "bash", "zsh", "fish", "pwsh", "powershell", "iex", "invoke-expression":
		return true
	default:
		return false
	}
}

func shellCommandSubstitutions(command string) []string {
	substitutions := []string{}
	inSingleQuote := false
	escaped := false
	for index := 0; index < len(command); index++ {
		ch := command[index]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inSingleQuote {
			if ch == '\'' {
				inSingleQuote = false
			}
			continue
		}
		switch ch {
		case '\'':
			inSingleQuote = true
		case '`':
			end := findShellBacktickEnd(command, index+1)
			if end > index {
				substitutions = append(substitutions, command[index+1:end])
				index = end
			}
		case '$':
			if index+1 < len(command) && command[index+1] == '(' {
				end := findShellParenSubstitutionEnd(command, index+2)
				if end > index {
					substitutions = append(substitutions, command[index+2:end])
					index = end
				}
			}
		}
	}
	return substitutions
}

func findShellBacktickEnd(command string, start int) int {
	escaped := false
	for index := start; index < len(command); index++ {
		ch := command[index]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '`' {
			return index
		}
	}
	return -1
}

func findShellParenSubstitutionEnd(command string, start int) int {
	depth := 1
	var quote rune
	escaped := false
	for index := start; index < len(command); index++ {
		ch := command[index]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if byte(quote) == ch {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = rune(ch)
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func executePowerShellCompat(ctx context.Context, root string, command string, workdir string, timeoutSeconds int64, stdin string, env map[string]string) (Result, error) {
	return executePowerShellCompatWithTimeout(ctx, root, command, workdir, timeoutFromSeconds(timeoutSeconds), stdin, env)
}

func executePowerShellCompatWithTimeout(ctx context.Context, root string, command string, workdir string, timeout time.Duration, stdin string, env map[string]string) (Result, error) {
	resolvedWorkdir, relWorkdir, err := resolveWorkdir(root, workdir)
	if err != nil {
		return Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := newBoundedOutput(maxOutputBytes)
	stderr := newBoundedOutput(maxOutputBytes)
	result := Result{
		Command: "PowerShell",
		Args:    []string{command},
		Workdir: filepath.ToSlash(relWorkdir),
	}
	for _, statement := range splitPowerShellStatements(command) {
		select {
		case <-runCtx.Done():
			result.Stdout = stdout.String()
			result.Stderr = stderr.String()
			result.StdoutBytes = stdout.Total()
			result.StderrBytes = stderr.Total()
			result.StdoutTruncated = stdout.Truncated()
			result.StderrTruncated = stderr.Truncated()
			result.ExitCode = 124
			if ctx.Err() != nil {
				if cause := context.Cause(ctx); cause != nil {
					return result, cause
				}
				return result, ctx.Err()
			}
			return result, fmt.Errorf("PowerShell compat timed out after %s", timeout)
		default:
		}
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.HasPrefix(statement, "$") {
			continue
		}
		if hasCommandPrefix(statement, "Set-Content") {
			if err := runCompatSetContent(root, resolvedWorkdir, statement, stdin, env); err != nil {
				_, _ = stderr.Write([]byte(err.Error()))
				result.ExitCode = 2
				result.Stdout = stdout.String()
				result.Stderr = stderr.String()
				result.StdoutBytes = stdout.Total()
				result.StderrBytes = stderr.Total()
				result.StdoutTruncated = stdout.Truncated()
				result.StderrTruncated = stderr.Truncated()
				return result, err
			}
			continue
		}
		if hasCommandPrefix(statement, "Write-Output") {
			text, err := compatCommandArgument(statement[len("Write-Output"):])
			if err != nil {
				_, _ = stderr.Write([]byte(err.Error()))
				result.ExitCode = 2
				result.Stdout = stdout.String()
				result.Stderr = stderr.String()
				result.StdoutBytes = stdout.Total()
				result.StderrBytes = stderr.Total()
				result.StdoutTruncated = stdout.Truncated()
				result.StderrTruncated = stderr.Truncated()
				return result, err
			}
			text = expandPowerShellCompatEnv(text, env)
			_, _ = stdout.Write([]byte(text + "\n"))
			continue
		}
		err := fmt.Errorf("PowerShell compat does not support statement: %s", statement)
		_, _ = stderr.Write([]byte(err.Error()))
		result.ExitCode = 127
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.StdoutBytes = stdout.Total()
		result.StderrBytes = stderr.Total()
		result.StdoutTruncated = stdout.Truncated()
		result.StderrTruncated = stderr.Truncated()
		return result, err
	}
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.StdoutBytes = stdout.Total()
	result.StderrBytes = stderr.Total()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	result.ExitCode = 0
	return result, nil
}

func splitPowerShellStatements(command string) []string {
	statements := []string{}
	start := 0
	var quote rune
	for index, r := range command {
		switch r {
		case '\'', '"':
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
		case ';':
			if quote == 0 {
				statements = append(statements, command[start:index])
				start = index + len(string(r))
			}
		}
	}
	statements = append(statements, command[start:])
	return statements
}

func hasCommandPrefix(statement string, prefix string) bool {
	statement = strings.TrimSpace(statement)
	if len(statement) < len(prefix) {
		return false
	}
	if !strings.EqualFold(statement[:len(prefix)], prefix) {
		return false
	}
	return len(statement) == len(prefix) || statement[len(prefix)] == ' ' || statement[len(prefix)] == '\t'
}

func runCompatSetContent(root string, resolvedWorkdir string, statement string, stdin string, env map[string]string) error {
	pathIndex := strings.Index(strings.ToLower(statement), "-literalpath")
	if pathIndex < 0 {
		return fmt.Errorf("PowerShell compat Set-Content requires -LiteralPath")
	}
	afterPath := statement[pathIndex+len("-literalpath"):]
	targetPath, rest, err := compatCommandArgumentWithRest(afterPath)
	if err != nil {
		return err
	}
	targetPath = expandPowerShellCompatEnv(targetPath, env)
	valueIndex := strings.Index(strings.ToLower(rest), "-value")
	if valueIndex < 0 {
		return fmt.Errorf("PowerShell compat Set-Content requires -Value")
	}
	valueText, _, err := compatCommandArgumentWithRest(rest[valueIndex+len("-value"):])
	if err != nil {
		return err
	}
	content := valueText
	if strings.EqualFold(valueText, "$stdin") {
		content = stdin
	} else {
		content = expandPowerShellCompatEnv(content, env)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target := targetPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(resolvedWorkdir, targetPath)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := ensureInside(rootAbs, targetAbs); err != nil {
		return err
	}
	return os.WriteFile(targetAbs, []byte(content), 0o644)
}

func expandPowerShellCompatEnv(value string, env map[string]string) string {
	if len(env) == 0 || !strings.Contains(value, "$env:") && !strings.Contains(value, "${env:") {
		return value
	}
	result := value
	for key, envValue := range env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		result = strings.ReplaceAll(result, "$env:"+key, envValue)
		result = strings.ReplaceAll(result, "${env:"+key+"}", envValue)
	}
	return result
}

func compatCommandArgument(raw string) (string, error) {
	value, _, err := compatCommandArgumentWithRest(raw)
	return value, err
}

func compatCommandArgumentWithRest(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("PowerShell compat command argument is required")
	}
	if raw[0] == '\'' || raw[0] == '"' {
		quote := raw[0]
		var builder strings.Builder
		for i := 1; i < len(raw); i++ {
			if raw[i] == quote {
				return builder.String(), raw[i+1:], nil
			}
			builder.WriteByte(raw[i])
		}
		return "", "", errors.New("PowerShell compat unterminated quoted argument")
	}
	end := len(raw)
	for i, r := range raw {
		if r == ' ' || r == '\t' {
			end = i
			break
		}
	}
	return raw[:end], raw[end:], nil
}

type boundedOutput struct {
	buffer    bytes.Buffer
	limit     int
	total     int
	truncated bool
}

func newBoundedOutput(limit int) boundedOutput {
	return boundedOutput{limit: limit}
}

func (o *boundedOutput) Write(chunk []byte) (int, error) {
	o.total += len(chunk)
	if o.limit <= 0 {
		if len(chunk) > 0 {
			o.truncated = true
		}
		return len(chunk), nil
	}
	remaining := o.limit - o.buffer.Len()
	if remaining <= 0 {
		if len(chunk) > 0 {
			o.truncated = true
		}
		return len(chunk), nil
	}
	if len(chunk) > remaining {
		_, _ = o.buffer.Write(chunk[:remaining])
		o.truncated = true
		return len(chunk), nil
	}
	_, _ = o.buffer.Write(chunk)
	return len(chunk), nil
}

func (o *boundedOutput) String() string {
	return o.buffer.String()
}

func (o *boundedOutput) Total() int {
	return o.total
}

func (o *boundedOutput) Truncated() bool {
	return o.truncated
}

func resolveWorkdir(root string, requested string) (string, string, error) {
	if root == "" {
		return "", "", errors.New("shell_exec root is not configured")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if requested == "" {
		requested = "."
	}
	var target string
	if filepath.IsAbs(requested) {
		target, err = filepath.Abs(requested)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requested))
	}
	if err != nil {
		return "", "", err
	}
	evaluated, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}
	if err := ensureInside(rootAbs, evaluated); err != nil {
		return "", "", err
	}
	info, err := os.Stat(evaluated)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("shell_exec.workdir is not a directory: %s", requested)
	}
	rel, err := filepath.Rel(rootAbs, evaluated)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		rel = "."
	}
	return evaluated, rel, nil
}

func ensureInside(rootAbs string, targetAbs string) error {
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes shell_exec root: %s", filepath.ToSlash(rel))
	}
	return nil
}

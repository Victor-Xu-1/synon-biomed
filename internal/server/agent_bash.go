package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"synon-go/internal/kernelcontract"
	"synon-go/internal/tools/shellops"
)

const (
	agentBashSourcePrefix = kernelcontract.BashSourcePrefix
	agentBashExitPrefix   = kernelcontract.BashExitPrefix
	maxAgentBashBytes     = kernelcontract.MaxBashBytes
)

func validateAgentBashCommand(command string) error {
	if strings.TrimSpace(command) == "" || len(command) > maxAgentBashBytes || strings.ContainsRune(command, '\x00') {
		return errors.New("bash command must be 1-262144 bytes without NUL characters")
	}
	if err := shellops.CheckSafety("Bash", command, nil); err != nil {
		return err
	}
	return shellops.CheckPackageManagerMutation(command)
}

// agentBashPythonWrapper uses the already confined, environment-bound kernel
// process as the durable lifecycle owner for Bash. The command is base64 data,
// never interpolated into Python syntax. The canonical shell envelope stops on
// the first unhandled command or pipeline failure so a trailing mkdir/echo
// cannot turn a failed scientific executable into a successful tool receipt.
// Stream pumps avoid buffering unbounded child output while the kernel's
// existing capped streams preserve the transcript/output limit contract.
func agentBashPythonWrapper(command string) (string, error) {
	if err := validateAgentBashCommand(command); err != nil {
		return "", err
	}
	return kernelcontract.BashPythonWrapper(command)
}

func agentBashCommandFromWrapper(source string) (string, error) {
	command, err := kernelcontract.BashCommandFromWrapper(source)
	if err != nil {
		return "", err
	}
	if err := validateAgentBashCommand(command); err != nil {
		return "", errors.New("bash durable source command is invalid")
	}
	return command, nil
}

func normalizeAgentBashTerminal(stderr string, status string) (visibleStderr string, exitStatus string, exitCode int, err error) {
	exitStatus = strings.TrimSpace(status)
	marker := "\n" + agentBashExitPrefix
	index := strings.LastIndex(stderr, marker)
	if index < 0 {
		if exitStatus == "ok" {
			return stderr, "error", -1, errors.New("bash terminal receipt is missing")
		}
		return stderr, exitStatus, -1, nil
	}
	payload := stderr[index+len(marker):]
	line, tail, _ := strings.Cut(payload, "\n")
	if strings.TrimSpace(tail) != "" {
		return stderr, "error", -1, errors.New("bash terminal receipt is ambiguous")
	}
	exitCode, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if parseErr != nil || exitCode < 0 || exitCode > 255 {
		return stderr, "error", -1, errors.New("bash terminal receipt is invalid")
	}
	visibleStderr = stderr[:index]
	if exitStatus == "ok" && exitCode != 0 {
		exitStatus = "error"
		if strings.TrimSpace(visibleStderr) == "" {
			visibleStderr = fmt.Sprintf("Bash command exited with status %d.", exitCode)
		}
	}
	return visibleStderr, exitStatus, exitCode, nil
}

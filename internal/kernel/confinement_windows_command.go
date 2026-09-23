//go:build windows

package kernel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	windowsConfinedHelperEnv  = "SYNON_INTERNAL_WINDOWS_CONFINED_HELPER"
	windowsConfinedPayloadEnv = "SYNON_INTERNAL_WINDOWS_CONFINED_PAYLOAD"
	maxWindowsConfinedPayload = 20 << 10
)

var windowsConfinementOSKeys = []string{
	"SystemRoot", "WINDIR", "SystemDrive", "USERPROFILE", "LOCALAPPDATA", "APPDATA",
	"ProgramData", "PUBLIC", "ComSpec", "OS", "HOMEDRIVE", "HOMEPATH",
	"PATH", "PATHEXT", "TEMP", "TMP",
}

type windowsConfinedRequest struct {
	ProfileName   string   `json:"profile_name"`
	Workspace     string   `json:"workspace"`
	Executable    string   `json:"executable"`
	Arguments     []string `json:"arguments"`
	Environment   []string `json:"environment"`
	ReadOnlyRoots []string `json:"read_only_roots,omitempty"`
}

func newWindowsConfinedCommand(workspace, executable string, arguments, environment, readOnlyRoots []string) (*exec.Cmd, error) {
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, windowsConfinedHelperEnv) || strings.EqualFold(key, windowsConfinedPayloadEnv) {
			return nil, errors.New("kernel worker environment contains a reserved confinement key")
		}
	}
	request := windowsConfinedRequest{
		ProfileName: "SynonWorker." + uuid.NewString(),
		Workspace:   workspace, Executable: executable,
		Arguments:     append([]string(nil), arguments...),
		Environment:   append([]string(nil), environment...),
		ReadOnlyRoots: append([]string(nil), readOnlyRoots...),
	}
	if err := validateWindowsConfinementReadOnlyRoots(request.Workspace, request.ReadOnlyRoots); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > maxWindowsConfinedPayload {
		return nil, errors.New("kernel worker confinement request is too large")
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate Windows kernel confinement helper: %w", err)
	}
	command := exec.Command(helper)
	command.WaitDelay = windowsConfinedWaitDelay
	command.Env = []string{
		windowsConfinedHelperEnv + "=1",
		windowsConfinedPayloadEnv + "=" + base64.RawStdEncoding.EncodeToString(encoded),
	}
	for _, key := range windowsConfinementOSKeys {
		if value := os.Getenv(key); value != "" {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Dir = filepath.Clean(workspace)
	return command, nil
}

func windowsConfinedRequestFromCommand(command *exec.Cmd) (*windowsConfinedRequest, error) {
	if command == nil {
		return nil, nil
	}
	marker, payload := "", ""
	for _, entry := range command.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(key, windowsConfinedHelperEnv):
			marker = value
		case strings.EqualFold(key, windowsConfinedPayloadEnv):
			payload = value
		}
	}
	if marker == "" && payload == "" {
		return nil, nil
	}
	if marker != "1" || payload == "" {
		return nil, errors.New("Windows kernel confinement helper request is incomplete")
	}
	return decodeWindowsConfinedRequest(payload)
}

func decodeWindowsConfinedRequest(payload string) (*windowsConfinedRequest, error) {
	if len(payload) > base64.RawStdEncoding.EncodedLen(maxWindowsConfinedPayload) {
		return nil, errors.New("Windows kernel confinement helper request is too large")
	}
	raw, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil || len(raw) > maxWindowsConfinedPayload {
		return nil, errors.New("Windows kernel confinement helper request is invalid")
	}
	request := &windowsConfinedRequest{}
	if err := json.Unmarshal(raw, request); err != nil {
		return nil, errors.New("Windows kernel confinement helper request is invalid")
	}
	if !strings.HasPrefix(request.ProfileName, "SynonWorker.") {
		return nil, errors.New("Windows kernel confinement profile identity is invalid")
	}
	if _, err := uuid.Parse(strings.TrimPrefix(request.ProfileName, "SynonWorker.")); err != nil {
		return nil, errors.New("Windows kernel confinement profile identity is invalid")
	}
	if err := validateWindowsConfinementRequest(
		request.Workspace, request.Executable, request.Arguments, request.Environment,
		nil, nil, nil,
	); err != nil {
		return nil, err
	}
	if err := validateWindowsConfinementReadOnlyRoots(request.Workspace, request.ReadOnlyRoots); err != nil {
		return nil, err
	}
	return request, nil
}

func validateWindowsConfinementReadOnlyRoots(workspace string, roots []string) error {
	if len(roots) > 8 {
		return errors.New("Windows kernel read-only roots exceed the bounded contract")
	}
	for _, root := range roots {
		if err := validateWindowsKernelPath(root, true, false); err != nil {
			return fmt.Errorf("Windows kernel read-only root: %w", err)
		}
		if windowsKernelPathsOverlap(root, workspace) {
			return errors.New("Windows kernel read-only root overlaps the writable workspace")
		}
	}
	return nil
}

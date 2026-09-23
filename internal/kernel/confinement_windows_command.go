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
	"golang.org/x/sys/windows"
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
	ProfileName   string                         `json:"profile_name"`
	Workspace     string                         `json:"workspace"`
	Executable    string                         `json:"executable"`
	Arguments     []string                       `json:"arguments"`
	Environment   []string                       `json:"environment"`
	ReadOnlyRoots []string                       `json:"read_only_roots,omitempty"`
	ReadOnlyFiles []string                       `json:"read_only_files,omitempty"`
	WritableFiles []string                       `json:"writable_files,omitempty"`
	Frozen        []windowsConfinementFrozenPath `json:"frozen,omitempty"`
	Authority     []windowsConfinementFrozenPath `json:"authority"`
}

type windowsConfinementFrozenPath struct {
	Path      string `json:"path"`
	Volume    uint32 `json:"volume"`
	IndexHigh uint32 `json:"index_high"`
	IndexLow  uint32 `json:"index_low"`
}

func windowsFrozenPath(path string, file *os.File) (windowsConfinementFrozenPath, error) {
	if file == nil {
		return windowsConfinementFrozenPath{}, errors.New("Windows kernel frozen file is missing")
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return windowsConfinementFrozenPath{}, err
	}
	return windowsConfinementFrozenPath{
		Path: path, Volume: identity.VolumeSerialNumber,
		IndexHigh: identity.FileIndexHigh, IndexLow: identity.FileIndexLow,
	}, nil
}

func newWindowsConfinedCommand(workspace, executable string, arguments, environment, readOnlyRoots []string) (*exec.Cmd, error) {
	return newWindowsConfinedCommandWithAuthority(workspace, executable, arguments, environment, readOnlyRoots, nil, nil)
}

func newWindowsConfinedCommandWithAuthority(workspace, executable string, arguments, environment, readOnlyRoots, readOnlyFiles, writableFiles []string) (*exec.Cmd, error) {
	return newWindowsConfinedCommandWithFrozenAuthority(workspace, executable, arguments, environment, readOnlyRoots, readOnlyFiles, writableFiles, nil)
}

func newWindowsConfinedCommandWithFrozenAuthority(workspace, executable string, arguments, environment, readOnlyRoots, readOnlyFiles, writableFiles []string, frozen []windowsConfinementFrozenPath) (*exec.Cmd, error) {
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
		ReadOnlyFiles: append([]string(nil), readOnlyFiles...),
		WritableFiles: append([]string(nil), writableFiles...),
		Frozen:        append([]windowsConfinementFrozenPath(nil), frozen...),
	}
	if err := validateWindowsConfinementAuthority(&request); err != nil {
		return nil, err
	}
	for _, grant := range windowsConfinementRequestGrants(&request) {
		identity, err := windowsConfinementPathIdentity(grant.path)
		if err != nil {
			return nil, fmt.Errorf("bind Windows kernel authority: %w", err)
		}
		request.Authority = append(request.Authority, identity)
	}
	if err := validateWindowsConfinementAuthorityBindings(&request); err != nil {
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
	if err := validateWindowsConfinementAuthority(request); err != nil {
		return nil, err
	}
	if err := validateWindowsConfinementAuthorityBindings(request); err != nil {
		return nil, err
	}
	return request, nil
}

func validateWindowsConfinementReadOnlyRootsAt(workspace string, roots []string, mustExist bool) error {
	if len(roots) > 8 {
		return errors.New("Windows kernel read-only roots exceed the bounded contract")
	}
	for _, root := range roots {
		if err := validateWindowsKernelPath(root, mustExist, false); err != nil {
			return fmt.Errorf("Windows kernel read-only root: %w", err)
		}
		if windowsKernelPathsOverlap(root, workspace) {
			return errors.New("Windows kernel read-only root overlaps the writable workspace")
		}
	}
	return nil
}

func validateWindowsConfinementAuthority(request *windowsConfinedRequest) error {
	return validateWindowsConfinementAuthorityAt(request, true)
}

func validateWindowsConfinementAuthorityAt(request *windowsConfinedRequest, mustExist bool) error {
	if request == nil {
		return errors.New("Windows kernel confinement authority is missing")
	}
	if err := validateWindowsConfinementReadOnlyRootsAt(request.Workspace, request.ReadOnlyRoots, mustExist); err != nil {
		return err
	}
	if len(request.ReadOnlyFiles)+len(request.WritableFiles) > 16 {
		return errors.New("Windows kernel file grants exceed the bounded contract")
	}
	seen := make(map[string]struct{}, len(request.ReadOnlyRoots)+len(request.ReadOnlyFiles)+len(request.WritableFiles))
	for _, root := range request.ReadOnlyRoots {
		key := strings.ToLower(root)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("Windows kernel authority contains duplicate paths")
		}
		seen[key] = struct{}{}
	}
	for _, group := range [][]string{request.ReadOnlyFiles, request.WritableFiles} {
		for _, path := range group {
			if err := validateWindowsKernelPath(path, mustExist, true); err != nil {
				return fmt.Errorf("Windows kernel file grant: %w", err)
			}
			if windowsKernelPathsOverlap(path, request.Workspace) || windowsKernelPathsOverlap(path, request.Executable) {
				return errors.New("Windows kernel file grant overlaps the workspace or executable")
			}
			key := strings.ToLower(path)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("Windows kernel authority contains duplicate paths")
			}
			seen[key] = struct{}{}
		}
	}
	if len(request.Frozen) > 16 {
		return errors.New("Windows kernel frozen grants exceed the bounded contract")
	}
	frozenSeen := make(map[string]struct{}, len(request.Frozen))
	for _, frozen := range request.Frozen {
		key := strings.ToLower(frozen.Path)
		if _, valid := seen[key]; !valid || windowsKernelPathsOverlap(frozen.Path, request.Workspace) ||
			windowsKernelPathsOverlap(frozen.Path, request.Executable) {
			return errors.New("Windows kernel frozen path has no matching grant")
		}
		if _, duplicate := frozenSeen[key]; duplicate {
			return errors.New("Windows kernel frozen path is duplicated")
		}
		frozenSeen[key] = struct{}{}
	}
	return nil
}

func validateWindowsConfinementAuthorityBindings(request *windowsConfinedRequest) error {
	grants := windowsConfinementRequestGrants(request)
	if len(request.Authority) != len(grants) {
		return errors.New("Windows kernel authority identities are incomplete")
	}
	for index, grant := range grants {
		if !strings.EqualFold(request.Authority[index].Path, grant.path) {
			return errors.New("Windows kernel authority identity path is invalid")
		}
	}
	for _, frozen := range request.Frozen {
		found := false
		for _, authority := range request.Authority {
			if strings.EqualFold(frozen.Path, authority.Path) {
				if !sameWindowsConfinementIdentity(frozen, authority) {
					return errors.New("Windows kernel frozen mount differs from bound authority")
				}
				found = true
				break
			}
		}
		if !found {
			return errors.New("Windows kernel frozen mount has no bound authority")
		}
	}
	return nil
}

func sameWindowsConfinementIdentity(left, right windowsConfinementFrozenPath) bool {
	return left.Volume == right.Volume && left.IndexHigh == right.IndexHigh && left.IndexLow == right.IndexLow
}

func windowsConfinementPathIdentity(path string) (windowsConfinementFrozenPath, error) {
	handle, release, err := openWindowsConfinementPath(path, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return windowsConfinementFrozenPath{}, err
	}
	defer release()
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return windowsConfinementFrozenPath{}, err
	}
	return windowsConfinementFrozenPath{
		Path: path, Volume: identity.VolumeSerialNumber,
		IndexHigh: identity.FileIndexHigh, IndexLow: identity.FileIndexLow,
	}, nil
}

func verifyWindowsConfinementAuthorityBindings(request *windowsConfinedRequest) error {
	if err := validateWindowsConfinementAuthorityBindings(request); err != nil {
		return err
	}
	for _, expected := range request.Authority {
		actual, err := windowsConfinementPathIdentity(expected.Path)
		if err != nil || !sameWindowsConfinementIdentity(expected, actual) {
			return fmt.Errorf("%w: bound authority was replaced", errWindowsConfinementPathReplaced)
		}
	}
	return nil
}

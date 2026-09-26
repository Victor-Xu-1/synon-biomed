package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

const hostGrantsSettingKey = "hostAccess.grants"

var errHostPickerCancelled = errors.New("host directory picker was cancelled")

type hostPickerCommand struct {
	name string
	args []string
	wsl  bool
}

type hostGrant struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Server) handleHostGrants(w http.ResponseWriter, r *http.Request) {
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "settings store is not configured"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		grants, err := s.loadHostGrants(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "grants": grants})
	case http.MethodPost, http.MethodPatch:
		var input struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		path, err := canonicalHostDirectory(input.Path)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		mode := strings.TrimSpace(input.Mode)
		if mode == "" {
			mode = "read"
		}
		if mode != "read" && mode != "read_write" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "host grant mode must be read or read_write"})
			return
		}
		grant, err := s.upsertHostGrant(userID, path, mode)
		if err != nil {
			if errors.Is(err, kernelruntime.ErrProtectedHostMount) {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		action := "granted"
		if r.Method == http.MethodPatch {
			action = "updated"
		}
		if _, err := s.publishUserEvent(userID, "host_access_granted", map[string]any{"grant_id": grant.ID, "path": grant.Path, "mode": grant.Mode, "action": action}); err != nil {
			writeDomainEventError(w, "host grant update", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
	case http.MethodDelete:
		var input struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		target := firstNonEmpty(input.Path, input.ID)
		path, err := canonicalHostGrantReference(target)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		removed, err := s.revokeHostGrant(userID, path)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !removed {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "host grant was not found"})
			return
		}
		if _, err := s.publishUserEvent(userID, "host_access_granted", map[string]any{"path": path, "action": "revoked"}); err != nil {
			writeDomainEventError(w, "host grant revoke", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func canonicalHostGrantReference(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") || !filepath.IsAbs(value) {
		return "", errors.New("host grant path must be absolute")
	}
	clean := filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved), nil
	}
	return clean, nil
}

func (s *Server) handleHostGrantPicker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "settings store is not configured"})
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	var input struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	selected := strings.TrimSpace(input.Path)
	if selected == "" {
		picker := s.hostDirectoryPicker
		if picker == nil {
			picker = defaultHostDirectoryPicker
		}
		var err error
		selected, err = picker(r.Context())
		if errors.Is(err, errHostPickerCancelled) {
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": true})
			return
		}
		if err != nil {
			writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	selected, err := canonicalHostDirectory(selected)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = "read"
	}
	if mode != "read" && mode != "read_write" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "host grant mode must be read or read_write"})
		return
	}
	grant, err := s.upsertHostGrant(userID, selected, mode)
	if err != nil {
		if errors.Is(err, kernelruntime.ErrProtectedHostMount) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.publishUserEvent(userID, "host_access_granted", map[string]any{"grant_id": grant.ID, "path": grant.Path, "mode": grant.Mode, "action": "granted"}); err != nil {
		writeDomainEventError(w, "host grant picker", err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
}

func defaultHostDirectoryPicker(ctx context.Context) (string, error) {
	candidates := make([]hostPickerCommand, 0, 3)
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates, hostPickerCommand{name: "powershell.exe", args: windowsFolderPickerArguments()})
	case "darwin":
		candidates = append(candidates, hostPickerCommand{name: "osascript", args: []string{"-e", `POSIX path of (choose folder with prompt "Select a directory for Synon access")`}})
	default:
		if isWSLRuntime() {
			candidates = append(candidates, hostPickerCommand{name: "powershell.exe", args: windowsFolderPickerArguments(), wsl: true})
		}
		candidates = append(candidates,
			hostPickerCommand{name: "zenity", args: []string{"--file-selection", "--directory", "--title=Select a directory for Synon access"}},
			hostPickerCommand{name: "kdialog", args: []string{"--getexistingdirectory", ".", "--title", "Select a directory for Synon access"}},
		)
	}
	return runHostDirectoryPickerCandidates(ctx, candidates)
}

func runHostDirectoryPickerCandidates(ctx context.Context, candidates []hostPickerCommand) (string, error) {
	var lastFailure error
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		stdout := &cappedCommandBuffer{max: 32 << 10}
		stderr := &cappedCommandBuffer{max: 4 << 10}
		command := exec.CommandContext(ctx, path, candidate.args...)
		command.Stdout = stdout
		command.Stderr = stderr
		err = command.Run()
		selected := strings.TrimSpace(stdout.String())
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			detail := strings.TrimSpace(stderr.String())
			var exitError *exec.ExitError
			if selected == "" && detail == "" && errors.As(err, &exitError) && (exitError.ExitCode() == 1 || exitError.ExitCode() == 2) {
				return "", errHostPickerCancelled
			}
			if detail == "" {
				detail = err.Error()
			}
			lastFailure = fmt.Errorf("%s directory picker failed: %s", candidate.name, detail)
			continue
		}
		if selected == "" {
			return "", errHostPickerCancelled
		}
		if candidate.wsl {
			selected, err = windowsPathToWSL(ctx, selected)
			if err != nil {
				return "", err
			}
		}
		return selected, nil
	}
	if lastFailure != nil {
		return "", lastFailure
	}
	return "", errors.New("no native directory picker is available; use the Web directory selector or submit an authorized path")
}

func windowsFolderPickerArguments() []string {
	script := `Add-Type -AssemblyName System.Windows.Forms; $dialog = New-Object System.Windows.Forms.FolderBrowserDialog; $dialog.Description = 'Select a directory for Synon access'; if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Write-Output $dialog.SelectedPath } else { exit 2 }`
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}

func isWSLRuntime() bool {
	if strings.TrimSpace(os.Getenv("WSL_INTEROP")) != "" || strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft")
}

func windowsPathToWSL(ctx context.Context, selected string) (string, error) {
	path, err := exec.LookPath("wslpath")
	if err != nil {
		return "", errors.New("wslpath is required to convert the selected Windows directory")
	}
	stdout := &cappedCommandBuffer{max: 32 << 10}
	stderr := &cappedCommandBuffer{max: 4 << 10}
	command := exec.CommandContext(ctx, path, "-u", selected)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("convert selected Windows directory: %s", strings.TrimSpace(stderr.String()))
	}
	converted := strings.TrimSpace(stdout.String())
	if converted == "" {
		return "", errors.New("selected Windows directory could not be converted")
	}
	return converted, nil
}

func (s *Server) handleHostHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if evaluated, err := filepath.EvalSymlinks(home); err == nil {
		home = evaluated
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "path": filepath.Clean(home)})
}

func (s *Server) handleHostBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	target, err := canonicalHostDirectory(r.URL.Query().Get("path"))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	allowed := false
	for _, grant := range grants {
		if hostPathWithin(grant.Path, target) {
			allowed = true
			break
		}
	}
	if !allowed {
		writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "path is outside granted host directories"})
		return
	}
	// target is an existing, symlink-resolved directory inside a persisted host
	// grant; hostPathWithin performed the authorization check above.
	// codeql[go/path-injection]
	items, err := os.ReadDir(target)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	entries := make([]map[string]any, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		entries = append(entries, map[string]any{
			"name": item.Name(), "path": filepath.Join(target, item.Name()),
			"isDirectory": item.IsDir(), "isSymlink": item.Type()&os.ModeSymlink != 0,
			"size": info.Size(), "mode": info.Mode().Perm().String(),
			"modifiedAt": info.ModTime().UTC(),
		})
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "path": target, "entries": entries})
}

func canonicalHostDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("host directory path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("host directory does not exist")
	}
	// evaluated is an absolute path whose symlink chain has already been
	// resolved by canonicalHostDirectory.
	// codeql[go/path-injection]
	info, err := os.Stat(evaluated)
	if err != nil || !info.IsDir() {
		return "", errors.New("host path must be an existing directory")
	}
	return filepath.Clean(evaluated), nil
}

func hostPathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func (s *Server) loadHostGrants(userID string) ([]hostGrant, error) {
	setting, found, err := s.settingsStore.Get(hostGrantsSettingKeyForUser(userID))
	if err != nil || !found {
		return []hostGrant{}, err
	}
	return decodeHostGrants(setting.Value)
}

func decodeHostGrants(value any) ([]hostGrant, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var grants []hostGrant
	if err := json.Unmarshal(raw, &grants); err != nil {
		return nil, err
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Path < grants[j].Path })
	return grants, nil
}

func (s *Server) upsertHostGrant(userID, path, mode string) (hostGrant, error) {
	selected, _, err := s.upsertHostGrantWithReceipt(userID, path, mode)
	return selected, err
}

type hostGrantMutationReceipt struct {
	UserID  string
	Before  *hostGrant
	After   hostGrant
	Changed bool
}

func (s *Server) upsertHostGrantWithReceipt(userID, path, mode string) (hostGrant, hostGrantMutationReceipt, error) {
	var err error
	path, err = s.validateHostGrantProtectedPaths(path)
	if err != nil {
		return hostGrant{}, hostGrantMutationReceipt{}, err
	}
	var selected hostGrant
	var before *hostGrant
	changed := false
	err = s.commitHostGrantMutation(userID, func() (bool, error) {
		_, err := s.settingsStore.Update(hostGrantsSettingKeyForUser(userID), func(current any, found bool) (any, error) {
			grants := []hostGrant{}
			if found {
				var err error
				grants, err = decodeHostGrants(current)
				if err != nil {
					return nil, err
				}
			}
			now := time.Now().UTC()
			for index := range grants {
				if grants[index].Path != path {
					continue
				}
				copy := grants[index]
				before = &copy
				selected = grants[index]
				if grants[index].Mode == mode {
					return grants, nil
				}
				grants[index].Mode = mode
				grants[index].UpdatedAt = now
				selected = grants[index]
				changed = true
				return grants, nil
			}
			selected = hostGrant{ID: path, Path: path, Mode: mode, CreatedAt: now, UpdatedAt: now}
			grants = append(grants, selected)
			changed = true
			return grants, nil
		})
		return changed, err
	})
	if err != nil {
		return hostGrant{}, hostGrantMutationReceipt{}, err
	}
	return selected, hostGrantMutationReceipt{
		UserID: strings.TrimSpace(userID), Before: before, After: selected, Changed: changed,
	}, nil
}

func (s *Server) rollbackHostGrantMutation(receipt hostGrantMutationReceipt) error {
	if !receipt.Changed {
		return nil
	}
	return s.commitHostGrantMutation(receipt.UserID, func() (bool, error) {
		changed := false
		_, err := s.settingsStore.Update(hostGrantsSettingKeyForUser(receipt.UserID), func(current any, found bool) (any, error) {
			if !found {
				return nil, errors.New("host grant rollback conflicts with current authority")
			}
			grants, err := decodeHostGrants(current)
			if err != nil {
				return nil, err
			}
			index := -1
			for candidate := range grants {
				if grants[candidate].Path == receipt.After.Path {
					index = candidate
					break
				}
			}
			if index < 0 || grants[index].Mode != receipt.After.Mode || !grants[index].UpdatedAt.Equal(receipt.After.UpdatedAt) {
				return nil, errors.New("host grant rollback conflicts with current authority")
			}
			if receipt.Before == nil {
				grants = append(grants[:index], grants[index+1:]...)
			} else {
				grants[index] = *receipt.Before
			}
			changed = true
			return grants, nil
		})
		return changed, err
	})
}

func (s *Server) validateHostGrantProtectedPaths(path string) (string, error) {
	path, err := canonicalHostDirectory(path)
	if err != nil {
		return "", err
	}
	if err := kernelruntime.ValidateHostMountPath(path); err != nil {
		return "", err
	}
	protectedPaths, err := s.agentKernelProtectedPaths()
	if err != nil {
		return "", err
	}
	for _, protected := range protectedPaths {
		if hostPathWithin(protected, path) || hostPathWithin(path, protected) {
			return "", errors.New("host grant overlaps protected application data")
		}
	}
	return path, nil
}

func (s *Server) revokeHostGrant(userID, path string) (bool, error) {
	removed := false
	err := s.commitHostGrantMutation(userID, func() (bool, error) {
		_, err := s.settingsStore.Update(hostGrantsSettingKeyForUser(userID), func(current any, found bool) (any, error) {
			if !found {
				return []hostGrant{}, nil
			}
			grants, err := decodeHostGrants(current)
			if err != nil {
				return nil, err
			}
			next := make([]hostGrant, 0, len(grants))
			for _, grant := range grants {
				if grant.Path == path {
					removed = true
					continue
				}
				next = append(next, grant)
			}
			return next, nil
		})
		return removed, err
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

func (s *Server) commitHostGrantMutation(userID string, mutate func() (bool, error)) error {
	return s.commitKernelConfinementMutation(userID, mutate)
}

// commitKernelConfinementMutation serializes any authority change that alters
// a kernel's mount or network sandbox and invalidates already-running owner
// kernels before returning. A subsequent tool therefore receives one coherent
// confinement snapshot instead of waiting on a same-ID kernel whose policy can
// never match. Idempotent mutations leave live kernels untouched.
func (s *Server) commitKernelConfinementMutation(userID string, mutate func() (bool, error)) error {
	if s == nil || mutate == nil {
		return errors.New("kernel confinement mutation is unavailable")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("host grant owner is required")
	}
	s.hostGrantKernelMu.Lock()
	changed, err := mutate()
	if err != nil || !changed {
		s.hostGrantKernelMu.Unlock()
		return err
	}
	if s.hostGrantKernelFences == nil {
		s.hostGrantKernelFences = map[string]bool{}
	}
	if s.hostGrantKernelFenceEpoch == nil {
		s.hostGrantKernelFenceEpoch = map[string]uint64{}
	}
	s.hostGrantKernelFenceEpoch[userID]++
	fenceEpoch := s.hostGrantKernelFenceEpoch[userID]
	s.hostGrantKernelFences[userID] = true
	s.hostGrantKernelMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var terminationErr error
	if s.kernelManager != nil {
		if err := func() error { _, err := s.kernelManager.TerminateOwner(ctx, userID); return err }(); err != nil {
			terminationErr = errors.Join(terminationErr, err)
		}
	}
	if revoker, ok := s.kernelExecutionBackend.(interface {
		TerminateOwner(context.Context, string) error
	}); ok {
		if err := revoker.TerminateOwner(ctx, userID); err != nil {
			terminationErr = errors.Join(terminationErr, err)
		}
	}
	s.hostGrantKernelMu.Lock()
	defer s.hostGrantKernelMu.Unlock()
	if terminationErr == nil && s.hostGrantKernelFenceEpoch[userID] == fenceEpoch {
		delete(s.hostGrantKernelFences, userID)
	}
	if terminationErr != nil {
		return errors.New("host grant changed but active kernel isolation could not be invalidated")
	}
	return nil
}

func hostGrantsSettingKeyForUser(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || userID == "local" {
		return hostGrantsSettingKey
	}
	digest := sha256.Sum256([]byte(userID))
	return hostGrantsSettingKey + "." + hex.EncodeToString(digest[:8])
}

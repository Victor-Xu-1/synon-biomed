package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	kernelruntime "synon-go/internal/kernel"
)

var errCompatibilityHostGrantNotFound = errors.New("compatibility host grant not found")

type compatibilityHostGrant struct {
	ID        string `json:"id"`
	HostPath  string `json:"hostPath"`
	MountName string `json:"mountName"`
	GuestPath string `json:"guestPath"`
	Mode      string `json:"mode"`
}

type compatibilityHostGrantInput struct {
	Path        string
	PathPresent bool
	Mode        string
	ModePresent bool
}

type compatibilityHostGrantModeChange struct {
	compatibilityHostGrant
	CreatedAt string `json:"createdAt"`
}

type compatibilityHostDirectoryEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"isDirectory"`
}

func (s *Server) handleCompatibilityHostGrants(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Settings store is not configured")
		return
	}
	userID := compatAgentUserID(r)
	switch r.Method {
	case http.MethodGet:
		grants, err := s.loadHostGrants(userID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		projected := make([]compatibilityHostGrant, 0, len(grants))
		for _, grant := range grants {
			projected = append(projected, projectCompatibilityHostGrant(grant))
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": projected})
	case http.MethodPost:
		input, ok := decodeCompatibilityHostGrantInput(w, r)
		if !ok {
			return
		}
		if !input.PathPresent {
			writeV11Detail(w, http.StatusBadRequest, "path: string required")
			return
		}
		if !compatibilityHostGrantMode(input.Mode, input.ModePresent) {
			writeV11Detail(w, http.StatusBadRequest, "mode: 'ro' | 'rw' required")
			return
		}
		grant, err := s.grantCompatibilityHostPath(userID, input.Path, input.Mode)
		if err != nil {
			if errors.Is(err, kernelruntime.ErrProtectedHostMount) {
				writeV11Detail(w, http.StatusBadRequest, err.Error())
				return
			}
			writeV11Detail(w, http.StatusBadRequest, "Directory could not be accessed.")
			return
		}
		writeJSON(w, http.StatusOK, projectCompatibilityHostGrant(grant))
	case http.MethodPatch:
		input, ok := decodeCompatibilityHostGrantInput(w, r)
		if !ok {
			return
		}
		if !input.PathPresent {
			writeV11Detail(w, http.StatusBadRequest, "path: string required")
			return
		}
		if !compatibilityHostGrantMode(input.Mode, input.ModePresent) {
			writeV11Detail(w, http.StatusBadRequest, "mode: 'ro' | 'rw' required")
			return
		}
		grant, found, err := s.changeCompatibilityHostGrantMode(userID, input.Path, input.Mode)
		if err != nil {
			if errors.Is(err, kernelruntime.ErrProtectedHostMount) {
				writeV11Detail(w, http.StatusBadRequest, err.Error())
				return
			}
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "No grant at that path that you can modify.")
			return
		}
		writeJSON(w, http.StatusOK, compatibilityHostGrantModeChange{
			compatibilityHostGrant: projectCompatibilityHostGrant(grant),
			CreatedAt:              time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	case http.MethodDelete:
		input, ok := decodeCompatibilityHostGrantInput(w, r)
		if !ok {
			return
		}
		if !input.PathPresent {
			writeV11Detail(w, http.StatusBadRequest, "path: string required")
			return
		}
		path, err := canonicalHostGrantReference(input.Path)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, "path: absolute string required")
			return
		}
		removed, err := s.revokeHostGrant(userID, path)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !removed {
			writeV11Detail(w, http.StatusNotFound, "No grant at that path that you can revoke.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleCompatibilityHostHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": filepath.Clean(home)})
}

func (s *Server) handleCompatibilityHostBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	userID := compatAgentUserID(r)
	values, present := r.URL.Query()["path"]
	if !present || len(values) == 0 || strings.Contains(values[0], "..") {
		writeV11Detail(w, http.StatusBadRequest, "absolute path required")
		return
	}
	entries, err := s.listCompatibilityHostDirectory(userID, values[0])
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleCompatibilityHostGrantPicker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.settingsStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Settings store is not configured")
		return
	}
	input, ok := decodeCompatibilityHostGrantInput(w, r)
	if !ok {
		return
	}
	if !compatibilityHostGrantMode(input.Mode, input.ModePresent) {
		writeV11Detail(w, http.StatusBadRequest, "mode: 'ro' | 'rw' required")
		return
	}
	selected := input.Path
	if !input.PathPresent || strings.TrimSpace(selected) == "" {
		picker := s.hostDirectoryPicker
		if picker == nil {
			picker = defaultHostDirectoryPicker
		}
		var err error
		selected, err = picker(r.Context())
		if errors.Is(err, errHostPickerCancelled) {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		if err != nil {
			writeV11Detail(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	grant, err := s.grantCompatibilityHostPath(compatAgentUserID(r), selected, input.Mode)
	if err != nil {
		if errors.Is(err, kernelruntime.ErrProtectedHostMount) {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV11Detail(w, http.StatusBadRequest, "Directory could not be accessed.")
		return
	}
	writeJSON(w, http.StatusOK, projectCompatibilityHostGrant(grant))
}

func decodeCompatibilityHostGrantInput(w http.ResponseWriter, r *http.Request) (compatibilityHostGrantInput, bool) {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	var body map[string]json.RawMessage
	if err := decoder.Decode(&body); err != nil || body == nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid request body")
		return compatibilityHostGrantInput{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeV11Detail(w, http.StatusBadRequest, "Invalid request body")
		return compatibilityHostGrantInput{}, false
	}
	input := compatibilityHostGrantInput{}
	if raw, present := body["path"]; present {
		input.PathPresent = true
		if err := json.Unmarshal(raw, &input.Path); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "path: string required")
			return compatibilityHostGrantInput{}, false
		}
	}
	if raw, present := body["mode"]; present {
		input.ModePresent = true
		if err := json.Unmarshal(raw, &input.Mode); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "mode: 'ro' | 'rw' required")
			return compatibilityHostGrantInput{}, false
		}
	}
	return input, true
}

func compatibilityHostGrantMode(mode string, present bool) bool {
	return present && (mode == "ro" || mode == "rw")
}

func (s *Server) grantCompatibilityHostPath(userID, requestedPath, mode string) (hostGrant, error) {
	canonical, err := canonicalCompatibilityHostDirectory(requestedPath)
	if err != nil {
		return hostGrant{}, err
	}
	internalMode := "read"
	if mode == "rw" {
		internalMode = "read_write"
	}
	return s.upsertHostGrant(userID, canonical, internalMode)
}

func (s *Server) changeCompatibilityHostGrantMode(userID, path, mode string) (hostGrant, bool, error) {
	var err error
	path, err = s.validateHostGrantProtectedPaths(path)
	if err != nil {
		return hostGrant{}, false, err
	}
	selected := hostGrant{}
	err = s.commitHostGrantMutation(userID, func() (bool, error) {
		changed := false
		_, err := s.settingsStore.Update(hostGrantsSettingKeyForUser(userID), func(current any, found bool) (any, error) {
			if !found {
				return nil, errCompatibilityHostGrantNotFound
			}
			grants, err := decodeHostGrants(current)
			if err != nil {
				return nil, err
			}
			internalMode := "read"
			if mode == "rw" {
				internalMode = "read_write"
			}
			for index := range grants {
				if grants[index].Path != path {
					continue
				}
				if grants[index].Mode == internalMode {
					selected = grants[index]
					return grants, nil
				}
				grants[index].Mode = internalMode
				grants[index].UpdatedAt = time.Now().UTC()
				selected = grants[index]
				changed = true
				return grants, nil
			}
			return nil, errCompatibilityHostGrantNotFound
		})
		return changed, err
	})
	if errors.Is(err, errCompatibilityHostGrantNotFound) {
		return hostGrant{}, false, nil
	}
	if err != nil {
		return hostGrant{}, false, err
	}
	return selected, true, nil
}

func (s *Server) listCompatibilityHostDirectory(userID, requestedPath string) ([]compatibilityHostDirectoryEntry, error) {
	if !filepath.IsAbs(requestedPath) {
		return nil, errors.New("path must be absolute")
	}
	target, err := filepath.EvalSymlinks(requestedPath)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", requestedPath, err)
	}
	target = filepath.Clean(target)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if evaluated, evaluateErr := filepath.EvalSymlinks(home); evaluateErr == nil {
		home = evaluated
	}
	allowed := hostPathWithin(filepath.Clean(home), target)
	if !allowed {
		grants, loadErr := s.loadHostGrants(userID)
		if loadErr != nil {
			return nil, loadErr
		}
		for _, grant := range grants {
			if hostPathWithin(grant.Path, target) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return nil, errors.New("Directory is not under $HOME or a granted root.")
	}
	// target is symlink-resolved and is admitted only under the canonical home
	// directory or a persisted grant owned by userID.
	// codeql[go/path-injection]
	items, err := os.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", target, err)
	}
	entries := make([]compatibilityHostDirectoryEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, compatibilityHostDirectoryEntry{
			Name: item.Name(), IsDirectory: item.IsDir(),
		})
	}
	nameCollator := collate.New(language.English)
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].IsDirectory != entries[right].IsDirectory {
			return entries[left].IsDirectory
		}
		return nameCollator.CompareString(entries[left].Name, entries[right].Name) < 0
	})
	return entries, nil
}

func canonicalCompatibilityHostDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("host directory path must be absolute")
	}
	return canonicalHostDirectory(value)
}

func projectCompatibilityHostGrant(grant hostGrant) compatibilityHostGrant {
	mode := "ro"
	if grant.Mode == "rw" || grant.Mode == "read_write" {
		mode = "rw"
	}
	mountName := filepath.Base(grant.Path)
	if mountName == "." || mountName == "" {
		mountName = grant.Path
	}
	return compatibilityHostGrant{
		ID: grant.Path, HostPath: grant.Path, MountName: mountName, GuestPath: grant.Path, Mode: mode,
	}
}

package mcpdirectory

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"synon-go/internal/tools/mcpstdio"
)

const (
	optionalInstallStateVersion = 1
	optionalInstallOutputLimit  = 48 * 1024
	optionalInstallTimeout      = 30 * time.Minute
)

// optionalConnectorDefinition is trusted, repository-owned metadata. It is
// intentionally separate from the bundled connector roster so an uninstalled
// local tool can never enter the model-visible MCP pool.
type optionalConnectorDefinition struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	License       string   `json:"license"`
	RepositoryURL string   `json:"repositoryUrl"`
	InstallKind   string   `json:"installKind"`
	InstallLabel  string   `json:"installLabel"`
	SizeLabel     string   `json:"sizeLabel"`
	Requirements  []string `json:"requirements"`
	SourceRef     string   `json:"sourceRef"`
}

type optionalInstallState struct {
	SchemaVersion int       `json:"schemaVersion"`
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	InstallRoot   string    `json:"installRoot"`
	InstalledAt   time.Time `json:"installedAt,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Error         string    `json:"error,omitempty"`
}

type OptionalConnector struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	License       string   `json:"license"`
	RepositoryURL string   `json:"repositoryUrl"`
	InstallKind   string   `json:"installKind"`
	InstallLabel  string   `json:"installLabel"`
	SizeLabel     string   `json:"sizeLabel"`
	Requirements  []string `json:"requirements"`
	SourceRef     string   `json:"sourceRef"`
	Status        string   `json:"status"`
	Installed     bool     `json:"installed"`
	InstallPath   string   `json:"installPath,omitempty"`
	Error         string   `json:"error,omitempty"`
}

//go:embed optional_metadata.json
var optionalMetadataJSON []byte

func loadOptionalConnectorDefinitions() map[string]optionalConnectorDefinition {
	var definitions []optionalConnectorDefinition
	if err := json.Unmarshal(optionalMetadataJSON, &definitions); err != nil {
		panic("decode optional MCP connector metadata: " + err.Error())
	}
	out := make(map[string]optionalConnectorDefinition, len(definitions))
	for _, definition := range definitions {
		definition.ID = strings.TrimSpace(definition.ID)
		if definition.ID == "" || definition.Name == "" || out[definition.ID].ID != "" {
			panic("invalid or duplicate optional MCP connector metadata")
		}
		out[definition.ID] = definition
	}
	return out
}

func registerOptionalConnectorDefinitions(root string, bundled map[string]bundledConnector, definitions map[string]optionalConnectorDefinition) {
	for _, definition := range definitions {
		installRoot := optionalConnectorInstallRoot(root, definition.ID)
		item := bundledConnector{
			ID: definition.ID, Name: definition.Name, DisplayName: definition.DisplayName,
			Description: definition.Description, Deferred: true, OptionalID: definition.ID,
			DefinitionSHA256: optionalDefinitionSHA256(definition),
		}
		switch definition.ID {
		case "bundled:renkin-local":
			item.Config = mcpstdio.ServerConfig{
				Type: "stdio", Scope: "bundled", Command: filepath.Join(installRoot, "bin", executableName("renkin-mcp")),
			}
		case "bundled:rna-design-local":
			adapter := filepath.Join(filepath.Dir(discoverBioToolsRoot()), "rna-design", "run_server.py")
			item.useBundledPython = true
			item.Config = mcpstdio.ServerConfig{
				Type: "stdio", Scope: "bundled", Command: "python3", Args: []string{adapter},
				Env: map[string]string{"SYNON_VIENNARNA_PACKAGE_ROOT": installRoot},
			}
		default:
			continue
		}
		bundled[item.ID] = item
	}
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func optionalDefinitionSHA256(definition optionalConnectorDefinition) string {
	raw, _ := json.Marshal(definition)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func optionalConnectorInstallRoot(root, connectorID string) string {
	slug := strings.TrimPrefix(strings.TrimSpace(connectorID), "bundled:")
	return filepath.Join(root, "mcp-servers", "managed", slug)
}

func optionalConnectorStatePath(root, connectorID string) string {
	return filepath.Join(optionalConnectorInstallRoot(root, connectorID), ".install-state.json")
}

func loadInstalledOptionalConnectors(root string, bundled map[string]bundledConnector, definitions map[string]optionalConnectorDefinition) {
	registerOptionalConnectorDefinitions(root, bundled, definitions)
}

func (s *Service) optionalConnectorInstalled(item bundledConnector) bool {
	if !item.Deferred {
		return true
	}
	state, ok := s.readOptionalInstallState(item.OptionalID)
	if !ok || state.Status != "installed" || state.InstallRoot == "" ||
		filepath.Clean(state.InstallRoot) != filepath.Clean(optionalConnectorInstallRoot(s.root, item.OptionalID)) {
		return false
	}
	definition, ok := s.optional[item.OptionalID]
	return ok && s.optionalInstallTargetValid(definition, state.InstallRoot)
}

func (s *Service) readOptionalInstallState(id string) (optionalInstallState, bool) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return optionalInstallState{}, false
	}
	raw, err := os.ReadFile(optionalConnectorStatePath(s.root, id))
	if err != nil {
		return optionalInstallState{}, false
	}
	var state optionalInstallState
	if json.Unmarshal(raw, &state) != nil || state.SchemaVersion != optionalInstallStateVersion || state.ID != id {
		return optionalInstallState{}, false
	}
	return state, true
}

func (s *Service) ListOptionalConnectors(_ context.Context) ([]OptionalConnector, error) {
	if s == nil {
		return nil, errors.New("MCP directory service is not configured")
	}
	items := make([]OptionalConnector, 0, len(s.optional))
	for _, definition := range s.optional {
		items = append(items, s.optionalProjection(definition))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *Service) optionalProjection(definition optionalConnectorDefinition) OptionalConnector {
	state, found := s.readOptionalInstallState(definition.ID)
	status := "not-installed"
	installed := false
	installPath := ""
	if found {
		status = state.Status
		installed = state.Status == "installed"
		if state.InstallRoot != "" && s.root != "" {
			if relative, err := filepath.Rel(s.root, state.InstallRoot); err == nil && !strings.HasPrefix(relative, "..") {
				installPath = filepath.ToSlash(relative)
			}
		}
		if installed {
			item, ok := s.bundled[definition.ID]
			if !ok || !s.optionalConnectorInstalled(item) {
				status, installed = "broken", false
			}
		}
	}
	return OptionalConnector{
		ID: definition.ID, Name: definition.Name, DisplayName: definition.DisplayName,
		Description: definition.Description, Category: definition.Category, License: definition.License,
		RepositoryURL: definition.RepositoryURL, InstallKind: definition.InstallKind,
		InstallLabel: definition.InstallLabel, SizeLabel: definition.SizeLabel,
		Requirements: append([]string(nil), definition.Requirements...), SourceRef: definition.SourceRef,
		Status: status, Installed: installed, InstallPath: installPath,
		Error: state.Error,
	}
}

// StartOptionalInstall starts one allow-listed installer. The installer is
// asynchronous because compiling RENKIN can outlive an HTTP request. State is
// written before and after the operation so refresh/restart never presents a
// false connected MCP.
func (s *Service) StartOptionalInstall(userID, id string) (OptionalConnector, error) {
	if s == nil || s.store == nil {
		return OptionalConnector{}, errors.New("MCP directory service is not configured")
	}
	definition, found := s.optional[strings.TrimSpace(id)]
	if !found {
		return OptionalConnector{}, errors.New("optional MCP connector not found")
	}
	s.optionalInstallMu.Lock()
	defer s.optionalInstallMu.Unlock()
	installRoot := optionalConnectorInstallRoot(s.root, id)
	if strings.TrimSpace(s.root) == "" || filepath.Clean(installRoot) == filepath.Clean(s.root) {
		return OptionalConnector{}, errors.New("application data root is required for optional MCP installation")
	}
	if state, ok := s.readOptionalInstallState(id); ok {
		switch state.Status {
		case "installed":
			return s.optionalProjection(definition), nil
		case "installing":
			// Reconcile activation if a process stopped after the atomic rename.
			if s.optionalInstallTargetValid(definition, installRoot) {
				state.Status, state.InstalledAt, state.UpdatedAt, state.Error = "installed", time.Now().UTC(), time.Now().UTC(), ""
				if err := writeOptionalInstallState(s.root, state); err != nil {
					return OptionalConnector{}, err
				}
				return s.optionalProjection(definition), nil
			}
		case "failed", "broken":
			// Only remove the exact app-owned target left by a failed retry.
			if _, err := os.Stat(installRoot); err == nil {
				if err := os.RemoveAll(installRoot); err != nil {
					return OptionalConnector{}, fmt.Errorf("clear failed optional MCP install: %w", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return OptionalConnector{}, fmt.Errorf("inspect failed optional MCP install: %w", err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(installRoot), 0o700); err != nil {
		return OptionalConnector{}, fmt.Errorf("create optional MCP install directory: %w", err)
	}
	state := optionalInstallState{SchemaVersion: optionalInstallStateVersion, ID: id, Status: "installing", InstallRoot: installRoot, UpdatedAt: time.Now().UTC()}
	if err := writeOptionalInstallState(s.root, state); err != nil {
		return OptionalConnector{}, err
	}
	installCtx := s.optionalInstallCtx
	if installCtx == nil {
		installCtx = context.Background()
	}
	s.optionalInstallWG.Add(1)
	go func() {
		defer s.optionalInstallWG.Done()
		ctx, cancel := context.WithTimeout(installCtx, optionalInstallTimeout)
		defer cancel()
		if err := s.installOptionalConnector(ctx, definition, installRoot); err != nil {
			state.Status, state.Error, state.UpdatedAt = "failed", truncateOptionalError(err), time.Now().UTC()
			_ = writeOptionalInstallState(s.root, state)
			return
		}
		state.Status, state.InstalledAt, state.UpdatedAt, state.Error = "installed", time.Now().UTC(), time.Now().UTC(), ""
		if err := writeOptionalInstallState(s.root, state); err != nil {
			return
		}
		probeCtx, probeCancel := context.WithTimeout(context.Background(), bundledBootstrapTimeout)
		defer probeCancel()
		if err := s.ProbeMissingBundled(probeCtx, userID); err != nil {
			// Installation remains installed; the connector health endpoint will
			// expose the probe failure and a refresh/reconcile can retry it.
			state.setProbeError(err)
			_ = writeOptionalInstallState(s.root, state)
		}
	}()
	return s.optionalProjection(definition), nil
}

func (s *Service) optionalInstallTargetValid(definition optionalConnectorDefinition, root string) bool {
	switch definition.ID {
	case "bundled:renkin-local":
		info, err := os.Stat(filepath.Join(root, "bin", executableName("renkin-mcp")))
		return err == nil && info.Mode().IsRegular()
	case "bundled:rna-design-local":
		_, err := os.Stat(filepath.Join(root, "RNA"))
		return err == nil
	default:
		return false
	}
}

func (s *Service) installOptionalConnector(ctx context.Context, definition optionalConnectorDefinition, target string) error {
	parent := filepath.Dir(target)
	staging, err := os.MkdirTemp(parent, ".install-"+strings.TrimPrefix(definition.Name, ".")+"-")
	if err != nil {
		return fmt.Errorf("create optional MCP staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	var command *exec.Cmd
	switch definition.InstallKind {
	case "cargo-git":
		command = exec.CommandContext(ctx, "cargo", "install", "--git", definition.RepositoryURL, "--locked", "--bin", "renkin-mcp", "--root", staging)
	case "python-wheel":
		s.mu.Lock()
		pythonProvider := s.bundledPython
		s.mu.Unlock()
		python := "python3"
		if pythonProvider != nil {
			python, err = pythonProvider()
			if err != nil {
				return fmt.Errorf("managed Python runtime is unavailable: %w", err)
			}
		}
		command = exec.CommandContext(ctx, python, "-m", "pip", "install", "--disable-pip-version-check", "--no-cache-dir", "--target", staging, "ViennaRNA==2.7.2")
	default:
		return fmt.Errorf("optional MCP %s has unsupported installer %q", definition.ID, definition.InstallKind)
	}
	if _, err := exec.LookPath(command.Path); err != nil {
		return fmt.Errorf("required installer %q is not available: %w", command.Path, err)
	}
	var output limitedOptionalBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("optional MCP installation cancelled or timed out: %w", ctx.Err())
		}
		return fmt.Errorf("optional MCP installer failed: %s", summarizeOptionalCommandError(err, output.String()))
	}
	if err := verifyOptionalInstall(ctx, definition, staging, s.bundledPython); err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return errors.New("optional MCP install target already exists; remove it from the managed MCP settings before retrying")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect optional MCP install target: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("activate optional MCP installation: %w", err)
	}
	return nil
}

func verifyOptionalInstall(ctx context.Context, definition optionalConnectorDefinition, root string, pythonProvider func() (string, error)) error {
	switch definition.ID {
	case "bundled:renkin-local":
		info, err := os.Stat(filepath.Join(root, "bin", executableName("renkin-mcp")))
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return errors.New("RENKIN installer completed without renkin-mcp binary")
		}
	case "bundled:rna-design-local":
		python := "python3"
		if pythonProvider != nil {
			var err error
			python, err = pythonProvider()
			if err != nil {
				return fmt.Errorf("managed Python runtime is unavailable for ViennaRNA validation: %w", err)
			}
		}
		check := exec.CommandContext(ctx, python, "-c", "import sys; sys.path.insert(0, sys.argv[1]); import RNA", root)
		if output, err := check.CombinedOutput(); err != nil {
			return fmt.Errorf("ViennaRNA import validation failed: %s", strings.TrimSpace(string(output)))
		}
	default:
		return errors.New("optional MCP install verification is not defined")
	}
	return nil
}

type limitedOptionalBuffer struct{ bytes.Buffer }

func (b *limitedOptionalBuffer) Write(p []byte) (int, error) {
	remaining := optionalInstallOutputLimit - b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return b.Buffer.Write(p)
}

func summarizeOptionalCommandError(err error, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return err.Error()
	}
	return truncateOptionalErrorText(output)
}

func truncateOptionalError(value error) string {
	if value == nil {
		return ""
	}
	return truncateOptionalErrorText(value.Error())
}

func truncateOptionalErrorText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 1200 {
		return value[:1200] + "…"
	}
	return value
}

func writeOptionalInstallState(root string, state optionalInstallState) error {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = optionalInstallStateVersion
	}
	state.UpdatedAt = state.UpdatedAt.UTC()
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := optionalConnectorStatePath(root, state.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".install-state-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (s *optionalInstallState) setProbeError(err error) {
	if err != nil {
		s.Error = "post-install MCP health probe failed: " + truncateOptionalError(err)
	}
}

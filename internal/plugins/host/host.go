package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"synon-go/internal/subprocess"
)

type Module struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

type ProcessManifest struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
}

type ServerManifest struct {
	APIMount     string           `json:"apiMount"`
	ContextEntry string           `json:"contextEntry,omitempty"`
	Process      *ProcessManifest `json:"-"`
}

type RuntimeStatus struct {
	Kind      string `json:"kind,omitempty"`
	Status    string `json:"status,omitempty"`
	PID       int    `json:"pid,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Plugin struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Description  string         `json:"description"`
	Author       string         `json:"author"`
	License      string         `json:"license"`
	Mount        string         `json:"mount"`
	PluginFormat string         `json:"pluginFormat"`
	Keywords     []string       `json:"keywords"`
	Modules      []Module       `json:"modules"`
	Server       ServerManifest `json:"server"`
	Runtime      RuntimeStatus  `json:"runtime,omitempty"`
	HooksConfig  map[string]any `json:"hooksConfig,omitempty"`
	root         string
}

func (p Plugin) Root() string {
	return p.root
}

type Host struct {
	mu        sync.RWMutex
	plugins   map[string]Plugin
	processes map[string]*externalProcess
}

type externalProcess struct {
	command *exec.Cmd
	control *subprocess.Control
}

func NewDefault() *Host {
	synon := Plugin{
		ID:           "synon",
		Name:         "synon",
		Version:      "1.1.5",
		Description:  "Compact Synon native plugin for Synon Link browser bridge, local device bridge, extension download, and capability reporting.",
		Author:       "Victor",
		License:      "Proprietary",
		Mount:        "/api/plugins/synon",
		PluginFormat: "synon_agent_native",
		Keywords:     []string{"synon", "synon-link", "browser-bridge", "local-device", "websocket"},
		Modules: []Module{
			{
				ID:          "capabilities",
				Label:       "Capabilities",
				Endpoint:    "/api/plugins/synon/capabilities",
				Description: "Compact Synon capability report for retained bridge, messaging, runtime, and tool surfaces.",
			},
			{
				ID:          "link-download",
				Label:       "Synon Link Download",
				Endpoint:    "/api/plugins/synon/link/download",
				Description: "Versioned Synon Link browser extension package download.",
			},
		},
		Server: ServerManifest{
			APIMount:     "/api/plugins/synon",
			ContextEntry: "builtin:synon-context",
		},
	}
	return New([]Plugin{synon})
}

func NewDefaultWithExternalDirectories(directories []string) (*Host, error) {
	host := NewDefault()
	plugins, err := LoadExternalDirectories(directories)
	if err != nil {
		return nil, err
	}
	for _, plugin := range plugins {
		if err := host.Add(plugin); err != nil {
			return nil, err
		}
	}
	return host, nil
}

func New(plugins []Plugin) *Host {
	items := make(map[string]Plugin, len(plugins))
	for _, plugin := range plugins {
		if plugin.ID == "" {
			continue
		}
		if plugin.Server.Process != nil && plugin.Runtime.Status == "" {
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "configured"}
		}
		items[plugin.ID] = plugin
	}
	return &Host{plugins: items, processes: make(map[string]*externalProcess)}
}

func (h *Host) List() []Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	plugins := make([]Plugin, 0, len(h.plugins))
	for _, plugin := range h.plugins {
		plugins = append(plugins, plugin)
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].ID < plugins[j].ID
	})
	return plugins
}

func (h *Host) Get(id string) (Plugin, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	plugin, ok := h.plugins[id]
	return plugin, ok
}

func (h *Host) MustGet(id string) (Plugin, error) {
	plugin, ok := h.Get(id)
	if !ok {
		return Plugin{}, fmt.Errorf("plugin not found: %s", id)
	}
	return plugin, nil
}

func (h *Host) Add(plugin Plugin) error {
	if err := validatePlugin(plugin); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.plugins[plugin.ID]; exists {
		return fmt.Errorf("plugin already registered: %s", plugin.ID)
	}
	if plugin.Server.Process != nil && plugin.Runtime.Status == "" {
		plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "configured"}
	}
	h.plugins[plugin.ID] = plugin
	return nil
}

func (h *Host) StartExternalProcesses(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	for id, plugin := range h.plugins {
		process := plugin.Server.Process
		if process == nil {
			continue
		}
		if _, running := h.processes[id]; running {
			continue
		}
		if err := subprocess.ValidateSpec("plugin subprocess", process.Command, process.Args); err != nil {
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("start plugin %s: %w", id, err))
			continue
		}
		environment, err := subprocess.BuildEnvironment("plugin subprocess", process.Env)
		if err != nil {
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("start plugin %s: %w", id, err))
			continue
		}
		cmd := exec.CommandContext(ctx, process.Command, process.Args...)
		if process.WorkingDir != "" {
			cmd.Dir = process.WorkingDir
		}
		cmd.Env = environment
		cmd.WaitDelay = 2 * time.Second
		control, err := subprocess.Prepare(cmd)
		if err != nil {
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("prepare plugin %s: %w", id, err))
			continue
		}
		if err := cmd.Start(); err != nil {
			_ = control.Close()
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("start plugin %s: %w", id, err))
			continue
		}
		if err := control.Attach(cmd); err != nil {
			_ = control.Kill(cmd)
			_ = cmd.Wait()
			_ = control.Close()
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("attach plugin %s process control: %w", id, err))
			continue
		}
		if err := ctx.Err(); err != nil {
			_ = control.Kill(cmd)
			_ = cmd.Wait()
			_ = control.Close()
			plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "failed", Error: err.Error()}
			h.plugins[id] = plugin
			errs = append(errs, fmt.Errorf("start plugin %s: %w", id, err))
			continue
		}
		running := &externalProcess{command: cmd, control: control}
		h.processes[id] = running
		plugin.Runtime = RuntimeStatus{
			Kind:      "external_process",
			Status:    "running",
			PID:       cmd.Process.Pid,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		}
		h.plugins[id] = plugin
		go h.waitForProcess(id, running)
	}
	return errors.Join(errs...)
}

func (h *Host) StopExternalProcesses() {
	h.mu.Lock()
	processes := h.processes
	h.processes = make(map[string]*externalProcess)
	for id, process := range processes {
		plugin := h.plugins[id]
		plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "stopped"}
		h.plugins[id] = plugin
		_ = process.control.Kill(process.command)
		_ = process.control.Close()
	}
	h.mu.Unlock()
}

func (h *Host) waitForProcess(id string, process *externalProcess) {
	err := process.command.Wait()
	closeErr := process.control.Close()
	if err == nil {
		err = closeErr
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.processes[id] != process {
		return
	}
	delete(h.processes, id)
	plugin := h.plugins[id]
	plugin.Runtime.PID = 0
	plugin.Runtime.StartedAt = ""
	if err != nil {
		plugin.Runtime.Status = "failed"
		plugin.Runtime.Error = err.Error()
	} else {
		plugin.Runtime.Status = "exited"
		plugin.Runtime.Error = ""
	}
	h.plugins[id] = plugin
}

func LoadExternalDirectories(directories []string) ([]Plugin, error) {
	plugins := make([]Plugin, 0, len(directories))
	var errs []error
	for _, directory := range directories {
		directory = strings.TrimSpace(directory)
		if directory == "" {
			continue
		}
		plugin, err := loadExternalDirectory(directory)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		plugins = append(plugins, plugin)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].ID < plugins[j].ID
	})
	return plugins, nil
}

type rawPluginManifest struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Description  string          `json:"description"`
	Author       json.RawMessage `json:"author"`
	License      string          `json:"license"`
	PluginFormat string          `json:"pluginFormat"`
	Keywords     []string        `json:"keywords"`
	Modules      []Module        `json:"modules"`
	Server       rawServer       `json:"server"`
	Hooks        json.RawMessage `json:"hooks"`
}

type rawServer struct {
	APIMount     string           `json:"apiMount"`
	ContextEntry string           `json:"contextEntry"`
	API          rawServerAPI     `json:"api"`
	Context      rawServerContext `json:"context"`
	Process      *ProcessManifest `json:"process"`
}

type rawServerAPI struct {
	Mount string `json:"mount"`
	Entry string `json:"entry"`
}

type rawServerContext struct {
	Entry string `json:"entry"`
}

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

func loadExternalDirectory(directory string) (Plugin, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return Plugin{}, fmt.Errorf("resolve plugin directory %s: %w", directory, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Plugin{}, fmt.Errorf("read plugin directory %s: %w", root, err)
	}
	if !info.IsDir() {
		return Plugin{}, fmt.Errorf("plugin path is not a directory: %s", root)
	}
	manifestPath := filepath.Join(root, ".synon-plugin", "plugin.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return Plugin{}, fmt.Errorf("read plugin manifest %s: %w", manifestPath, err)
	}
	var manifest rawPluginManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Plugin{}, fmt.Errorf("parse plugin manifest %s: %w", manifestPath, err)
	}
	plugin, err := manifest.toPlugin(root)
	if err != nil {
		return Plugin{}, fmt.Errorf("invalid plugin manifest %s: %w", manifestPath, err)
	}
	return plugin, nil
}

func (manifest rawPluginManifest) toPlugin(root string) (Plugin, error) {
	id := strings.TrimSpace(manifest.ID)
	if id == "" {
		id = strings.TrimSpace(manifest.Name)
	}
	id = strings.ToLower(id)
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = id
	}
	if isExcludedPlugin(id, name) {
		return Plugin{}, fmt.Errorf("excluded capability plugin is not part of this clean package: %s", name)
	}
	apiMount := strings.TrimSpace(manifest.Server.APIMount)
	if manifest.Server.API.Mount != "" {
		apiMount = strings.TrimSpace(manifest.Server.API.Mount)
	}
	if apiMount == "" {
		apiMount = "/api/plugins/" + id
	}
	contextEntry := strings.TrimSpace(manifest.Server.ContextEntry)
	if manifest.Server.Context.Entry != "" {
		contextEntry = strings.TrimSpace(manifest.Server.Context.Entry)
	}
	process := manifest.Server.Process
	if process != nil {
		resolved, err := resolveProcessManifest(root, *process)
		if err != nil {
			return Plugin{}, err
		}
		process = &resolved
	}
	pluginFormat := strings.TrimSpace(manifest.PluginFormat)
	if pluginFormat == "" {
		pluginFormat = "synon_agent_external"
	}
	plugin := Plugin{
		ID:           id,
		Name:         name,
		Version:      strings.TrimSpace(manifest.Version),
		Description:  strings.TrimSpace(manifest.Description),
		Author:       parseAuthor(manifest.Author),
		License:      strings.TrimSpace(manifest.License),
		Mount:        apiMount,
		PluginFormat: pluginFormat,
		Keywords:     append([]string(nil), manifest.Keywords...),
		Modules:      normalizeModules(manifest.Modules, id, apiMount),
		Server: ServerManifest{
			APIMount:     apiMount,
			ContextEntry: contextEntry,
			Process:      process,
		},
		Runtime: RuntimeStatus{Kind: "external_manifest", Status: "loaded"},
		root:    root,
	}
	if process != nil {
		plugin.Runtime = RuntimeStatus{Kind: "external_process", Status: "configured"}
	}
	hooks, err := loadPluginHooksConfig(root, manifest)
	if err != nil {
		return Plugin{}, err
	}
	plugin.HooksConfig = hooks
	if err := validatePlugin(plugin); err != nil {
		return Plugin{}, err
	}
	return plugin, nil
}

func loadPluginHooksConfig(root string, manifest rawPluginManifest) (map[string]any, error) {
	merged := map[string]any{}
	standardPath := filepath.Join(root, "hooks", "hooks.json")
	if _, err := os.Stat(standardPath); err == nil {
		hooks, err := readPluginHooksFile(standardPath)
		if err != nil {
			return nil, err
		}
		mergePluginHooks(merged, hooks)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read plugin hooks %s: %w", standardPath, err)
	}
	if len(manifest.Hooks) > 0 {
		var value any
		if err := json.Unmarshal(manifest.Hooks, &value); err != nil {
			return nil, fmt.Errorf("parse plugin manifest hooks for %s: %w", manifest.Name, err)
		}
		hookConfigs, err := pluginHooksFromManifestValue(root, value)
		if err != nil {
			return nil, err
		}
		for _, hooks := range hookConfigs {
			mergePluginHooks(merged, hooks)
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}

func pluginHooksFromManifestValue(root string, value any) ([]map[string]any, error) {
	switch typed := value.(type) {
	case string:
		path, err := resolveContainedPluginPath(root, typed, "hooks file", false)
		if err != nil {
			return nil, err
		}
		hooks, err := readPluginHooksFile(path)
		if err != nil {
			return nil, err
		}
		return []map[string]any{hooks}, nil
	case []any:
		result := []map[string]any{}
		for _, item := range typed {
			hooks, err := pluginHooksFromManifestValue(root, item)
			if err != nil {
				return nil, err
			}
			result = append(result, hooks...)
		}
		return result, nil
	case map[string]any:
		if hooks, ok := typed["hooks"].(map[string]any); ok {
			return []map[string]any{hooks}, nil
		}
		return []map[string]any{typed}, nil
	default:
		return nil, fmt.Errorf("unsupported plugin hooks manifest value %T", value)
	}
}

func readPluginHooksFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin hooks %s: %w", path, err)
	}
	var wrapper map[string]any
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse plugin hooks %s: %w", path, err)
	}
	if hooks, ok := wrapper["hooks"].(map[string]any); ok {
		return hooks, nil
	}
	return wrapper, nil
}

func mergePluginHooks(dst map[string]any, src map[string]any) {
	for event, rawMatchers := range src {
		incoming, ok := rawMatchers.([]any)
		if !ok {
			continue
		}
		existing, _ := dst[event].([]any)
		dst[event] = append(existing, incoming...)
	}
}

func resolveProcessManifest(root string, process ProcessManifest) (ProcessManifest, error) {
	process.Command = strings.TrimSpace(process.Command)
	if process.Command == "" {
		return ProcessManifest{}, fmt.Errorf("server.process.command is required")
	}
	if shouldResolvePluginPath(process.Command) {
		resolved, err := resolveContainedPluginPath(root, process.Command, "process command", false)
		if err != nil {
			return ProcessManifest{}, err
		}
		process.Command = resolved
	}
	if process.WorkingDir == "" {
		process.WorkingDir = root
	} else {
		resolved, err := resolveContainedPluginPath(root, process.WorkingDir, "process working directory", true)
		if err != nil {
			return ProcessManifest{}, err
		}
		process.WorkingDir = resolved
	}
	return process, nil
}

func resolveContainedPluginPath(root string, value string, label string, requireDirectory bool) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve plugin root: %w", err)
	}
	target := value
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve plugin %s: %w", label, err)
	}
	if !pathWithinPluginRoot(root, target) {
		return "", fmt.Errorf("plugin %s must stay under %s", label, root)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve plugin root links: %w", err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve plugin %s %s: %w", label, target, err)
	}
	if !pathWithinPluginRoot(realRoot, realTarget) {
		return "", fmt.Errorf("plugin %s resolves outside %s", label, root)
	}
	info, err := os.Stat(realTarget)
	if err != nil {
		return "", fmt.Errorf("read plugin %s %s: %w", label, target, err)
	}
	if requireDirectory && !info.IsDir() {
		return "", fmt.Errorf("plugin %s is not a directory: %s", label, target)
	}
	if !requireDirectory && !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin %s is not a regular file: %s", label, target)
	}
	return target, nil
}

func pathWithinPluginRoot(root string, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func shouldResolvePluginPath(value string) bool {
	if filepath.IsAbs(value) {
		return false
	}
	if strings.HasPrefix(value, "."+string(filepath.Separator)) || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return true
	}
	return strings.ContainsAny(value, `/\`)
}

func parseAuthor(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return object.Name
	}
	return ""
}

func normalizeModules(modules []Module, id string, apiMount string) []Module {
	if len(modules) > 0 {
		out := append([]Module(nil), modules...)
		sort.Slice(out, func(i, j int) bool {
			return out[i].ID < out[j].ID
		})
		return out
	}
	return []Module{
		{
			ID:          "api",
			Label:       "API",
			Endpoint:    apiMount,
			Description: fmt.Sprintf("External plugin API mount for %s.", id),
		},
	}
}

func validatePlugin(plugin Plugin) error {
	if !pluginIDPattern.MatchString(plugin.ID) {
		return fmt.Errorf("invalid plugin id: %s", plugin.ID)
	}
	if isExcludedPlugin(plugin.ID, plugin.Name) {
		return fmt.Errorf("excluded capability plugin is not part of this clean package: %s", plugin.ID)
	}
	expectedMount := "/api/plugins/" + plugin.ID
	if plugin.Mount == "" {
		return fmt.Errorf("plugin %s mount is required", plugin.ID)
	}
	if plugin.Mount != expectedMount && !strings.HasPrefix(plugin.Mount, expectedMount+"/") {
		return fmt.Errorf("plugin %s mount %q must stay under %s", plugin.ID, plugin.Mount, expectedMount)
	}
	if plugin.Server.APIMount == "" {
		return fmt.Errorf("plugin %s server api mount is required", plugin.ID)
	}
	if plugin.Server.APIMount != expectedMount && !strings.HasPrefix(plugin.Server.APIMount, expectedMount+"/") {
		return fmt.Errorf("plugin %s server api mount %q must stay under %s", plugin.ID, plugin.Server.APIMount, expectedMount)
	}
	return nil
}

func isExcludedPlugin(id string, name string) bool {
	value := strings.ToLower(id + " " + name)
	excluded := []string{
		"admet",
		"markush",
		"knowledge",
		"daily briefing",
		"daily-briefing",
		"molecular",
		"molecular modeling",
		"pcc",
		"pcc selector",
	}
	for _, token := range excluded {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

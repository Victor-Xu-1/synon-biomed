package registry

import (
	"fmt"
	"strings"

	"synon-go/internal/harnesscontract"
)

// ToolExposure is the single model-projection decision stored with a Tool.
// Direct tools are always present in the root turn snapshot, deferred tools
// require an explicit task-scoped activation, and hidden tools are executable
// only through trusted service, fixed-job, or compatibility entry points.
type ToolExposure string

const (
	ToolExposureDirect   ToolExposure = "direct"
	ToolExposureDeferred ToolExposure = "deferred"
	ToolExposureHidden   ToolExposure = "hidden"
)

// Build validates and seals one exact Tool registration set. Duplicate names
// are compared case-insensitively because provider function namespaces are not
// a safe place to preserve two executors that differ only by case.
func Build(tools []Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(tools))}
	if err := registry.Register(tools...); err != nil {
		return nil, err
	}
	return registry, nil
}

// Register atomically adds exact Tool contracts to an existing registry using
// the same normalization and collision gate as Build.
func (registry *Registry) Register(tools ...Tool) error {
	if registry == nil {
		return fmt.Errorf("Tool registry is nil")
	}
	if registry.tools == nil {
		registry.tools = make(map[string]Tool, len(tools))
	}
	identities := make(map[string]string, len(registry.tools)+len(tools))
	for name := range registry.tools {
		identities[strings.ToLower(strings.TrimSpace(name))] = name
	}
	pending := make([]Tool, 0, len(tools))
	for index, tool := range tools {
		registered, err := normalizeRegistration(tool)
		if err != nil {
			return fmt.Errorf("register Tool at index %d: %w", index, err)
		}
		identity := strings.ToLower(registered.Name)
		if previous, duplicate := identities[identity]; duplicate {
			return fmt.Errorf("competing Tool registrations %q and %q", previous, registered.Name)
		}
		identities[identity] = registered.Name
		pending = append(pending, registered)
	}
	for _, registered := range pending {
		registry.tools[registered.Name] = registered
	}
	return nil
}

func (registry *Registry) MustRegister(tools ...Tool) {
	if err := registry.Register(tools...); err != nil {
		panic(err)
	}
}

func normalizeRegistration(tool Tool) (Tool, error) {
	tool.Name = strings.TrimSpace(tool.Name)
	if tool.Name == "" {
		return Tool{}, fmt.Errorf("Tool name is empty")
	}
	if tool.Exposure == "" {
		tool.Exposure = defaultToolExposure(tool.Name)
	}
	switch tool.Exposure {
	case ToolExposureDirect, ToolExposureDeferred, ToolExposureHidden:
		return tool, nil
	default:
		return Tool{}, fmt.Errorf("Tool %q has invalid exposure %q", tool.Name, tool.Exposure)
	}
}

func defaultToolExposure(name string) ToolExposure {
	switch harnesscontract.ClassifyToolSurface(name) {
	case harnesscontract.ToolSurfaceModelRoot:
		return ToolExposureDirect
	case harnesscontract.ToolSurfaceCompatibilityAPI:
		return ToolExposureDeferred
	default:
		return ToolExposureHidden
	}
}

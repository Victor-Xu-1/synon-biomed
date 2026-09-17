package kernel

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
)

const bundledMCPPythonEnvironment = "synon-biomed-mcp-python"

//go:embed mcp_requirements.lock
var bundledMCPPythonRequirementsLock []byte

var bundledMCPPythonImportNames = []string{
	"mcp",
	"httpx",
	"requests",
	"jsonschema",
	"lxml",
}

// MCPPythonEnvironmentName is the stable managed environment name used by
// bundled MCP processes. Keeping it separate from the scientific base
// environment prevents MCP protocol dependencies from silently changing the
// user's analysis runtime.
func (m *Manager) MCPPythonEnvironmentName() string {
	return bundledMCPPythonEnvironment
}

// MCPPythonRequirements parses the checked-in dependency contract used by the
// MCP environment provisioner. The same validation used for managed package
// requests rejects malformed or duplicate requirements before installation.
func (m *Manager) MCPPythonRequirements() ([]string, error) {
	lines := strings.Split(string(bundledMCPPythonRequirementsLock), "\n")
	specs := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		specs = append(specs, line)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("bundled MCP Python dependency lock is empty")
	}
	return validateManagedPackageSpecs(specs)
}

// ProvisionBundledMCPPythonEnvironment creates or refreshes the managed MCP
// environment from the verified scientific Python base. It deliberately uses
// CreateManagedEnvironment so the normal supervisor, provenance marker, and
// import smoke checks remain the single installation path.
func (m *Manager) ProvisionBundledMCPPythonEnvironment(ctx context.Context) (ManagedEnvironment, error) {
	requirements, err := m.MCPPythonRequirements()
	if err != nil {
		return ManagedEnvironment{}, err
	}
	if _, err := m.ManagedPythonActivePrefix(); err != nil {
		return ManagedEnvironment{}, fmt.Errorf("verified scientific Python base is unavailable: %w", err)
	}
	return m.CreateManagedEnvironment(ctx, CreateManagedEnvironmentInput{
		Name:              bundledMCPPythonEnvironment,
		Language:          "python",
		PythonVersion:     "3.11",
		SourceEnvironment: m.ManagedPythonEnvironmentName(),
		PipPhases:         [][]string{requirements},
		ImportNames:       bundledMCPPythonImportNames,
		OperationID:       "bundled-mcp-python-v2",
	})
}

// BundledMCPPythonExecutable resolves the verified interpreter for bundled MCP
// processes. Callers should keep this provider lazy because provisioning is
// asynchronous and may still be in progress when the directory is created.
func (m *Manager) BundledMCPPythonExecutable() (string, error) {
	return m.managedEnvironmentExecutable(bundledMCPPythonEnvironment, "python")
}

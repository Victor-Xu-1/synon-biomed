package mcpdirectory

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/ketchermcp"
	"synon-go/internal/tools/mcpstdio"
)

type bundledConnector struct {
	ID                 string                `json:"id"`
	Name               string                `json:"name"`
	DisplayName        string                `json:"displayName"`
	Description        string                `json:"description"`
	Package            string                `json:"package,omitempty"`
	Config             mcpstdio.ServerConfig `json:"config,omitempty"`
	DefinitionSHA256   string                `json:"definitionSha256,omitempty"`
	AuthRequired       bool                  `json:"authRequired,omitempty"`
	OAuthSupported     bool                  `json:"oauthSupported,omitempty"`
	AuthHint           string                `json:"authHint,omitempty"`
	APIKeyHeader       string                `json:"apiKeyHeader,omitempty"`
	APIKeyQueryParam   string                `json:"apiKeyQueryParam,omitempty"`
	APIKeyPrefix       string                `json:"apiKeyPrefix,omitempty"`
	APIKeyLabel        string                `json:"apiKeyLabel,omitempty"`
	HostedBySynon      bool                  `json:"hostedBySynon,omitempty"`
	Upstreams          []ConnectorUpstream   `json:"upstreams,omitempty"`
	RuntimeEnvironment []string              `json:"runtimeEnvironment,omitempty"`
	Deferred           bool
	OptionalID         string
	useBundledPython   bool
}

//go:embed bundled_metadata.json
var bundledMetadataJSON []byte

var v11BundledDefinitions = loadBundledDefinitions()

func loadBundledDefinitions() []bundledConnector {
	var definitions []bundledConnector
	if err := json.Unmarshal(bundledMetadataJSON, &definitions); err != nil {
		panic("decode embedded MCP connector metadata: " + err.Error())
	}
	for _, definition := range definitions {
		for _, name := range definition.RuntimeEnvironment {
			if !validBundledRuntimeEnvironmentName(name) {
				panic("decode embedded MCP connector metadata: invalid runtime environment name " + name)
			}
		}
	}
	return definitions
}

func validBundledRuntimeEnvironmentName(name string) bool {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "SYNON_") || len(name) > 128 {
		return false
	}
	for _, character := range name {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	upper := strings.ToUpper(name)
	for _, sensitive := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "COOKIE", "AUTH"} {
		if strings.Contains(upper, sensitive) {
			return false
		}
	}
	return true
}

// bundledConnectorExecutable returns the process that hosts native bundled
// connectors. Managed deployments may pin it explicitly through
// SYNON_BUNDLED_CONNECTOR_EXECUTABLE; otherwise the current executable is
// used, matching packaged layouts where the product binary ships the
// "mcp-ketcher" subcommand.
func bundledConnectorExecutable() (string, bool) {
	if configured := strings.TrimSpace(os.Getenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE")); configured != "" {
		return filepath.Clean(configured), true
	}
	executable, err := os.Executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return "synon-go", false
	}
	return executable, false
}

// isGoTestBinary reports whether executable is a `go test` binary. Test
// binaries do not dispatch product subcommands such as "mcp-ketcher"; spawning
// one would recursively run the entire test suite instead of serving a
// connector.
func isGoTestBinary(executable string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(executable)))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func defaultBundledConnectors() map[string]bundledConnector {
	root := discoverBioToolsRoot()
	bioToolsDefinitionSHA256 := bundledBioToolsDefinitionSHA256(root)
	executable, overridden := bundledConnectorExecutable()
	out := make(map[string]bundledConnector, len(v11BundledDefinitions))
	for _, item := range v11BundledDefinitions {
		if item.Package != "" {
			item.Config = mcpstdio.ServerConfig{Type: "stdio", Scope: "bundled"}
			item.Config.Command = "python3"
			item.Config.Args = []string{filepath.Join(root, "run_server.py"), item.Package}
			for _, name := range item.RuntimeEnvironment {
				if value := strings.TrimSpace(os.Getenv(name)); value != "" {
					if item.Config.Env == nil {
						item.Config.Env = map[string]string{}
					}
					item.Config.Env[name] = value
				}
			}
			item.DefinitionSHA256 = bioToolsDefinitionSHA256
		} else if item.ID == "bundled:ketcher-chemistry" {
			item.Config = mcpstdio.ServerConfig{Type: "stdio", Scope: "bundled"}
			if !overridden && isGoTestBinary(executable) {
				continue
			}
			item.Config.Command = executable
			item.Config.Args = []string{
				"mcp-ketcher", "--widget-gzip", ketchermcp.DiscoverWidgetPath(),
			}
		} else {
			item.Config.Scope = "bundled"
			item.Config.Name = item.Name
			item.DefinitionSHA256 = bundledRemoteDefinitionSHA256(item)
		}
		out[item.ID] = item
	}
	return out
}

func bundledRemoteDefinitionSHA256(item bundledConnector) string {
	canonical := struct {
		ID, Name, DisplayName, Description string
		Config                             mcpstdio.ServerConfig
		AuthRequired                       bool
		OAuthSupported                     bool
		AuthHint, APIKeyHeader             string
		APIKeyQueryParam                   string
		APIKeyPrefix, APIKeyLabel          string
		Upstreams                          []ConnectorUpstream
	}{
		ID: item.ID, Name: item.Name, DisplayName: item.DisplayName,
		Description: item.Description, Config: item.Config,
		AuthRequired: item.AuthRequired, OAuthSupported: item.OAuthSupported, AuthHint: item.AuthHint,
		APIKeyHeader: item.APIKeyHeader, APIKeyPrefix: item.APIKeyPrefix,
		APIKeyQueryParam: item.APIKeyQueryParam,
		APIKeyLabel:      item.APIKeyLabel,
		Upstreams:        item.Upstreams,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		panic("encode bundled remote MCP connector metadata: " + err.Error())
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}

func bundledBioToolsDefinitionSHA256(root string) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(root), "bio-tools.manifest.json"))
	if err != nil || len(raw) == 0 || len(raw) > maxManifestBytes {
		return ""
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}

func discoverBioToolsRoot() string {
	if root := os.Getenv("SYNON_BIO_TOOLS_ROOT"); root != "" {
		return filepath.Clean(root)
	}
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.Join(dir, "assets", "optional", "mcp-servers", "bio-tools"))
			next := filepath.Dir(dir)
			if next == dir {
				break
			}
		}
	}
	for _, root := range candidates {
		if info, err := os.Stat(filepath.Join(root, "run_server.py")); err == nil && !info.IsDir() {
			return root
		}
	}
	return filepath.Join("assets", "optional", "mcp-servers", "bio-tools")
}

func orderedBundled(connectors map[string]bundledConnector) []bundledConnector {
	out := make([]bundledConnector, 0, len(connectors))
	seen := make(map[string]struct{}, len(connectors))
	for _, definition := range v11BundledDefinitions {
		if item, ok := connectors[definition.ID]; ok {
			out = append(out, item)
			seen[item.ID] = struct{}{}
		}
	}
	extras := make([]bundledConnector, 0)
	for id, item := range connectors {
		if _, ok := seen[id]; !ok {
			extras = append(extras, item)
		}
	}
	sort.Slice(extras, func(i, j int) bool { return extras[i].ID < extras[j].ID })
	return append(out, extras...)
}
